package database

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func CheckEngineRules(spec v1alpha1.DatabaseSpec, class v1alpha1.DatabaseClassSpec) *Refusal {
	switch spec.Engine {
	case v1alpha1.DatabaseEngineValkey:
		return checkValkeyRules(spec, class)
	case v1alpha1.DatabaseEngineMySQL:
		return checkMySQLRules(spec)
	}
	return nil
}

func checkValkeyRules(spec v1alpha1.DatabaseSpec, class v1alpha1.DatabaseClassSpec) *Refusal {
	switch {
	case class.HighAvailability:
		return &Refusal{Reason: ReasonInstancesUnsupported, Message: "Valkey runs as one instance: choose a class without high availability"}
	case spec.Backup.Enabled:
		return &Refusal{Reason: ReasonBackupUnsupported, Message: "a Valkey database has no backups: set backup.enabled to false"}
	case spec.Restore != nil:
		return &Refusal{Reason: ReasonRestoreUnsupported, Message: "a Valkey database has no restore"}
	}
	return nil
}

func checkMySQLRules(spec v1alpha1.DatabaseSpec) *Refusal {
	if spec.Restore != nil && spec.Restore.TargetTime != nil {
		return &Refusal{Reason: ReasonRestoreTimeUnsupported, Message: "a MySQL restore has no target time: remove restore.targetTime"}
	}
	return nil
}

func SchemaRefusal(engine string) *Refusal {
	if engine == v1alpha1.DatabaseEngineValkey {
		return &Refusal{Reason: ReasonUnsupportedForEngine, Message: "a Valkey database has one database named app and no schema objects"}
	}
	return nil
}

func engineRulesRefusal(ctx context.Context, c client.Reader, db v1alpha1.Database) (*Refusal, error) {
	var class v1alpha1.DatabaseClass
	err := c.Get(ctx, client.ObjectKey{Name: db.Spec.Class}, &class)
	if apierrors.IsNotFound(err) {
		return CheckEngineRules(db.Spec, v1alpha1.DatabaseClassSpec{}), nil
	}
	if err != nil {
		return nil, err
	}
	return CheckEngineRules(db.Spec, class.Spec), nil
}
