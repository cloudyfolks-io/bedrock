package database

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

type SchemaReconciler struct {
	Client client.Client
	Engine EngineFactory
}

func SchemaNameOf(schema v1alpha1.DatabaseSchema) string {
	if schema.Spec.Name != "" {
		return schema.Spec.Name
	}
	return schema.Name
}

func validateSchema(schema v1alpha1.DatabaseSchema) string {
	name := SchemaNameOf(schema)
	if !postgres.ValidIdentifier(name) {
		return "the database name " + name + " must match ^[a-z][a-z0-9_]{1,62}$; set spec.name"
	}
	return ""
}

func (r *SchemaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var schema v1alpha1.DatabaseSchema
	if err := r.Client.Get(ctx, req.NamespacedName, &schema); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !schema.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, schema)
	}
	outcome, err := r.converge(ctx, &schema)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.writeStatus(ctx, schema, outcome); err != nil {
		return ctrl.Result{}, err
	}
	return result(outcome), nil
}

func (r *SchemaReconciler) writeStatus(ctx context.Context, schema v1alpha1.DatabaseSchema, outcome accessOutcome) error {
	return updateStatus(ctx, r.Client, client.ObjectKeyFromObject(&schema), func() *v1alpha1.DatabaseSchema { return &v1alpha1.DatabaseSchema{} }, func(current *v1alpha1.DatabaseSchema) {
		v1alpha1.SetCondition(&current.Status.Conditions, readyCondition(outcome, current.Generation))
		current.Status.ObservedGeneration = current.Generation
		if outcome.ready() {
			current.Status.Name = SchemaNameOf(*current)
		}
	})
}

func (r *SchemaReconciler) converge(ctx context.Context, schema *v1alpha1.DatabaseSchema) (accessOutcome, error) {
	if problem := validateSchema(*schema); problem != "" {
		return accessOutcome{reason: ReasonInvalidSpec, message: problem, requeue: accessBackoff}, nil
	}
	db, blocked, err := resolveDatabase(ctx, r.Client, schema.Namespace, schema.Spec.DatabaseRef)
	if err != nil || blocked != nil {
		return orBlocked(blocked), err
	}
	if refusal := SchemaRefusal(db.Spec.Engine); refusal != nil {
		return accessOutcome{reason: refusal.Reason, message: refusal.Message, requeue: accessBackoff}, nil
	}
	if problem := schemaProblem(db.Spec.Engine, SchemaNameOf(*schema)); problem != "" {
		return accessOutcome{reason: ReasonInvalidSpec, message: problem, requeue: accessBackoff}, nil
	}
	if other, err := r.olderOwner(ctx, *schema); err != nil || other != "" {
		if err != nil {
			return accessOutcome{}, err
		}
		return accessOutcome{reason: ReasonNameConflict, message: "the DatabaseSchema " + other + " already owns the database " + SchemaNameOf(*schema), requeue: accessBackoff}, nil
	}
	if !controllerutil.ContainsFinalizer(schema, finalizerSchema) {
		if err := patchSchemaFinalizer(ctx, r.Client, schema, controllerutil.AddFinalizer); err != nil {
			return accessOutcome{}, err
		}
	}
	if schema.Status.Name == SchemaNameOf(*schema) {
		return readyOutcome(), nil
	}
	engineAccess, err := r.Engine(ctx, db)
	if err != nil {
		return accessOutcome{}, err
	}
	err = createDatabase(ctx, engineAccess, SchemaNameOf(*schema))
	if engine.IsExists(err) {
		err = r.adoptSchema(ctx, engineAccess, SchemaNameOf(*schema))
	}
	if err != nil {
		if failure := engineFailure(err); failure != nil {
			return *failure, nil
		}
		return accessOutcome{}, err
	}
	return readyOutcome(), r.recordName(ctx, schema)
}

func (r *SchemaReconciler) adoptSchema(ctx context.Context, engineAccess Engine, name string) error {
	if engineAccess.Kind == v1alpha1.DatabaseEngineMySQL {
		return nil
	}
	return r.adoptGroups(ctx, engineAccess, name)
}

func (r *SchemaReconciler) adoptGroups(ctx context.Context, engineAccess Engine, name string) error {
	script, err := postgres.EnsureGroupsScript(name, engineAccess.Timeout)
	if err != nil {
		return err
	}
	_, err = engineAccess.Run(ctx, script)
	return err
}

func (r *SchemaReconciler) recordName(ctx context.Context, schema *v1alpha1.DatabaseSchema) error {
	return updateStatus(ctx, r.Client, client.ObjectKeyFromObject(schema), func() *v1alpha1.DatabaseSchema { return &v1alpha1.DatabaseSchema{} }, func(current *v1alpha1.DatabaseSchema) {
		current.Status.Name = SchemaNameOf(*current)
		schema.Status.Name = current.Status.Name
	})
}

func (r *SchemaReconciler) olderOwner(ctx context.Context, schema v1alpha1.DatabaseSchema) (string, error) {
	var schemas v1alpha1.DatabaseSchemaList
	if err := r.Client.List(ctx, &schemas, client.InNamespace(schema.Namespace)); err != nil {
		return "", err
	}
	for i := range schemas.Items {
		other := &schemas.Items[i]
		if other.Name != schema.Name && other.Spec.DatabaseRef == schema.Spec.DatabaseRef && SchemaNameOf(*other) == SchemaNameOf(schema) && network.Older(other, &schema) {
			return other.Name, nil
		}
	}
	return "", nil
}

func patchSchemaFinalizer(ctx context.Context, c client.Client, schema *v1alpha1.DatabaseSchema, change func(client.Object, string) bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, client.ObjectKeyFromObject(schema), schema); err != nil {
			return client.IgnoreNotFound(err)
		}
		before := schema.DeepCopy()
		if !change(schema, finalizerSchema) {
			return nil
		}
		return c.Patch(ctx, schema, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(v1alpha1.DatabaseFieldManager))
	})
}

func (r *SchemaReconciler) finalize(ctx context.Context, schema v1alpha1.DatabaseSchema) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(&schema, finalizerSchema) {
		return ctrl.Result{}, nil
	}
	release := func() (ctrl.Result, error) {
		return ctrl.Result{}, patchSchemaFinalizer(ctx, r.Client, &schema, controllerutil.RemoveFinalizer)
	}
	if schema.Status.Name == "" || schema.Spec.DeletionPolicy != v1alpha1.DatabaseSchemaDelete {
		return release()
	}
	var db v1alpha1.Database
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: schema.Namespace, Name: schema.Spec.DatabaseRef}, &db)
	if apierrors.IsNotFound(err) || err == nil && !db.DeletionTimestamp.IsZero() {
		return release()
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if schemaProblem(db.Spec.Engine, schema.Status.Name) != "" {
		return release()
	}
	_, blocked, err := resolveDatabase(ctx, r.Client, schema.Namespace, schema.Spec.DatabaseRef)
	if err != nil {
		return ctrl.Result{}, err
	}
	if blocked != nil {
		return result(*blocked), r.writeStatus(ctx, schema, *blocked)
	}
	engineAccess, err := r.Engine(ctx, db)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := dropDatabase(ctx, engineAccess, schema.Status.Name); err != nil && !engine.IsNotFound(err) {
		if failure := engineFailure(err); failure != nil {
			return result(*failure), r.writeStatus(ctx, schema, *failure)
		}
		return ctrl.Result{}, err
	}
	return release()
}
