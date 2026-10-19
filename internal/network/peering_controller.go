package network

import (
	"context"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

type NetworkPeeringReconciler struct {
	Client client.Client
	Reader client.Reader
}

func (r *NetworkPeeringReconciler) live() client.Reader {
	if r.Reader != nil {
		return r.Reader
	}
	return r.Client
}

func (r *NetworkPeeringReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var peering v1alpha1.NetworkPeering
	if err := r.Client.Get(ctx, req.NamespacedName, &peering); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !peering.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}
	cfg, err := LoadConfig(ctx, r.Client)
	if err != nil {
		if _, publishErr := r.publish(ctx, peering, peering.Status.ConnectCIDR, falseCondition(v1alpha1.ConditionReady, peering.Generation, ReasonPending, err.Error())); publishErr != nil {
			return reconcile.Result{}, publishErr
		}
		return reconcile.Result{}, err
	}
	var all v1alpha1.NetworkPeeringList
	if err := r.live().List(ctx, &all); err != nil {
		return reconcile.Result{}, err
	}
	cidr, violation := ResolvePeering(peering, all.Items, cfg.PeeringRange)
	if violation != nil {
		return r.publish(ctx, peering, "", violationCondition(v1alpha1.ConditionReady, peering.Generation, violation))
	}
	condition, err := r.readiness(ctx, peering)
	if err != nil {
		return reconcile.Result{}, err
	}
	return r.publish(ctx, peering, cidr, condition)
}

func (r *NetworkPeeringReconciler) readiness(ctx context.Context, peering v1alpha1.NetworkPeering) (metav1.Condition, error) {
	generation := peering.Generation
	for _, ref := range peering.Spec.NetworkRefs {
		var network v1alpha1.Network
		err := r.Client.Get(ctx, client.ObjectKey{Name: ref}, &network)
		if apierrors.IsNotFound(err) {
			return falseCondition(v1alpha1.ConditionReady, generation, ReasonNetworkNotFound, "the Network "+ref+" does not exist"), nil
		}
		if err != nil {
			return metav1.Condition{}, err
		}
	}
	a, b := VpcName(peering.Spec.NetworkRefs[0]), VpcName(peering.Spec.NetworkRefs[1])
	programmed, err := r.vpcsListEachOther(ctx, a, b)
	if err != nil {
		return metav1.Condition{}, err
	}
	if !programmed {
		return falseCondition(v1alpha1.ConditionReady, generation, ReasonPending, "waiting for Fabric to program the peering on both Vpcs"), nil
	}
	return trueCondition(v1alpha1.ConditionReady, generation, ReasonProgrammed), nil
}

func (r *NetworkPeeringReconciler) vpcsListEachOther(ctx context.Context, a, b string) (bool, error) {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		vpc, err := getObject(ctx, r.Client, fabric.VpcGVK, pair[0])
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !slices.Contains(fabric.VpcStatusPeerings(vpc), pair[1]) {
			return false, nil
		}
	}
	return true, nil
}

func (r *NetworkPeeringReconciler) publish(ctx context.Context, peering v1alpha1.NetworkPeering, cidr string, ready metav1.Condition) (reconcile.Result, error) {
	conditions := withCondition(peering.Status.Conditions, ready)
	body := &v1alpha1.NetworkPeering{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "NetworkPeering"},
		ObjectMeta: metav1.ObjectMeta{Name: peering.Name},
		Status: v1alpha1.NetworkPeeringStatus{
			CommonStatus: v1alpha1.CommonStatus{ObservedGeneration: peering.Generation, Conditions: conditions},
			ConnectCIDR:  cidr,
		},
	}
	if err := ssa.ApplyStatus(ctx, r.Client, body, v1alpha1.NetworkFieldManager); err != nil {
		return reconcile.Result{}, err
	}
	if meta.IsStatusConditionTrue(conditions, v1alpha1.ConditionReady) {
		return reconcile.Result{}, nil
	}
	return reconcile.Result{RequeueAfter: waitRequeue}, nil
}

func (r *NetworkPeeringReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c := mgr.GetClient()
	peeringsOf := func(ctx context.Context, name string) []reconcile.Request {
		var list v1alpha1.NetworkPeeringList
		if err := c.List(ctx, &list); err != nil {
			return nil
		}
		return PeeringsOfNetwork(list.Items, name)
	}
	return ctrl.NewControllerManagedBy(mgr).Named("network-peering").
		For(&v1alpha1.NetworkPeering{}).
		Watches(&v1alpha1.Network{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			return peeringsOf(ctx, obj.GetName())
		})).
		Watches(fabricObject("Vpc"), handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			var requests []reconcile.Request
			for _, owner := range OwnerRequests(obj) {
				requests = append(requests, peeringsOf(ctx, owner.Name)...)
			}
			return requests
		})).
		Complete(r)
}
