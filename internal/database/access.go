package database

import (
	"context"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/exec"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
)

const (
	ConditionEngineReady = "EngineReady"

	finalizerUser   = "bedrock.cloudyfolks.io/database-user"
	finalizerSchema = "bedrock.cloudyfolks.io/database-schema"

	accessPending = 10 * time.Second
	accessBackoff = 30 * time.Second
)

type Engine struct {
	Kind    string
	Run     Run
	Timeout time.Duration
}

type EngineFactory func(ctx context.Context, db v1alpha1.Database) (Engine, error)

func NewEngineFactory(reader client.Reader, base exec.Runner) EngineFactory {
	return func(ctx context.Context, db v1alpha1.Database) (Engine, error) {
		cfg, err := LoadConfig(ctx, reader)
		if err != nil {
			return Engine{}, err
		}
		runner := base
		runner.Timeout = cfg.ExecTimeout
		target := TargetFor(db.Spec.Engine, Name(db.UID))
		return Engine{
			Kind:    db.Spec.Engine,
			Timeout: cfg.ExecTimeout,
			Run: func(ctx context.Context, script string) (string, error) {
				return runner.Run(ctx, Name(db.UID), target, script)
			},
		}, nil
	}
}

type accessOutcome struct {
	reason  string
	message string
	requeue time.Duration
}

func (o accessOutcome) ready() bool { return o.reason == ReasonReady }

func readyOutcome() accessOutcome {
	return accessOutcome{reason: ReasonReady, message: "the engine matches the object", requeue: readyRequeue}
}

func resolveDatabase(ctx context.Context, c client.Reader, namespace, name string) (v1alpha1.Database, *accessOutcome, error) {
	var db v1alpha1.Database
	err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &db)
	switch {
	case apierrors.IsNotFound(err):
		return db, &accessOutcome{reason: ReasonDatabaseNotFound, message: "the database " + name + " does not exist", requeue: accessPending}, nil
	case err != nil:
		return db, nil, err
	case !db.DeletionTimestamp.IsZero():
		return db, &accessOutcome{reason: ReasonDatabaseNotReady, message: "the database " + name + " is being deleted", requeue: accessBackoff}, nil
	case !meta.IsStatusConditionTrue(db.Status.Conditions, ConditionEngineReady):
		return db, &accessOutcome{reason: ReasonDatabaseNotReady, message: "the engine of the database " + name + " is not ready", requeue: accessPending}, nil
	}
	return db, nil, nil
}

func engineFailure(err error) *accessOutcome {
	var rejected *exec.CommandError
	switch {
	case errors.As(err, &rejected), errors.Is(err, engine.ErrUnavailable):
		return &accessOutcome{reason: ReasonEngineRejected, message: rejected.Error(), requeue: accessBackoff}
	case errors.Is(err, exec.ErrNotReady), errors.Is(err, exec.ErrUnavailable), errors.Is(err, exec.ErrBusy), errors.Is(err, exec.ErrOutputTooLarge):
		return &accessOutcome{reason: ReasonEngineUnavailable, message: err.Error(), requeue: accessPending}
	}
	return nil
}

func listDatabases(ctx context.Context, access Engine) ([]string, error) {
	output, err := access.Run(ctx, postgres.ListDatabasesScript(access.Timeout))
	if err != nil {
		return nil, err
	}
	return postgres.ParseDatabases(output), nil
}

func updateStatus[T client.Object](ctx context.Context, c client.Client, key client.ObjectKey, fresh func() T, mutate func(T)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := fresh()
		if err := c.Get(ctx, key, current); err != nil {
			return client.IgnoreNotFound(err)
		}
		desired := current.DeepCopyObject().(T)
		mutate(desired)
		if statusEqual(current, desired) {
			return nil
		}
		return c.Status().Update(ctx, desired, client.FieldOwner(v1alpha1.DatabaseFieldManager))
	})
}

func readyCondition(outcome accessOutcome, generation int64) metav1.Condition {
	status := metav1.ConditionFalse
	if outcome.ready() {
		status = metav1.ConditionTrue
	}
	return metav1.Condition{Type: v1alpha1.ConditionReady, Status: status, Reason: outcome.reason, Message: outcome.message, ObservedGeneration: generation}
}

func result(outcome accessOutcome) ctrl.Result {
	return ctrl.Result{RequeueAfter: outcome.requeue}
}
