package container

import (
	"context"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func SetupBuildsWithManager(mgr ctrl.Manager) error {
	r := &BuildReconciler{Client: mgr.GetClient(), Secrets: mgr.GetAPIReader(), Scheme: mgr.GetScheme(), Now: time.Now}
	return r.SetupWithManager(mgr)
}

func (r *BuildReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("build").
		For(&v1alpha1.Build{}).
		Owns(&batchv1.Job{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(buildOfPod)).
		Watches(&v1alpha1.Subnet{}, handler.EnqueueRequestsFromMapFunc(r.buildsOfSubnet)).
		Watches(&v1alpha1.Cluster{}, handler.EnqueueRequestsFromMapFunc(r.pendingBuilds)).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(r.pendingBuilds), builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			return strings.HasPrefix(obj.GetName(), "network.")
		}))).
		Complete(r)
}

func (r *BuildReconciler) pendingBuilds(ctx context.Context, _ client.Object) []reconcile.Request {
	var builds v1alpha1.BuildList
	if err := r.Client.List(ctx, &builds); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, build := range builds.Items {
		if build.Status.JobName == "" && !IsTerminalBuildPhase(build.Status.Phase) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&build)})
		}
	}
	return requests
}

func buildOfPod(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	if labels[v1alpha1.LabelKind] != BuildKind || labels[v1alpha1.LabelName] == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: labels[v1alpha1.LabelName]}}}
}

func (r *BuildReconciler) buildsOfSubnet(ctx context.Context, obj client.Object) []reconcile.Request {
	var list v1alpha1.BuildList
	if err := r.Client.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, b := range list.Items {
		if b.Spec.Network.SubnetRef == obj.GetName() && !IsTerminalBuildPhase(b.Status.Phase) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&b)})
		}
	}
	return requests
}
