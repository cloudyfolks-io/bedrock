package database

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

type RestoreCheck struct {
	Target             v1alpha1.Database
	Source             *v1alpha1.Database
	TombstoneNamespace string
	PrefixObjects      int
	Now                time.Time
}

func RestoreSpecOf(db v1alpha1.Database) *engine.RestoreSpec {
	if db.Spec.Restore == nil {
		return nil
	}
	spec := &engine.RestoreSpec{SourceUID: db.Spec.Restore.SourceUID}
	if db.Spec.Restore.TargetTime != nil {
		at := db.Spec.Restore.TargetTime.UTC()
		spec.TargetTime = &at
	}
	return spec
}

func CheckRestore(in RestoreCheck) *Refusal {
	restore := in.Target.Spec.Restore
	if restore == nil {
		return nil
	}
	if restore.TargetTime != nil && restore.TargetTime.Time.After(in.Now) {
		return &Refusal{Reason: ReasonRestoreTargetInFuture, Message: "the restore target time is in the future"}
	}
	if refusal := checkLiveSource(in); refusal != nil {
		return refusal
	}
	if in.Source == nil && in.TombstoneNamespace != "" && in.TombstoneNamespace != in.Target.Namespace {
		return &Refusal{Reason: ReasonRestoreSourceMismatch, Message: "the source database belonged to another project"}
	}
	if in.PrefixObjects == 0 {
		return &Refusal{Reason: ReasonRestoreSourceEmpty, Message: "the backup prefix " + restore.SourceUID + "/ holds no object"}
	}
	return nil
}

func checkLiveSource(in RestoreCheck) *Refusal {
	source, target := in.Source, in.Target
	switch {
	case source == nil:
		return nil
	case source.Namespace != target.Namespace:
		return &Refusal{Reason: ReasonRestoreSourceMismatch, Message: "the source database belongs to another project"}
	case source.Spec.Engine != target.Spec.Engine:
		return &Refusal{Reason: ReasonRestoreSourceMismatch, Message: "the source database has the engine " + source.Spec.Engine}
	case source.Spec.Version != target.Spec.Version:
		return &Refusal{Reason: ReasonRestoreSourceMismatch, Message: "the source database has the version " + source.Spec.Version}
	case target.Spec.StorageGi < source.Spec.StorageGi:
		return &Refusal{Reason: ReasonRestoreSourceMismatch, Message: "the storage of the source database is larger than storageGi"}
	}
	return nil
}

func LoadRestoreCheck(ctx context.Context, c client.Reader, store BackupStore, cfg Config, db v1alpha1.Database, now time.Time) (RestoreCheck, error) {
	check := RestoreCheck{Target: db, Now: now}
	if db.Spec.Restore == nil {
		return check, nil
	}
	sourceUID := db.Spec.Restore.SourceUID
	var databases v1alpha1.DatabaseList
	if err := c.List(ctx, &databases); err != nil {
		return check, err
	}
	for i := range databases.Items {
		if string(databases.Items[i].UID) == sourceUID {
			check.Source = &databases.Items[i]
		}
	}
	namespace, found, err := TombstoneNamespace(ctx, c, sourceUID)
	if err != nil {
		return check, err
	}
	if found {
		check.TombstoneNamespace = namespace
	}
	keys, err := BackupCredentials(ctx, c)
	if apierrors.IsNotFound(err) {
		return check, nil
	}
	if err != nil {
		return check, err
	}
	page, err := store.S3.ListObjectsV2(ctx, store.Endpoint, store.Region, keys, cfg.BackupBucket, sourceUID+"/", "")
	if err != nil {
		return check, err
	}
	check.PrefixObjects = len(page.Objects)
	return check, nil
}
