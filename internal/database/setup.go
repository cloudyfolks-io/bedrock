package database

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/exec"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

func SetupWithManager(mgr ctrl.Manager) error {
	platform := &PlatformReconciler{Client: mgr.GetClient()}
	if err := platform.SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.Add(&Sweeper{Client: mgr.GetClient(), Store: BackupStore{S3: &s3.Client{}, Endpoint: objectstorage.Endpoint, Region: objectstorage.Region}}); err != nil {
		return err
	}
	engine := NewEngineFactory(mgr.GetAPIReader(), exec.Runner{Pods: mgr.GetAPIReader(), Executor: exec.KubeExecutor{Config: mgr.GetConfig(), OutputLimit: outputLimit}, Locks: &exec.Locks{}})
	if err := (&UserReconciler{Client: mgr.GetClient(), Engine: engine}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&SchemaReconciler{Client: mgr.GetClient(), Engine: engine}).SetupWithManager(mgr); err != nil {
		return err
	}
	return (&Reconciler{Client: mgr.GetClient()}).SetupWithManager(mgr)
}

const outputLimit = 1 << 20

func (r *UserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("database-user").
		For(&v1alpha1.DatabaseUser{}).
		Owns(&corev1.Secret{}).
		Watches(&v1alpha1.Database{}, handler.EnqueueRequestsFromMapFunc(r.usersOfDatabase)).
		Watches(&v1alpha1.DatabaseSchema{}, handler.EnqueueRequestsFromMapFunc(r.usersOfSchema)).
		Complete(r)
}

func (r *UserReconciler) usersOfDatabase(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.usersReferencing(ctx, obj.GetNamespace(), obj.GetName())
}

func (r *UserReconciler) usersOfSchema(ctx context.Context, obj client.Object) []reconcile.Request {
	schema, ok := obj.(*v1alpha1.DatabaseSchema)
	if !ok {
		return nil
	}
	return r.usersReferencing(ctx, schema.Namespace, schema.Spec.DatabaseRef)
}

func (r *UserReconciler) usersReferencing(ctx context.Context, namespace, database string) []reconcile.Request {
	var users v1alpha1.DatabaseUserList
	if err := r.Client.List(ctx, &users, client.InNamespace(namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range users.Items {
		if users.Items[i].Spec.DatabaseRef == database {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&users.Items[i])})
		}
	}
	return requests
}

func (r *SchemaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("database-schema").
		For(&v1alpha1.DatabaseSchema{}).
		Watches(&v1alpha1.Database{}, handler.EnqueueRequestsFromMapFunc(r.schemasOfDatabase)).
		Complete(r)
}

func (r *SchemaReconciler) schemasOfDatabase(ctx context.Context, obj client.Object) []reconcile.Request {
	var schemas v1alpha1.DatabaseSchemaList
	if err := r.Client.List(ctx, &schemas, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range schemas.Items {
		if schemas.Items[i].Spec.DatabaseRef == obj.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&schemas.Items[i])})
		}
	}
	return requests
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("database").
		For(&v1alpha1.Database{}).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(r.allDatabases), builder.WithPredicates(predicate.NewPredicateFuncs(databaseSetting))).
		Watches(&v1alpha1.Addon{}, handler.EnqueueRequestsFromMapFunc(r.allDatabases), builder.WithPredicates(predicate.NewPredicateFuncs(engineAddon))).
		Complete(r)
}

func databaseSetting(obj client.Object) bool {
	return strings.HasPrefix(obj.GetName(), settingPrefix)
}

func engineAddon(obj client.Object) bool {
	return obj.GetName() == addonName
}

func (r *Reconciler) allDatabases(ctx context.Context, _ client.Object) []reconcile.Request {
	var list v1alpha1.DatabaseList
	if err := r.Client.List(ctx, &list); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return requests
}

var platformRequest = []reconcile.Request{{NamespacedName: types.NamespacedName{Name: PlatformNamespace}}}

type PlatformReconciler struct {
	Client client.Client
}

func (r *PlatformReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	cfg, err := LoadConfig(ctx, r.Client)
	if err != nil || !cfg.Enabled {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, EnsurePlatform(ctx, r.Client, cfg)
}

func (r *PlatformReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("database-platform").
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request { return platformRequest }), builder.WithPredicates(predicate.NewPredicateFuncs(databaseSetting))).
		Complete(r)
}
