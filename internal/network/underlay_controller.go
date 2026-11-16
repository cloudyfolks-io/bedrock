package network

import (
	"context"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

var underlayKinds = []schema.GroupVersionKind{fabric.SubnetGVK, fabric.VlanGVK, fabric.ProviderNetworkGVK, fabric.BgpConfGVK}

type UnderlayReconciler struct {
	Client client.Client
}

func (r *UnderlayReconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	cfg, err := LoadConfig(ctx, r.Client)
	if err != nil {
		return reconcile.Result{}, warn(ctx, r.Client, settingReference(keyProviderNetwork), "InvalidSettings", err.Error())
	}
	existing, err := r.existingCarriers(ctx)
	if err != nil {
		return reconcile.Result{}, err
	}
	objects, warnings, err := RenderUnderlayOver(cfg, existing)
	if err != nil {
		return reconcile.Result{}, warn(ctx, r.Client, settingReference(keyProviderNetwork), "InvalidSettings", err.Error())
	}
	for _, message := range warnings {
		if err := warn(ctx, r.Client, settingReference(keyProviderNetwork), ReasonBGPNotConfigured, message); err != nil {
			return reconcile.Result{}, err
		}
	}
	ranges, err := r.clusterRanges(ctx)
	if err != nil {
		return reconcile.Result{}, err
	}
	if err := TenantManagementMissing(cfg, ranges); err != nil {
		if err := warn(ctx, r.Client, settingReference(keyTenantManagementEnabled), "InvalidSettings", err.Error()); err != nil {
			return reconcile.Result{}, err
		}
		cfg.TenantManagement.Enabled = false
	}
	if cfg.TenantManagement.Enabled {
		if err := TenantManagementConflict(cfg, ranges); err != nil {
			return reconcile.Result{}, warn(ctx, r.Client, settingReference(keyTenantManagementEnabled), "InvalidSettings", err.Error())
		}
	}
	objects = append(objects, TenantManagementSubnets(cfg, ranges)...)
	wanted := map[string]bool{}
	for _, object := range objects {
		if err := ssa.Apply(ctx, r.Client, object, v1alpha1.NetworkFieldManager); err != nil {
			return reconcile.Result{}, err
		}
		wanted[identity(object)] = true
	}
	servicesReady, err := r.syncServicesPool(ctx, cfg)
	if err != nil {
		return reconcile.Result{}, err
	}
	if err := r.syncAttachment(ctx, cfg.TenantManagement.Enabled); err != nil {
		return reconcile.Result{}, err
	}
	if err := r.ensureOVNTLS(ctx, cfg.OVNTLS); err != nil {
		return reconcile.Result{}, err
	}
	refused, err := r.prune(ctx, wanted)
	if err != nil {
		return reconcile.Result{}, err
	}
	if refused != "" {
		return reconcile.Result{RequeueAfter: underlayRetry}, warn(ctx, r.Client, settingReference(keyProviderNetwork), ReasonInUse, refused)
	}
	if !servicesReady {
		return reconcile.Result{RequeueAfter: underlayRetry}, nil
	}
	return reconcile.Result{}, nil
}

func (r *UnderlayReconciler) clusterRanges(ctx context.Context) (ClusterRanges, error) {
	var cluster v1alpha1.Cluster
	err := r.Client.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster)
	if apierrors.IsNotFound(err) {
		return DefaultClusterRanges(), nil
	}
	return ClusterRangesOf(cluster), err
}

func (r *UnderlayReconciler) syncAttachment(ctx context.Context, enabled bool) error {
	if enabled {
		return ssa.Apply(ctx, r.Client, VPCAttachment(), v1alpha1.NetworkFieldManager)
	}
	existing := newObject(attachmentGVK, VPCAttachmentName)
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: VPCAttachmentNamespace, Name: VPCAttachmentName}, existing)
	if apierrors.IsNotFound(err) || (err == nil && existing.GetLabels()[LabelComponent] != ComponentUnderlay) {
		return nil
	}
	if err != nil {
		return err
	}
	return client.IgnoreNotFound(r.Client.Delete(ctx, existing))
}

func (r *UnderlayReconciler) ensureOVNTLS(ctx context.Context, enabled bool) error {
	if !enabled {
		return nil
	}
	secret, err := OVNTLSSecret(time.Now())
	if err != nil {
		return err
	}
	return client.IgnoreAlreadyExists(r.Client.Create(ctx, secret))
}

func (r *UnderlayReconciler) existingCarriers(ctx context.Context) ([]*unstructured.Unstructured, error) {
	var existing []*unstructured.Unstructured
	for _, gvk := range []schema.GroupVersionKind{fabric.ProviderNetworkGVK, fabric.VlanGVK} {
		objects, err := listObjects(ctx, r.Client, gvk, client.MatchingLabels{LabelComponent: ComponentUnderlay})
		if err != nil {
			return nil, err
		}
		existing = append(existing, objects...)
	}
	return existing, nil
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
			if holder, held, err := r.holder(ctx, gvk, object.GetName()); err != nil {
				return "", err
			} else if held {
				refused = append(refused, identity(object)+": still used by "+holder)
				continue
			}
			if err := deleteObject(ctx, r.Client, gvk, object.GetName()); err != nil {
				refused = append(refused, identity(object)+": "+err.Error())
			}
		}
	}
	return strings.Join(refused, "; "), nil
}

func (r *UnderlayReconciler) holder(ctx context.Context, gvk schema.GroupVersionKind, name string) (string, bool, error) {
	var holderGVK schema.GroupVersionKind
	var field string
	switch gvk {
	case fabric.SubnetGVK:
		holderGVK, field = fabric.LoadBalancerPoolGVK, "subnet"
	case fabric.VlanGVK:
		holderGVK, field = fabric.SubnetGVK, "vlan"
	case fabric.ProviderNetworkGVK:
		holderGVK, field = fabric.VlanGVK, "provider"
	default:
		return "", false, nil
	}
	holders, err := listObjects(ctx, r.Client, holderGVK)
	if err != nil {
		return "", false, err
	}
	for _, h := range holders {
		if nestedStringOf(h, "spec", field) == name {
			return identity(h), true, nil
		}
	}
	return "", false, nil
}

func toUnderlay(context.Context, client.Object) []reconcile.Request {
	return []reconcile.Request{UnderlayRequest}
}

func (r *UnderlayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	networkSettings := predicate.NewPredicateFuncs(func(obj client.Object) bool { return strings.HasPrefix(obj.GetName(), settingPrefix) })
	underlayLabel := predicate.NewPredicateFuncs(func(obj client.Object) bool { return obj.GetLabels()[LabelComponent] == ComponentUnderlay })
	b := ctrl.NewControllerManagedBy(mgr).Named(underlayControllerID).
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(toUnderlay), builder.WithPredicates(networkSettings)).
		Watches(&v1alpha1.Cluster{}, handler.EnqueueRequestsFromMapFunc(toUnderlay))
	for _, gvk := range append(slices.Clone(underlayKinds), attachmentGVK) {
		object := newObject(gvk, "")
		b = b.Watches(object, handler.EnqueueRequestsFromMapFunc(toUnderlay), builder.WithPredicates(underlayLabel))
	}
	servicesObject := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetName() == servicesLoadBalancerPoolName || obj.GetName() == tenantManagementVPC
	})
	for _, gvk := range []schema.GroupVersionKind{fabric.LoadBalancerPoolGVK, fabric.VpcGVK} {
		b = b.Watches(newObject(gvk, ""), handler.EnqueueRequestsFromMapFunc(toUnderlay), builder.WithPredicates(servicesObject))
	}
	return b.Complete(r)
}
