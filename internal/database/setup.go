package database

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func SetupWithManager(mgr ctrl.Manager) error {
	platform := &PlatformReconciler{Client: mgr.GetClient()}
	if err := platform.SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.Add(&Sweeper{Client: mgr.GetClient()}); err != nil {
		return err
	}
	return (&Reconciler{Client: mgr.GetClient()}).SetupWithManager(mgr)
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
