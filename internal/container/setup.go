package container

import (
	"context"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/kinds"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

var requiredKinds = []schema.GroupVersionKind{fabric.IPGVK}

func SetupWithManager(mgr ctrl.Manager) error {
	return kinds.SetupWhenServed(mgr, "container", requiredKinds, setupControllers)
}

func setupControllers(mgr ctrl.Manager) error {
	r := &Reconciler{Client: mgr.GetClient(), Secrets: mgr.GetAPIReader(), FabricIPs: mgr.GetCache(), Scheme: mgr.GetScheme(), Now: time.Now}
	if err := r.SetupWithManager(mgr); err != nil {
		return err
	}
	return SetupBuildsWithManager(mgr)
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("container").
		For(&v1alpha1.Container{}).
		Owns(&appsv1.Deployment{}).
		Owns(&autoscalingv2.HorizontalPodAutoscaler{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(containerOfPod)).
		Watches(&v1alpha1.Subnet{}, handler.EnqueueRequestsFromMapFunc(r.containersOf(usesSubnet))).
		Watches(&v1alpha1.SecurityGroup{}, handler.EnqueueRequestsFromMapFunc(r.containersOf(usesGroup))).
		Watches(&v1alpha1.Volume{}, handler.EnqueueRequestsFromMapFunc(r.containersOf(usesVolume))).
		Watches(&nodev1.RuntimeClass{}, handler.EnqueueRequestsFromMapFunc(r.allContainers)).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(r.allContainers), builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			return obj.GetName() == RuntimeSetting
		}))).
		Complete(r)
}

func containerOfPod(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	if labels[v1alpha1.LabelKind] != Kind || labels[v1alpha1.LabelName] == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: labels[v1alpha1.LabelName]}}}
}

type containerFilter func(c v1alpha1.Container, name string) bool

func usesSubnet(c v1alpha1.Container, name string) bool { return SubnetName(c.Spec) == name }

func usesGroup(c v1alpha1.Container, name string) bool {
	return slices.Contains(c.Spec.Network.SecurityGroups, name)
}

func usesVolume(c v1alpha1.Container, name string) bool {
	return slices.ContainsFunc(c.Spec.Volumes, func(v v1alpha1.ContainerVolume) bool { return v.VolumeRef == name })
}

func (r *Reconciler) containersOf(uses containerFilter) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list v1alpha1.ContainerList
		if err := r.Client.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for _, c := range list.Items {
			if uses(c, obj.GetName()) {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&c)})
			}
		}
		return requests
	}
}

func (r *Reconciler) allContainers(ctx context.Context, _ client.Object) []reconcile.Request {
	var list v1alpha1.ContainerList
	if err := r.Client.List(ctx, &list); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for _, c := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&c)})
	}
	return requests
}
