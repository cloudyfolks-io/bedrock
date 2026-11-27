package database

import (
	"context"
	"time"

	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/mysql"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
	"github.com/cloudyfolks-io/bedrock/internal/database/valkey"
)

const annotationBootstrapped = "bedrock.cloudyfolks.io/bootstrapped"

type Run func(ctx context.Context, script string) (string, error)

func (run Run) Run(ctx context.Context, script string) (string, error) { return run(ctx, script) }

func Bootstrapped(db v1alpha1.Database) bool {
	return db.Annotations[annotationBootstrapped] == synced
}

func EnsureBootstrapped(ctx context.Context, c client.Client, run Run, timeout time.Duration, db *v1alpha1.Database) error {
	if Bootstrapped(*db) {
		return nil
	}
	if err := bootstrapEngine(ctx, c, run, timeout, *db); err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(db), db); err != nil {
			return err
		}
		before := db.DeepCopy()
		if db.Annotations == nil {
			db.Annotations = map[string]string{}
		}
		db.Annotations[annotationBootstrapped] = synced
		return c.Patch(ctx, db, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.DatabaseFieldManager))
	})
}

func SyncAdmin(ctx context.Context, c client.Client, run Run, timeout time.Duration, db v1alpha1.Database) error {
	state, err := ReadAdmin(ctx, c, db)
	if err != nil {
		return err
	}
	if token := db.Annotations[v1alpha1.AnnotationResetPassword]; token != "" && token != state.Applied {
		if state.Password, err = ResetAdminPassword(ctx, c, db, token); err != nil {
			return err
		}
		state.Synced = false
	}
	if state.Synced {
		return nil
	}
	if err := resetAdminOnEngine(ctx, run, timeout, db, state.Password); err != nil {
		return err
	}
	return MarkAdminSynced(ctx, c, db)
}

func bootstrapEngine(ctx context.Context, c client.Client, run Run, timeout time.Duration, db v1alpha1.Database) error {
	switch db.Spec.Engine {
	case v1alpha1.DatabaseEngineValkey:
		return nil
	case v1alpha1.DatabaseEngineMySQL:
		state, err := ReadAdmin(ctx, c, db)
		if err != nil {
			return err
		}
		return mysql.Bootstrap(ctx, run, state.Password)
	}
	script, err := postgres.BootstrapScript(timeout)
	if err != nil {
		return err
	}
	_, err = run(ctx, script)
	return err
}

func resetAdminOnEngine(ctx context.Context, run Run, timeout time.Duration, db v1alpha1.Database, password string) error {
	switch db.Spec.Engine {
	case v1alpha1.DatabaseEngineValkey:
		return valkey.ResetAdmin(ctx, run, password)
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.ResetAdmin(ctx, run, password)
	}
	script, err := postgres.ResetAdminScript(password, timeout)
	if err != nil {
		return err
	}
	_, err = run(ctx, script)
	return err
}
