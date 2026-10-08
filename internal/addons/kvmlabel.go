package addons

import (
	"context"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func KVMLabelPatch(host v1alpha1.Host, node corev1.Node) ([]byte, bool) {
	if host.Status.Checks == nil || host.Status.Checks.KVM == nil {
		return nil, false
	}
	current, labelled := node.Labels[LabelKVM]
	var value any
	switch {
	case *host.Status.Checks.KVM && current != "true":
		value = "true"
	case !*host.Status.Checks.KVM && labelled:
		value = nil
	default:
		return nil, false
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"labels": map[string]any{LabelKVM: value}}})
	return patch, err == nil
}

type KVMLabeler struct {
	Client client.Client
}

func (r *KVMLabeler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var host v1alpha1.Host
	if err := r.Client.Get(ctx, req.NamespacedName, &host); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var node corev1.Node
	if err := r.Client.Get(ctx, types.NamespacedName{Name: host.Name}, &node); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	patch, needed := KVMLabelPatch(host, node)
	if !needed {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Client.Patch(ctx, &node, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(v1alpha1.AddonsFieldManager))
}

func (r *KVMLabeler) SetupWithManager(mgr ctrl.Manager) error {
	nodeToHost := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: obj.GetName()}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("addon-kvm-label").
		For(&v1alpha1.Host{}).
		Watches(&corev1.Node{}, nodeToHost).
		Complete(r)
}
