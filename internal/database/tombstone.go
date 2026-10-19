package database

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	AnnotationDeleteAfter = "bedrock.cloudyfolks.io/delete-after"
	tombstoneKind         = "Tombstone"
	tombstonePrefix       = "tombstone-"
)

func TombstoneName(uid types.UID) string { return tombstonePrefix + string(uid) }

func TombstoneObject(db v1alpha1.Database, project Project, deleteAfter time.Time) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: TombstoneName(db.UID), Namespace: PlatformNamespace,
			Labels:      v1alpha1.TenantLabels(tombstoneKind, db.Name, project.Tenant, project.Name),
			Annotations: map[string]string{AnnotationDeleteAfter: deleteAfter.UTC().Format(time.RFC3339)},
		},
		Data: map[string]string{"namespace": db.Namespace, "name": db.Name},
	}
}

func EnsureTombstone(ctx context.Context, c client.Client, db v1alpha1.Database, project Project, retention time.Duration, now time.Time) error {
	var existing corev1.ConfigMap
	err := c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: TombstoneName(db.UID)}, &existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	err = c.Create(ctx, TombstoneObject(db, project, now.Add(retention)), client.FieldOwner(v1alpha1.DatabaseFieldManager))
	return client.IgnoreNotFound(client.IgnoreAlreadyExists(err))
}

func TombstoneNamespace(ctx context.Context, c client.Reader, uid string) (string, bool, error) {
	var tombstone corev1.ConfigMap
	err := c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: TombstoneName(types.UID(uid))}, &tombstone)
	if apierrors.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return tombstone.Data["namespace"], true, nil
}

func PurgeBackups(ctx context.Context, c client.Client, store BackupStore, cfg Config, now time.Time) error {
	var tombstones corev1.ConfigMapList
	if err := c.List(ctx, &tombstones, client.InNamespace(PlatformNamespace), client.MatchingLabels{v1alpha1.LabelKind: tombstoneKind}); err != nil {
		return err
	}
	var failures []error
	for i := range tombstones.Items {
		tombstone := &tombstones.Items[i]
		if !purgeDue(tombstone, now) {
			continue
		}
		if err := purge(ctx, c, store, cfg, tombstone); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func purgeDue(tombstone *corev1.ConfigMap, now time.Time) bool {
	after, err := time.Parse(time.RFC3339, tombstone.Annotations[AnnotationDeleteAfter])
	return err == nil && !now.Before(after)
}

func purge(ctx context.Context, c client.Client, store BackupStore, cfg Config, tombstone *corev1.ConfigMap) error {
	keys, err := BackupCredentials(ctx, c)
	if err != nil {
		return err
	}
	prefix := tombstone.Name[len(tombstonePrefix):] + "/"
	if err := store.S3.DeletePrefix(ctx, store.Endpoint, store.Region, keys, cfg.BackupBucket, prefix); err != nil {
		return err
	}
	return client.IgnoreNotFound(c.Delete(ctx, tombstone))
}
