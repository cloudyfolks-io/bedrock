package clusters

import (
	"context"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/addons"
)

func SetupWithManager(mgr ctrl.Manager) error {
	r := &Reconciler{Client: mgr.GetClient(), Secrets: mgr.GetAPIReader(), Reader: mgr.GetAPIReader(), FabricIPs: mgr.GetAPIReader(), Scheme: mgr.GetScheme(), Now: time.Now}
	return r.SetupWithManager(mgr)
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	clusterObject := &unstructured.Unstructured{}
	clusterObject.SetGroupVersionKind(clusterGVK)
	return ctrl.NewControllerManagedBy(mgr).
		Named("kubernetescluster").
		For(&v1alpha1.KubernetesCluster{}).
		Owns(&v1alpha1.VirtualMachine{}).
		Owns(clusterObject).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(clusterOfPod)).
		Watches(&v1alpha1.Subnet{}, handler.EnqueueRequestsFromMapFunc(r.clustersOf(usesSubnet))).
		Watches(&v1alpha1.SecurityGroup{}, handler.EnqueueRequestsFromMapFunc(r.clustersOf(usesGroup))).
		Watches(&v1alpha1.Addon{}, handler.EnqueueRequestsFromMapFunc(r.allClusters), builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			return obj.GetName() == addons.Clusters
		}))).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(r.allClusters), builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			return obj.GetName() == VersionsSetting
		}))).
		Complete(r)
}

func clusterOfPod(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	owner := metav1.GetControllerOf(obj)
	if labels["app"] != "k0smotron" || labels["cluster"] == "" || owner == nil || owner.Kind != "StatefulSet" || owner.Name != StatefulSetName(labels["cluster"]) {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: labels["cluster"]}}}
}

type clusterFilter func(cluster v1alpha1.KubernetesCluster, name string) bool

func usesSubnet(cluster v1alpha1.KubernetesCluster, name string) bool {
	return SubnetName(cluster.Spec) == name
}

func usesGroup(cluster v1alpha1.KubernetesCluster, name string) bool {
	return slices.Contains(GroupNames(cluster.Spec), name)
}

func (r *Reconciler) clustersOf(uses clusterFilter) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.KubernetesClusterList
		if err := r.Client.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for index := range list.Items {
			if uses(list.Items[index], obj.GetName()) {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[index])})
			}
		}
		return requests
	}
}

func (r *Reconciler) allClusters(ctx context.Context, _ client.Object) []reconcile.Request {
	var list v1alpha1.KubernetesClusterList
	if err := r.Client.List(ctx, &list); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for index := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[index])})
	}
	return requests
}
