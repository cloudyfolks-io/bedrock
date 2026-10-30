package database

import (
	"context"
	"path"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/target"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	OffsiteName         = "database-offsite"
	OffsiteSecretName   = "database-offsite-s3"
	OffsiteScheduleKey  = "database.backup.offsite-schedule"
	OffsiteKeyDir       = "databases"
	RcloneRepository    = "docker.io/rclone/rclone"
	offsiteTargetKey    = "backup.target"
	offsiteCredsKey     = "backup.credentials-secret"
	offsiteRequeue      = 5 * time.Minute
	offsiteCAMountPath  = "/etc/offsite-ca"
	offsiteBackoffLimit = 2
)

type OffsiteSpec struct {
	Image    string
	Schedule string
	Source   string
	Target   target.Target
	HasCA    bool
}

func OffsiteObjects(spec OffsiteSpec) []runtime.Object {
	return []runtime.Object{offsiteCronJob(spec)}
}

func OffsiteSecret(access, secret, ca []byte) *corev1.Secret {
	data := map[string][]byte{"access-key-id": access, "secret-access-key": secret}
	if len(ca) > 0 {
		data["ca.crt"] = ca
	}
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: OffsiteSecretName, Namespace: PlatformNamespace, Labels: offsiteLabels()},
		Data:       data,
	}
}

func offsiteLabels() map[string]string {
	return map[string]string{LabelManagedBy: ManagedBy, LabelComponent: OffsiteName}
}

func secretEnv(name, secret, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: key}}}
}

func offsiteCronJob(spec OffsiteSpec) *batchv1.CronJob {
	env := []corev1.EnvVar{
		{Name: "RCLONE_CONFIG_SRC_TYPE", Value: "s3"},
		{Name: "RCLONE_CONFIG_SRC_PROVIDER", Value: "Ceph"},
		{Name: "RCLONE_CONFIG_SRC_ENDPOINT", Value: objectstorage.Endpoint},
		{Name: "RCLONE_CONFIG_SRC_REGION", Value: objectstorage.Region},
		secretEnv("RCLONE_CONFIG_SRC_ACCESS_KEY_ID", BackupSecretName, "ACCESS_KEY_ID"),
		secretEnv("RCLONE_CONFIG_SRC_SECRET_ACCESS_KEY", BackupSecretName, "ACCESS_SECRET_KEY"),
		{Name: "RCLONE_CONFIG_DST_TYPE", Value: "s3"},
		{Name: "RCLONE_CONFIG_DST_PROVIDER", Value: "Other"},
		{Name: "RCLONE_CONFIG_DST_ENDPOINT", Value: target.S3URL(spec.Target)},
		{Name: "RCLONE_CONFIG_DST_REGION", Value: spec.Target.Region},
		secretEnv("RCLONE_CONFIG_DST_ACCESS_KEY_ID", OffsiteSecretName, "access-key-id"),
		secretEnv("RCLONE_CONFIG_DST_SECRET_ACCESS_KEY", OffsiteSecretName, "secret-access-key"),
	}
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	if spec.HasCA {
		env = append(env, corev1.EnvVar{Name: "RCLONE_CA_CERT", Value: offsiteCAMountPath + "/ca.crt"})
		volumes = []corev1.Volume{{Name: "ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: OffsiteSecretName, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}}}}
		mounts = []corev1.VolumeMount{{Name: "ca", MountPath: offsiteCAMountPath, ReadOnly: true}}
	}
	destination := "DST:" + path.Join(spec.Target.Bucket, spec.Target.Prefix, OffsiteKeyDir)
	backoff, history := int32(offsiteBackoffLimit), int32(1)
	return &batchv1.CronJob{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "CronJob"},
		ObjectMeta: metav1.ObjectMeta{Name: OffsiteName, Namespace: PlatformNamespace, Labels: offsiteLabels()},
		Spec: batchv1.CronJobSpec{
			Schedule:                   spec.Schedule,
			TimeZone:                   new("UTC"),
			ConcurrencyPolicy:          batchv1.ForbidConcurrent,
			SuccessfulJobsHistoryLimit: &history,
			FailedJobsHistoryLimit:     &history,
			JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{
				BackoffLimit: &backoff,
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: offsiteLabels()},
					Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Volumes:       volumes,
						Containers: []corev1.Container{{
							Name: "rclone", Image: spec.Image, Env: env, VolumeMounts: mounts,
							Args: []string{"copy", "SRC:" + spec.Source, destination, "--s3-no-check-bucket"},
						}},
					},
				},
			}},
		},
	}
}

type OffsiteReconciler struct {
	Client client.Client
	Live   client.Reader
	Images []string
}

func (r *OffsiteReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	cfg, err := LoadConfig(ctx, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	rawTarget, err := offsiteSetting(ctx, r.Client, offsiteTargetKey)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !cfg.Enabled || rawTarget == "" {
		return ctrl.Result{}, r.removeOwned(ctx)
	}
	tgt, err := target.Parse(rawTarget)
	if err != nil {
		return ctrl.Result{RequeueAfter: offsiteRequeue}, nil
	}
	image, found := rcloneImage(r.Images)
	if !found {
		return ctrl.Result{RequeueAfter: offsiteRequeue}, nil
	}
	secretName, err := offsiteSetting(ctx, r.Client, offsiteCredsKey)
	if err != nil {
		return ctrl.Result{}, err
	}
	var creds corev1.Secret
	err = r.Live.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: secretName}, &creds)
	if secretName == "" || apierrors.IsNotFound(err) {
		return ctrl.Result{RequeueAfter: offsiteRequeue}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	schedule, err := offsiteSetting(ctx, r.Client, OffsiteScheduleKey)
	if err != nil {
		return ctrl.Result{}, err
	}
	spec := OffsiteSpec{Image: image, Schedule: schedule, Source: cfg.BackupBucket, Target: tgt, HasCA: len(creds.Data["ca.crt"]) > 0}
	objects := append([]runtime.Object{OffsiteSecret(creds.Data["access-key-id"], creds.Data["secret-access-key"], creds.Data["ca.crt"])}, OffsiteObjects(spec)...)
	for _, obj := range objects {
		if err := ssa.Apply(ctx, r.Client, obj, v1alpha1.DatabaseFieldManager); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: offsiteRequeue}, nil
}

func (r *OffsiteReconciler) removeOwned(ctx context.Context) error {
	for _, obj := range []client.Object{
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: OffsiteName, Namespace: PlatformNamespace}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: OffsiteSecretName, Namespace: PlatformNamespace}},
	} {
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, obj)); err != nil {
			return err
		}
	}
	return nil
}

func rcloneImage(images []string) (string, bool) {
	for _, image := range images {
		if len(image) > len(RcloneRepository) && image[:len(RcloneRepository)] == RcloneRepository && (image[len(RcloneRepository)] == ':' || image[len(RcloneRepository)] == '@') {
			return image, true
		}
	}
	return "", false
}

func offsiteSetting(ctx context.Context, c client.Reader, key string) (string, error) {
	var setting v1alpha1.Setting
	err := c.Get(ctx, client.ObjectKey{Name: key}, &setting)
	if client.IgnoreNotFound(err) != nil {
		return "", err
	}
	if setting.Spec.Value != "" {
		return setting.Spec.Value, nil
	}
	def, _ := settings.Lookup(key)
	return def.Default, nil
}

func offsiteSettingName(obj client.Object) bool {
	switch obj.GetName() {
	case offsiteTargetKey, offsiteCredsKey, OffsiteScheduleKey:
		return true
	}
	return databaseSetting(obj)
}

var offsiteRequest = []reconcile.Request{{NamespacedName: types.NamespacedName{Name: OffsiteName}}}

func (r *OffsiteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("database-offsite").
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request { return offsiteRequest }), builder.WithPredicates(predicate.NewPredicateFuncs(offsiteSettingName))).
		Complete(r)
}
