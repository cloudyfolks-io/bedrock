package database

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	gateRequeue     = 30 * time.Second
	deleteRequeue   = 5 * time.Second
	progressRequeue = 10 * time.Second
	readyRequeue    = time.Minute
	addonName       = "cnpg"

	ConditionGateReady     = "GateReady"
	ConditionEndpointReady = "EndpointReady"
	ConditionBackupHealthy = "BackupHealthy"
)

type Gate struct{ Reason, Message string }

type Reconciler struct {
	Client   client.Client
	Live     client.Reader
	Images   []string
	Store    BackupStore
	Alloc    *Allocator
	Engine   EngineFactory
	Recorder record.EventRecorder
	Now      func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
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
		observed := Observed{Refusal: &Refusal{Reason: gate.Reason, Message: gate.Message}}
		_, err := r.publish(ctx, cfg, &db, observed)
		return ctrl.Result{RequeueAfter: gateRequeue}, err
	}
	if err := EnsureFinalizer(ctx, r.Client, &db); err != nil {
		return ctrl.Result{}, err
	}
	observed, err := r.converge(ctx, cfg, project, &db)
	if err != nil {
		return ctrl.Result{}, err
	}
	verdict, err := r.publish(ctx, cfg, &db, observed)
	return ctrl.Result{RequeueAfter: requeueFor(verdict)}, err
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
	return nil, nil
}

func engineAvailable(ctx context.Context, c client.Reader, engine string) (bool, error) {
	if engine != v1alpha1.DatabaseEnginePostgreSQL {
		return false, nil
	}
	var addon v1alpha1.Addon
	err := c.Get(ctx, client.ObjectKey{Name: addonName}, &addon)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil && addon.Spec.Enabled, err
}

func (r *Reconciler) finalize(ctx context.Context, db v1alpha1.Database) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(&db, Finalizer) {
		return ctrl.Result{}, nil
	}
	cfg, err := LoadConfig(ctx, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	project, _, err := ProjectOf(ctx, r.Client, db.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := EnsureTombstone(ctx, r.Client, db, project, cfg.RetentionAfterDelete, r.now()); err != nil {
		return ctrl.Result{}, fmt.Errorf("leave the tombstone: %w", err)
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

func (r *Reconciler) publish(ctx context.Context, cfg Config, db *v1alpha1.Database, observed Observed) (Verdict, error) {
	now := r.now()
	verdict := Phase(Observation{
		Suspended:        db.Spec.Suspended,
		Restoring:        db.Spec.Restore != nil,
		Bootstrapped:     observed.Bootstrapped,
		ReadyBefore:      db.Annotations[annotationReadyOnce] == "true",
		EngineReady:      observed.EngineReady,
		EngineReason:     observed.EngineReason,
		GateReady:        observed.GateReady,
		EndpointReady:    observed.EndpointReady,
		Refusal:          observed.Refusal,
		Age:              now.Sub(db.CreationTimestamp.Time),
		ProvisionTimeout: cfg.ProvisionTimeout,
	})
	if verdict.Phase == v1alpha1.DatabasePhaseReady && db.Annotations[annotationReadyOnce] != "true" {
		if err := markReadyOnce(ctx, r.Client, db); err != nil {
			return verdict, err
		}
	}
	backup := BackupHealth(BackupObservation{Enabled: db.Spec.Backup.Enabled, Age: now.Sub(db.CreationTimestamp.Time), Now: now, Status: observed.Backup})
	previous := db.Status.Phase
	err := r.writeStatus(ctx, *db, func(status *v1alpha1.DatabaseStatus) {
		status.Phase = verdict.Phase
		status.Instances = observed.Instances
		status.Backup = observed.Backup
		if len(status.Addresses) > 0 {
			status.CredentialsSecret = AdminCopyName(*db)
		}
		for _, condition := range conditionsOf(observed, verdict, backup, db.Generation) {
			v1alpha1.SetCondition(&status.Conditions, condition)
		}
	})
	if err == nil && previous != verdict.Phase {
		r.emit(*db, verdict)
	}
	return verdict, err
}

func markReadyOnce(ctx context.Context, c client.Client, db *v1alpha1.Database) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(db), db); err != nil {
			return err
		}
		before := db.DeepCopy()
		if db.Annotations == nil {
			db.Annotations = map[string]string{}
		}
		db.Annotations[annotationReadyOnce] = "true"
		return c.Patch(ctx, db, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.DatabaseFieldManager))
	})
}

func conditionsOf(observed Observed, verdict Verdict, backup BackupVerdict, generation int64) []metav1.Condition {
	truth := func(ok bool) metav1.ConditionStatus {
		if ok {
			return metav1.ConditionTrue
		}
		return metav1.ConditionFalse
	}
	reasoned := func(ok bool, yes, no, message string) (string, string) {
		if ok {
			return yes, message
		}
		return no, message
	}
	engineReason, engineMessage := reasoned(observed.EngineReady, "EngineHealthy", ReasonEngineWaiting, observed.EngineReason)
	if observed.EngineReady {
		engineMessage = "the engine is healthy"
	}
	gateReason, _ := reasoned(observed.GateReady, "GateRunning", ReasonGateWaiting, "")
	endpointReason, _ := reasoned(observed.EndpointReady, "EndpointRunning", ReasonProxyWaiting, "")
	backupStatus := truth(backup.Healthy)
	if backup.Unknown {
		backupStatus = metav1.ConditionUnknown
	}
	return []metav1.Condition{
		{Type: v1alpha1.ConditionReady, Status: truth(verdict.Phase == v1alpha1.DatabasePhaseReady), Reason: verdict.Reason, Message: verdict.Message, ObservedGeneration: generation},
		{Type: ConditionEngineReady, Status: truth(observed.EngineReady), Reason: engineReason, Message: engineMessage, ObservedGeneration: generation},
		{Type: ConditionGateReady, Status: truth(observed.GateReady), Reason: gateReason, Message: "the gate deployment of the database", ObservedGeneration: generation},
		{Type: ConditionEndpointReady, Status: truth(observed.EndpointReady), Reason: endpointReason, Message: "the proxy deployment in the subnet", ObservedGeneration: generation},
		{Type: ConditionBackupHealthy, Status: backupStatus, Reason: backup.Reason, Message: backup.Message, ObservedGeneration: generation},
	}
}

func (r *Reconciler) emit(db v1alpha1.Database, verdict Verdict) {
	if r.Recorder == nil {
		return
	}
	switch verdict.Phase {
	case v1alpha1.DatabasePhaseReady:
		r.Recorder.Event(&db, corev1.EventTypeNormal, v1alpha1.DatabasePhaseReady, verdict.Message)
	case v1alpha1.DatabasePhaseUnhealthy, v1alpha1.DatabasePhaseFailed:
		r.Recorder.Event(&db, corev1.EventTypeWarning, verdict.Phase, verdict.Reason+": "+verdict.Message)
	}
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
