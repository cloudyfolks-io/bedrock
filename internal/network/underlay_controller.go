package network

import (
	"context"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	settingPrefix        = "network."
	underlayRetry        = time.Minute
	underlayControllerID = "network-underlay"
)

var UnderlayRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "underlay"}}

var underlayKinds = []schema.GroupVersionKind{fabric.ProviderNetworkGVK, fabric.VlanGVK, fabric.SubnetGVK, fabric.BgpConfGVK}

type UnderlayReconciler struct {
	Client client.Client
}

func (r *UnderlayReconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	cfg, err := LoadConfig(ctx, r.Client)
	if err != nil {
		return reconcile.Result{}, warn(ctx, r.Client, settingReference(keyProviderNetwork), "InvalidSettings", err.Error())
	}
	objects, warnings := RenderUnderlay(cfg)
	for _, message := range warnings {
		if err := warn(ctx, r.Client, settingReference(keyProviderNetwork), ReasonBGPNotConfigured, message); err != nil {
			return reconcile.Result{}, err
		}
	}
	wanted := map[string]bool{}
	for _, object := range objects {
		if err := ssa.Apply(ctx, r.Client, object, v1alpha1.NetworkFieldManager); err != nil {
			return reconcile.Result{}, err
		}
		wanted[identity(object)] = true
	}
	refused, err := r.prune(ctx, wanted)
	if err != nil {
		return reconcile.Result{}, err
	}
	if refused != "" {
		return reconcile.Result{RequeueAfter: underlayRetry}, warn(ctx, r.Client, settingReference(keyProviderNetwork), ReasonInUse, refused)
	}
	return reconcile.Result{}, nil
}

func identity(u *unstructured.Unstructured) string {
	return u.GetKind() + "/" + u.GetName()
}

func (r *UnderlayReconciler) prune(ctx context.Context, wanted map[string]bool) (string, error) {
	var refused []string
	for _, gvk := range underlayKinds {
		existing, err := listObjects(ctx, r.Client, gvk, client.MatchingLabels{LabelComponent: ComponentUnderlay})
		if err != nil {
			return "", err
		}
		for _, object := range existing {
			if wanted[identity(object)] {
				continue
			}
			if err := deleteObject(ctx, r.Client, gvk, object.GetName()); err != nil {
				refused = append(refused, identity(object)+": "+err.Error())
			}
		}
	}
	return strings.Join(refused, "; "), nil
}

func toUnderlay(context.Context, client.Object) []reconcile.Request {
	return []reconcile.Request{UnderlayRequest}
}

func (r *UnderlayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	networkSettings := predicate.NewPredicateFuncs(func(obj client.Object) bool { return strings.HasPrefix(obj.GetName(), settingPrefix) })
	underlayLabel := predicate.NewPredicateFuncs(func(obj client.Object) bool { return obj.GetLabels()[LabelComponent] == ComponentUnderlay })
	b := ctrl.NewControllerManagedBy(mgr).Named(underlayControllerID).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(toUnderlay), builder.WithPredicates(networkSettings))
	for _, gvk := range underlayKinds {
		object := newObject(gvk, "")
		b = b.Watches(object, handler.EnqueueRequestsFromMapFunc(toUnderlay), builder.WithPredicates(underlayLabel))
	}
	return b.Complete(r)
}
