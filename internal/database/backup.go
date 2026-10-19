package database

import (
	"context"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/mysql"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

const (
	BackupSecretName     = "database-backups-s3"
	SizeSampleInterval   = 10 * time.Minute
	annotationBucket     = "bedrock.cloudyfolks.io/bucket-ready"
	maxBackupItems       = 100
	backupPhaseCompleted = "completed"
)

type BackupStore struct {
	S3       *s3.Client
	Endpoint string
	Region   string
}

func BackupIdentity() platform.Identity {
	return platform.Identity{
		Store:           objectstorage.Store(),
		Name:            BackupSecretName,
		TargetNamespace: PlatformNamespace,
		AccessKeyNames:  []string{"ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"},
		SecretKeyNames:  []string{"ACCESS_SECRET_KEY", "AWS_SECRET_ACCESS_KEY"},
		Manager:         v1alpha1.DatabaseFieldManager,
	}
}

func (s BackupStore) Credentials(secret corev1.Secret) s3.Credentials {
	return s3.Credentials{AccessKey: string(secret.Data["ACCESS_KEY_ID"]), SecretKey: string(secret.Data["ACCESS_SECRET_KEY"])}
}

func EnsureBackupStore(ctx context.Context, c client.Client, store BackupStore, bucket string) (bool, error) {
	ready, err := platform.EnsureStoreIdentity(ctx, c, BackupIdentity())
	if err != nil || !ready {
		return false, err
	}
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: BackupSecretName}, &secret); err != nil {
		return false, err
	}
	if secret.Annotations[annotationBucket] == bucket {
		return true, nil
	}
	if err := platform.EnsureStoreBucket(ctx, store.S3, store.Endpoint, store.Region, store.Credentials(secret), bucket); err != nil {
		return false, err
	}
	return true, annotateBucket(ctx, c, bucket)
}

func annotateBucket(ctx context.Context, c client.Client, bucket string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var secret corev1.Secret
		if err := c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: BackupSecretName}, &secret); err != nil {
			return err
		}
		before := secret.DeepCopy()
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[annotationBucket] = bucket
		return c.Patch(ctx, &secret, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.DatabaseFieldManager))
	})
}

func BackupCredentials(ctx context.Context, c client.Reader) (s3.Credentials, error) {
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: BackupSecretName}, &secret); err != nil {
		return s3.Credentials{}, err
	}
	return BackupStore{}.Credentials(secret), nil
}

func BackupSpecOf(cfg Config, store BackupStore, enabled bool) engine.BackupSpec {
	return engine.BackupSpec{
		Enabled: enabled, Bucket: cfg.BackupBucket, Endpoint: store.Endpoint, Region: store.Region,
		RetentionDays: cfg.RetentionDays, SecretName: BackupSecretName,
	}
}

func SizeSampleDue(previous *v1alpha1.DatabaseBackupStatus, now time.Time) bool {
	return previous == nil || previous.SizeSampledAt == nil || now.Sub(previous.SizeSampledAt.Time) >= SizeSampleInterval
}

func ListBackups(ctx context.Context, c client.Reader, db v1alpha1.Database) ([]*unstructured.Unstructured, error) {
	gvk, supported := backupGVK(db.Spec.Engine)
	if !supported {
		return nil, nil
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	err := c.List(ctx, list, client.InNamespace(PlatformNamespace))
	if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var own []*unstructured.Unstructured
	for i := range list.Items {
		if backupClusterOf(&list.Items[i]) == Name(db.UID) {
			own = append(own, &list.Items[i])
		}
	}
	return own, nil
}

func timeField(obj *unstructured.Unstructured, field string) *metav1.Time {
	raw, _, _ := unstructured.NestedString(obj.Object, "status", field)
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &metav1.Time{Time: parsed.UTC()}
}

func BackupStatusOf(backups []*unstructured.Unstructured, now time.Time, sizeBytes int64, sampledAt *metav1.Time) *v1alpha1.DatabaseBackupStatus {
	items := make([]v1alpha1.DatabaseBackupItem, 0, len(backups))
	var completed []metav1.Time
	for _, backup := range backups {
		item := backupItemOf(backup)
		phase := item.Phase
		if phase == backupPhaseCompleted && item.CompletedAt != nil {
			completed = append(completed, *item.CompletedAt)
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool { return newer(items[i], items[j]) })
	if len(items) > maxBackupItems {
		items = items[:maxBackupItems]
	}
	status := &v1alpha1.DatabaseBackupStatus{Items: items, SizeBytes: sizeBytes, SizeSampledAt: sampledAt}
	if len(completed) > 0 {
		sort.Slice(completed, func(i, j int) bool { return completed[i].Before(&completed[j]) })
		first, last := completed[0], completed[len(completed)-1]
		status.PITRFrom, status.LastAt = &first, &last
		status.PITRTo = &metav1.Time{Time: now.UTC()}
	}
	return status
}

func backupItemOf(backup *unstructured.Unstructured) v1alpha1.DatabaseBackupItem {
	if backup.GetKind() != mysql.BackupGVK.Kind {
		phase, _, _ := unstructured.NestedString(backup.Object, "status", "phase")
		return v1alpha1.DatabaseBackupItem{ID: backup.GetName(), Phase: phase, StartedAt: timeField(backup, "startedAt"), CompletedAt: timeField(backup, "stoppedAt")}
	}
	completed, failed := mysql.BackupState(backup)
	created := backup.GetCreationTimestamp()
	item := v1alpha1.DatabaseBackupItem{ID: backup.GetName(), Phase: "running", StartedAt: &created}
	switch {
	case failed:
		item.Phase = "failed"
	case completed != nil:
		item.Phase, item.CompletedAt = backupPhaseCompleted, &metav1.Time{Time: completed.UTC()}
	}
	return item
}

func newer(a, b v1alpha1.DatabaseBackupItem) bool {
	switch {
	case a.StartedAt == nil || b.StartedAt == nil:
		return a.StartedAt != nil && b.StartedAt == nil || a.StartedAt == nil && b.StartedAt == nil && a.ID < b.ID
	case a.StartedAt.Equal(b.StartedAt):
		return a.ID < b.ID
	}
	return a.StartedAt.After(b.StartedAt.Time)
}

func SampleBackupSize(ctx context.Context, c client.Reader, store BackupStore, cfg Config, db v1alpha1.Database, now time.Time) (int64, *metav1.Time, error) {
	previous := db.Status.Backup
	if !SizeSampleDue(previous, now) {
		return previous.SizeBytes, previous.SizeSampledAt, nil
	}
	keys, err := BackupCredentials(ctx, c)
	if err != nil {
		return 0, nil, err
	}
	usage, err := store.S3.UsageOfPrefix(ctx, store.Endpoint, store.Region, keys, cfg.BackupBucket, Name(db.UID)+"/")
	if err != nil {
		return keptSize(previous), keptTime(previous), err
	}
	return usage.Bytes, &metav1.Time{Time: now.UTC()}, nil
}

func keptSize(previous *v1alpha1.DatabaseBackupStatus) int64 {
	if previous == nil {
		return 0
	}
	return previous.SizeBytes
}

func keptTime(previous *v1alpha1.DatabaseBackupStatus) *metav1.Time {
	if previous == nil {
		return nil
	}
	return previous.SizeSampledAt
}
