package operator

import (
	"context"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

type HostReconciler struct {
	Client client.Client
}

func (r *HostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	requeue, err := r.reconcileAuthnRestart(ctx, time.Now())
	if err != nil {
		return ctrl.Result{}, err
	}
	result, err := r.reconcileHost(ctx, req)
	result.RequeueAfter = requeue
	return result, err
}

func (r *HostReconciler) reconcileHost(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var host v1alpha1.Host
	err := r.Client.Get(ctx, req.NamespacedName, &host)
	if errors.IsNotFound(err) {
		return ctrl.Result{}, r.createHostFromNode(ctx, req.Name)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	var node corev1.Node
	err = r.Client.Get(ctx, client.ObjectKey{Name: host.Name}, &node)
	if errors.IsNotFound(err) {
		next := hostStatus(host, nil, metav1.ConditionFalse, "NodeMissing", "no Node with this name")
		return ctrl.Result{}, r.updateStatus(ctx, host, next)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	patch := client.MergeFromWithOptions(node.DeepCopy(), client.MergeFromWithOptimisticLock{})
	if ApplyRoles(&node, host.Spec.Roles, host.Spec.Management.Enabled) {
		if err := r.Client.Patch(ctx, &node, patch); err != nil {
			return ctrl.Result{}, err
		}
	}
	cluster, err := r.cluster(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureHostConfig(ctx, host, cluster); err != nil {
		return ctrl.Result{}, err
	}
	next := hostStatus(host, &node, metav1.ConditionTrue, "RolesApplied", "")
	return ctrl.Result{}, r.updateStatus(ctx, host, next)
}

func (r *HostReconciler) allHostRequests(ctx context.Context) []reconcile.Request {
	var hosts v1alpha1.HostList
	if err := r.Client.List(ctx, &hosts); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(hosts.Items))
	for _, host := range hosts.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: host.Name}})
	}
	return requests
}

func (r *HostReconciler) createHostFromNode(ctx context.Context, name string) error {
	var node corev1.Node
	if err := r.Client.Get(ctx, client.ObjectKey{Name: name}, &node); err != nil {
		return client.IgnoreNotFound(err)
	}
	nodeRoles := roles.FromLabels(node.Labels)
	if len(nodeRoles) == 0 {
		return nil
	}
	host := &v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{v1alpha1.LabelKind: "Host", v1alpha1.LabelName: name}}, Spec: v1alpha1.HostSpec{Roles: nodeRoles}}
	return client.IgnoreAlreadyExists(r.Client.Create(ctx, host))
}

func (r *HostReconciler) updateStatus(ctx context.Context, host v1alpha1.Host, next v1alpha1.HostStatus) error {
	if hostStatusEqual(host.Status, next) {
		return nil
	}
	host.Status = next
	return r.Client.Status().Update(ctx, &host)
}

func hostStatus(host v1alpha1.Host, node *corev1.Node, ready metav1.ConditionStatus, reason, message string) v1alpha1.HostStatus {
	status := host.Status
	status.Conditions = slices.Clone(host.Status.Conditions)
	status.ObservedGeneration = host.Generation
	status.KubernetesVersion = ""
	if node != nil {
		status.KubernetesVersion = node.Status.NodeInfo.KubeletVersion
	}
	v1alpha1.SetCondition(&status.Conditions, metav1.Condition{Type: v1alpha1.ConditionReady, Status: ready, Reason: reason, Message: message, ObservedGeneration: host.Generation})
	return status
}

func hostStatusEqual(a, b v1alpha1.HostStatus) bool {
	if a.ObservedGeneration != b.ObservedGeneration || a.KubernetesVersion != b.KubernetesVersion || len(a.Conditions) != len(b.Conditions) {
		return false
	}
	for i := range a.Conditions {
		if a.Conditions[i].Type != b.Conditions[i].Type || a.Conditions[i].Status != b.Conditions[i].Status || a.Conditions[i].Reason != b.Conditions[i].Reason || a.Conditions[i].Message != b.Conditions[i].Message {
			return false
		}
	}
	return true
}

func (r *HostReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Host{}).
		Owns(&v1alpha1.HostConfig{}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: obj.GetName()}}}
		})).
		Watches(&v1alpha1.Cluster{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
			return r.allHostRequests(ctx)
		})).
		Complete(r)
}
