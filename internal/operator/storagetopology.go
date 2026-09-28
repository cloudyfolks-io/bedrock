package operator

import (
	"context"
	"strconv"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	durableReplicaCount         = 3
	replicasRatchetedAnnotation = "bedrock.cloudyfolks.io/storage-replicas-ratcheted"
)

type StorageTopologyReconciler struct {
	Client client.Client
}

func ratchetReplicas(current string, osdHostCount int, ratcheted bool) (string, bool) {
	if ratcheted || osdHostCount < durableReplicaCount {
		return "", false
	}
	if n, err := strconv.Atoi(current); err == nil && n >= durableReplicaCount {
		return current, true
	}
	return strconv.Itoa(durableReplicaCount), true
}

func (r *StorageTopologyReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	var hosts v1alpha1.HostList
	if err := r.Client.List(ctx, &hosts); err != nil {
		return ctrl.Result{}, err
	}
	var setting v1alpha1.Setting
	if err := r.Client.Get(ctx, client.ObjectKey{Name: storageReplicasKey}, &setting); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	_, ratcheted := setting.Annotations[replicasRatchetedAnnotation]
	value, write := ratchetReplicas(setting.Spec.Value, len(osdHosts(hosts.Items)), ratcheted)
	if !write {
		return ctrl.Result{}, nil
	}
	annotations := map[string]string{}
	for key, existing := range setting.Annotations {
		annotations[key] = existing
	}
	annotations[replicasRatchetedAnnotation] = value
	setting.Annotations = annotations
	setting.Spec.Value = value
	return ctrl.Result{}, r.Client.Update(ctx, &setting)
}

func (r *StorageTopologyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	toReplicas := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
		if obj.GetName() != storageReplicasKey {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: storageReplicasKey}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("storagetopology").
		For(&v1alpha1.Host{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.Setting{}, toReplicas).
		Complete(r)
}
