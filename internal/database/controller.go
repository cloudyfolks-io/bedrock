package database

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	gateRequeue   = 30 * time.Second
	deleteRequeue = 5 * time.Second
)

type Gate struct{ Reason, Message string }

type Reconciler struct {
	Client client.Client
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var db v1alpha1.Database
	if err := r.Client.Get(ctx, req.NamespacedName, &db); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !db.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, db)
	}
	cfg, err := LoadConfig(ctx, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	project, inProject, err := ProjectOf(ctx, r.Client, db.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	gate, err := r.gate(ctx, cfg, db, inProject)
	if err != nil {
		return ctrl.Result{}, err
	}
	if gate != nil {
		return ctrl.Result{RequeueAfter: gateRequeue}, r.writeGate(ctx, db, *gate)
	}
	if err := EnsureFinalizer(ctx, r.Client, &db); err != nil {
		return ctrl.Result{}, err
	}
	_ = project
	return ctrl.Result{}, nil
}

func (r *Reconciler) gate(ctx context.Context, cfg Config, db v1alpha1.Database, inProject bool) (*Gate, error) {
	switch {
	case !inProject:
		return &Gate{Reason: ReasonNotInProject, Message: "the namespace is not a project namespace"}, nil
	case !cfg.Enabled:
		return &Gate{Reason: ReasonDatabaseDisabled, Message: "the Setting " + keyEnabled + " is false"}, nil
	}
	available, err := engineAvailable(ctx, r.Client, db.Spec.Engine)
	if err != nil {
		return nil, err
	}
	if !available {
		return &Gate{Reason: ReasonEngineNotAvailable, Message: "the engine " + db.Spec.Engine + " has no running operator addon"}, nil
	}
	refusal, err := engineRulesRefusal(ctx, r.Client, db)
	if err != nil || refusal == nil {
		return nil, err
	}
	return &Gate{Reason: refusal.Reason, Message: refusal.Message}, nil
}

func (r *Reconciler) finalize(ctx context.Context, db v1alpha1.Database) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(&db, Finalizer) {
		return ctrl.Result{}, nil
	}
	gone, err := DeleteEngineObjects(ctx, r.Client, db)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("delete the engine objects: %w", err)
	}
	if !gone {
		return ctrl.Result{RequeueAfter: deleteRequeue}, nil
	}
	return ctrl.Result{}, ReleaseFinalizer(ctx, r.Client, &db)
}

func (r *Reconciler) writeGate(ctx context.Context, db v1alpha1.Database, gate Gate) error {
	return r.writeStatus(ctx, db, func(status *v1alpha1.DatabaseStatus) {
		v1alpha1.SetCondition(&status.Conditions, metav1.Condition{
			Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: gate.Reason, Message: gate.Message, ObservedGeneration: db.Generation,
		})
	})
}

func (r *Reconciler) writeStatus(ctx context.Context, db v1alpha1.Database, change func(*v1alpha1.DatabaseStatus)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current v1alpha1.Database
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&db), &current); err != nil {
			return client.IgnoreNotFound(err)
		}
		desired := current.DeepCopy()
		change(&desired.Status)
		desired.Status.ObservedGeneration = current.Generation
		if equality.Semantic.DeepEqual(current.Status, desired.Status) {
			return nil
		}
		return r.Client.Status().Update(ctx, desired, client.FieldOwner(v1alpha1.DatabaseFieldManager))
	})
}
