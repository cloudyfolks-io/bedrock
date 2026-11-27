package database

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

var errChangedPostgresRestore = errors.New("the existing PostgreSQL bootstrap differs from the UID-bound backup selection")

func (r *Reconciler) checkExistingPostgresRestore(ctx context.Context, in *flow) error {
	if in.db.Spec.Engine != v1alpha1.DatabaseEnginePostgreSQL {
		return nil
	}
	selected := in.source != nil && in.source.Postgres != nil
	if in.cluster == nil {
		if selected && in.db.Annotations[annotationReadyOnce] == "true" {
			return errChangedPostgresRestore
		}
		return nil
	}
	_, hasID, err := unstructured.NestedString(in.cluster.Object, "spec", "bootstrap", "recovery", "recoveryTarget", "backupID")
	if err != nil {
		return errChangedPostgresRestore
	}
	if !selected && !hasID {
		return nil
	}
	if !selected || !hasID || in.cluster.GetDeletionTimestamp() != nil || platform.OwnerOf(in.cluster).UID != in.db.UID {
		return errChangedPostgresRestore
	}
	name := Name(in.db.UID)
	store := &unstructured.Unstructured{}
	store.SetGroupVersionKind(ObjectStoreGVK)
	if err := r.Live.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: engine.Named(name, "source")}, store); err != nil {
		if apierrors.IsNotFound(err) {
			return errChangedPostgresRestore
		}
		return err
	}
	restore := RestoreSpecOf(*in.db)
	restore.Backup = in.source
	s := engine.Spec{Name: name, Restore: restore, Backup: engine.BackupSpec{Endpoint: r.Store.Endpoint}}
	if store.GetDeletionTimestamp() != nil || platform.OwnerOf(store).UID != in.db.UID || !postgres.MatchesSelectedRestore(s, in.cluster, store) {
		return errChangedPostgresRestore
	}
	return nil
}

func (r *Reconciler) applySelectedPostgresStore(ctx context.Context, in *flow, desired *unstructured.Unstructured) error {
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(ObjectStoreGVK)
	err := r.Live.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) && in.cluster == nil {
		return applyObservedEngineObject(ctx, r.Client, desired, nil)
	}
	if err != nil {
		return err
	}
	if current.GetDeletionTimestamp() != nil || platform.OwnerOf(current).UID != in.db.UID || !postgres.MatchesSelectedStore(desired, current) {
		return errChangedPostgresRestore
	}
	return applyObservedEngineObject(ctx, r.Client, desired, current)
}

func applyObservedEngineObject(ctx context.Context, c client.Client, desired, current *unstructured.Unstructured) error {
	obj := desired.DeepCopy()
	if current == nil {
		return c.Create(ctx, obj, client.FieldOwner(v1alpha1.DatabaseFieldManager))
	}
	if current.GetResourceVersion() == "" {
		return errors.New("the observed engine object has no resource version")
	}
	obj.SetResourceVersion(current.GetResourceVersion())
	return ssa.Apply(ctx, c, obj, v1alpha1.DatabaseFieldManager)
}
