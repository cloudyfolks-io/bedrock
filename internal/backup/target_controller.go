package backup

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/target"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	SystemNamespace       = "bedrock-system"
	TargetSettingKey      = "backup.target"
	CredentialsSettingKey = "backup.credentials-secret"

	ReasonTargetInvalid      = "BackupTargetInvalid"
	ReasonCredentialsInvalid = "BackupCredentialsInvalid"

	requeueInstalling = 30 * time.Second
	requeueIdle       = 5 * time.Minute

	keyAccessKeyID     = "access-key-id"
	keySecretAccessKey = "secret-access-key"
	keyRepositoryPass  = "repository-password"
	keyCA              = "ca.crt"
)

var requiredCredentialKeys = []string{keyAccessKeyID, keySecretAccessKey, keyRepositoryPass}

type TargetReconciler struct {
	Client    client.Client
	APIReader client.Reader
	Recorder  record.EventRecorder
}

func (r *TargetReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	rawTarget, err := settingValue(ctx, r.Client, TargetSettingKey)
	if err != nil {
		return ctrl.Result{}, err
	}
	if rawTarget == "" {
		return r.finish(r.removeOwned(ctx))
	}
	tgt, err := target.Parse(rawTarget)
	if err != nil {
		r.warn(TargetSettingKey, ReasonTargetInvalid, err.Error())
		return ctrl.Result{RequeueAfter: requeueIdle}, nil
	}
	secretName, err := settingValue(ctx, r.Client, CredentialsSettingKey)
	if err != nil {
		return ctrl.Result{}, err
	}
	secret, problem, err := r.credentialsSecret(ctx, secretName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if problem != "" {
		r.warn(TargetSettingKey, ReasonCredentialsInvalid, problem)
		return ctrl.Result{RequeueAfter: requeueIdle}, nil
	}
	return r.finish(r.applyAll(ctx, tgt, secret))
}

func (r *TargetReconciler) finish(err error) (ctrl.Result, error) {
	switch {
	case err == nil:
		return ctrl.Result{RequeueAfter: requeueIdle}, nil
	case isVeleroMissing(err):
		return ctrl.Result{RequeueAfter: requeueInstalling}, nil
	default:
		return ctrl.Result{}, err
	}
}

func (r *TargetReconciler) warn(settingName, reason, message string) {
	ref := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: settingName}}
	r.Recorder.Event(ref, corev1.EventTypeWarning, reason, message)
}

func (r *TargetReconciler) credentialsSecret(ctx context.Context, name string) (*corev1.Secret, string, error) {
	if name == "" {
		return nil, CredentialsSettingKey + " is empty", nil
	}
	secret := &corev1.Secret{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: SystemNamespace, Name: name}, secret)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Sprintf("secret %s/%s does not exist", SystemNamespace, name), nil
	}
	if err != nil {
		return nil, "", err
	}
	if missing := missingKeys(secret.Data); len(missing) > 0 {
		return nil, "secret " + name + " lacks the keys " + strings.Join(missing, ", "), nil
	}
	return secret, "", nil
}

func missingKeys(data map[string][]byte) []string {
	var missing []string
	for _, key := range requiredCredentialKeys {
		if len(data[key]) == 0 {
			missing = append(missing, key)
		}
	}
	return missing
}

func (r *TargetReconciler) applyAll(ctx context.Context, tgt target.Target, secret *corev1.Secret) error {
	objects := []runtime.Object{
		RepositorySecret(string(secret.Data[keyRepositoryPass])),
		CredentialsSecret(string(secret.Data[keyAccessKeyID]), string(secret.Data[keySecretAccessKey])),
		SnapshotClass(),
		StorageLocation(tgt, secret.Data[keyCA]),
	}
	for _, obj := range objects {
		if err := ssa.Apply(ctx, r.Client, obj, v1alpha1.BackupFieldManager); err != nil {
			return err
		}
	}
	return nil
}

func (r *TargetReconciler) removeOwned(ctx context.Context) error {
	owned := []client.Object{
		veleroStub("BackupStorageLocation", LocationName),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: CredentialsSecretName, Namespace: VeleroNamespace}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: RepositorySecretName, Namespace: VeleroNamespace}},
	}
	for _, obj := range owned {
		if err := deleteOwned(ctx, r.Client, obj); err != nil {
			return err
		}
	}
	return nil
}

func veleroStub(kind, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "velero.io", Version: "v1", Kind: kind})
	u.SetNamespace(VeleroNamespace)
	u.SetName(name)
	return u
}

func deleteOwned(ctx context.Context, c client.Client, obj client.Object) error {
	err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, owned := obj.GetLabels()[v1alpha1.LabelKind]; !owned {
		return nil
	}
	return client.IgnoreNotFound(c.Delete(ctx, obj))
}

func isVeleroMissing(err error) bool {
	return meta.IsNoMatchError(err) || apierrors.IsNotFound(err)
}

func settingValue(ctx context.Context, c client.Client, key string) (string, error) {
	setting := &v1alpha1.Setting{}
	err := c.Get(ctx, types.NamespacedName{Name: key}, setting)
	if err != nil && !apierrors.IsNotFound(err) {
		return "", err
	}
	if err == nil && setting.Spec.Value != "" {
		return setting.Spec.Value, nil
	}
	return settingDefault(key), nil
}

func settingDefault(key string) string {
	def, _ := settings.Lookup(key)
	return def.Default
}

var singleRequest = []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "backup"}}}

func toSingleRequest(context.Context, client.Object) []reconcile.Request {
	return singleRequest
}

func backupSettingNames(obj client.Object) bool {
	return slices.Contains([]string{TargetSettingKey, CredentialsSettingKey}, obj.GetName())
}

func (r *TargetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("backup-target").
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(toSingleRequest), builder.WithPredicates(predicate.NewPredicateFuncs(backupSettingNames))).
		Complete(r)
}
