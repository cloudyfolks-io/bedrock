package network

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

type InternetGatewayReconciler struct {
	Client client.Client
	Kinds  KindProbe
}

func (r *InternetGatewayReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var gateway v1alpha1.InternetGateway
	if err := r.Client.Get(ctx, req.NamespacedName, &gateway); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !gateway.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &gateway)
	}
	if err := ensureFinalizer(ctx, r.Client, &gateway); err != nil {
		return reconcile.Result{}, err
	}
	cond, requeue, err := r.program(ctx, gateway)
	if err != nil {
		return reconcile.Result{}, err
	}
	return r.publish(ctx, gateway, cond, requeue), nil
}

func (r *InternetGatewayReconciler) publish(ctx context.Context, gateway v1alpha1.InternetGateway, cond metav1.Condition, requeue time.Duration) reconcile.Result {
	body := &v1alpha1.InternetGateway{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "InternetGateway"},
		ObjectMeta: metav1.ObjectMeta{Namespace: gateway.Namespace, Name: gateway.Name},
		Status:     v1alpha1.InternetGatewayStatus{CommonStatus: v1alpha1.CommonStatus{ObservedGeneration: gateway.Generation, Conditions: withCondition(gateway.Status.Conditions, cond)}},
	}
	if err := applyStatus(ctx, r.Client, body); err != nil && requeue == 0 {
		return reconcile.Result{RequeueAfter: waitRequeue}
	}
	return reconcile.Result{RequeueAfter: requeue}
}

func gatewayRequeue(v *Violation) time.Duration {
	switch v.Reason {
	case ReasonElasticIPNotReady, ReasonExternalNotConnected, ReasonFRRAgentMissing, ReasonFabricNotReady:
		return waitRequeue
	}
	return 0
}

func dropsRules(v *Violation) bool {
	switch v.Reason {
	case ReasonGatewayExists, ReasonElasticIPNotFound, ReasonTargetMismatch, ReasonRefNotFound, ReasonTargetNetworkMismatch, ReasonNetworkNotFound, ReasonNetworkNotOwned:
		return true
	}
	return false
}

func (r *InternetGatewayReconciler) stop(ctx context.Context, gateway v1alpha1.InternetGateway, v *Violation) (metav1.Condition, time.Duration, error) {
	if dropsRules(v) {
		if err := r.deleteOwned(ctx, &gateway); err != nil {
			return metav1.Condition{}, 0, err
		}
	}
	return violationCondition(v1alpha1.ConditionReady, gateway.Generation, v), gatewayRequeue(v), nil
}

func (r *InternetGatewayReconciler) program(ctx context.Context, gateway v1alpha1.InternetGateway) (metav1.Condition, time.Duration, error) {
	rules, v, err := r.plan(ctx, gateway)
	if err != nil {
		return metav1.Condition{}, 0, err
	}
	if v != nil {
		return r.stop(ctx, gateway, v)
	}
	return r.apply(ctx, gateway, rules)
}

func (r *InternetGatewayReconciler) plan(ctx context.Context, gateway v1alpha1.InternetGateway) ([]*unstructured.Unstructured, *Violation, error) {
	var namespace corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: gateway.Namespace}, &namespace); err != nil {
		return nil, nil, err
	}
	tenant, project := namespace.Labels[v1alpha1.LabelTenant], namespace.Labels[v1alpha1.LabelProject]
	if tenant == "" || project == "" {
		return nil, &Violation{Reason: ReasonNamespaceNotLabeled, Message: "the namespace has no tenant and project labels; only project namespaces hold internet gateways"}, nil
	}
	if v := missingKind(r.Kinds, "OvnSnatRule"); v != nil {
		return nil, v, nil
	}
	network, v, err := r.network(ctx, gateway, tenant)
	if err != nil || v != nil {
		return nil, v, err
	}
	if v, err := r.otherGateway(ctx, gateway, network); err != nil || v != nil {
		return nil, v, err
	}
	eip, v, err := r.elasticIP(ctx, gateway)
	if err != nil || v != nil {
		return nil, v, err
	}
	subnet, v, err := r.subnet(ctx, gateway)
	if err != nil || v != nil {
		return nil, v, err
	}
	cfg, err := LoadConfig(ctx, r.Client)
	if err != nil {
		return nil, nil, err
	}
	if v, err := networkExternalPath(ctx, r.Client, eip, cfg, network); err != nil || v != nil {
		return nil, v, err
	}
	in := SnatInput{Network: VpcName(network.Name), EIP: FabricName(eip.Namespace, eip.Name), Labels: ObjectLabels("InternetGateway", gateway.Name, tenant, project), Annotations: OwnerAnnotations(&gateway)}
	return snatRules(in, network, subnet), nil, nil
}

func snatRules(in SnatInput, network v1alpha1.Network, fabricSubnet string) []*unstructured.Unstructured {
	if fabricSubnet != "" {
		return []*unstructured.Unstructured{SubnetSnatRule(in, fabricSubnet)}
	}
	return RangeSnatRules(in, network.Spec.Ranges())
}

func (r *InternetGatewayReconciler) network(ctx context.Context, gateway v1alpha1.InternetGateway, tenant string) (v1alpha1.Network, *Violation, error) {
	var network v1alpha1.Network
	err := r.Client.Get(ctx, client.ObjectKey{Name: gateway.Spec.NetworkRef}, &network)
	if apierrors.IsNotFound(err) {
		return network, &Violation{Reason: ReasonNetworkNotFound, Message: fmt.Sprintf("the Network %q does not exist", gateway.Spec.NetworkRef)}, nil
	}
	if err != nil {
		return network, nil, err
	}
	return network, NetworkAllowed(tenant, network), nil
}

func (r *InternetGatewayReconciler) otherGateway(ctx context.Context, gateway v1alpha1.InternetGateway, network v1alpha1.Network) (*Violation, error) {
	var all v1alpha1.InternetGatewayList
	if err := r.Client.List(ctx, &all); err != nil {
		return nil, err
	}
	for _, other := range all.Items {
		if other.UID == gateway.UID || other.Spec.NetworkRef != gateway.Spec.NetworkRef || !Older(&other, &gateway) {
			continue
		}
		owned, err := r.ownedByTenantOf(ctx, other.Namespace, network)
		if err != nil {
			return nil, err
		}
		if owned {
			return &Violation{Reason: ReasonGatewayExists, Message: fmt.Sprintf("the InternetGateway %s/%s already serves the Network %q", other.Namespace, other.Name, gateway.Spec.NetworkRef)}, nil
		}
	}
	return nil, nil
}

func (r *InternetGatewayReconciler) ownedByTenantOf(ctx context.Context, namespace string, network v1alpha1.Network) (bool, error) {
	var holder corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: namespace}, &holder); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return NetworkAllowed(holder.Labels[v1alpha1.LabelTenant], network) == nil, nil
}

func (r *InternetGatewayReconciler) elasticIP(ctx context.Context, gateway v1alpha1.InternetGateway) (v1alpha1.ElasticIP, *Violation, error) {
	var eip v1alpha1.ElasticIP
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: gateway.Namespace, Name: gateway.Spec.ElasticIPRef}, &eip)
	if apierrors.IsNotFound(err) {
		return eip, &Violation{Reason: ReasonElasticIPNotFound, Message: fmt.Sprintf("the ElasticIP %q does not exist", gateway.Spec.ElasticIPRef)}, nil
	}
	if err != nil {
		return eip, nil, err
	}
	if !eip.DeletionTimestamp.IsZero() {
		return eip, &Violation{Reason: ReasonElasticIPNotFound, Message: fmt.Sprintf("the ElasticIP %q is being deleted", eip.Name)}, nil
	}
	target := eip.Spec.Target
	if target == nil || target.Kind != v1alpha1.ElasticIPTargetInternetGateway || target.Name != gateway.Name {
		return eip, &Violation{Reason: ReasonTargetMismatch, Message: fmt.Sprintf("the ElasticIP %q must name this InternetGateway as its target", eip.Name)}, nil
	}
	if !v1alpha1.IsConditionTrue(eip.Status.Conditions, v1alpha1.ConditionReady) || eip.Status.Address == "" {
		return eip, &Violation{Reason: ReasonElasticIPNotReady, Message: fmt.Sprintf("the ElasticIP %q is not ready yet", eip.Name)}, nil
	}
	return eip, nil, nil
}

func (r *InternetGatewayReconciler) subnet(ctx context.Context, gateway v1alpha1.InternetGateway) (string, *Violation, error) {
	if gateway.Spec.SubnetRef == "" {
		return "", nil, nil
	}
	var subnet v1alpha1.Subnet
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: gateway.Namespace, Name: gateway.Spec.SubnetRef}, &subnet)
	if apierrors.IsNotFound(err) {
		return "", &Violation{Reason: ReasonRefNotFound, Message: fmt.Sprintf("the Subnet %q does not exist", gateway.Spec.SubnetRef)}, nil
	}
	if err != nil {
		return "", nil, err
	}
	if subnet.Spec.NetworkRef != gateway.Spec.NetworkRef {
		return "", &Violation{Reason: ReasonTargetNetworkMismatch, Message: fmt.Sprintf("the Subnet %q belongs to the Network %q", subnet.Name, subnet.Spec.NetworkRef)}, nil
	}
	if !v1alpha1.IsConditionTrue(subnet.Status.Conditions, v1alpha1.ConditionReady) {
		return "", &Violation{Reason: ReasonFabricNotReady, Message: fmt.Sprintf("the Subnet %q is not ready yet", subnet.Name)}, nil
	}
	return FabricName(subnet.Namespace, subnet.Name), nil, nil
}

func (r *InternetGatewayReconciler) apply(ctx context.Context, gateway v1alpha1.InternetGateway, rules []*unstructured.Unstructured) (metav1.Condition, time.Duration, error) {
	wanted := map[string]bool{}
	for _, rule := range rules {
		existing, err := getObject(ctx, r.Client, fabric.OvnSnatRuleGVK, rule.GetName())
		if err == nil && !ownedBy(existing, &gateway) {
			return r.stop(ctx, gateway, &Violation{Reason: ReasonFabricConflict, Message: fmt.Sprintf("a Fabric OvnSnatRule named %s belongs to another owner", rule.GetName())})
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return metav1.Condition{}, 0, err
		}
		if err := ssa.Apply(ctx, r.Client, rule, v1alpha1.NetworkFieldManager); err != nil {
			return metav1.Condition{}, 0, err
		}
		wanted[rule.GetName()] = true
	}
	owned, err := r.owned(ctx, &gateway)
	if err != nil {
		return metav1.Condition{}, 0, err
	}
	for _, rule := range owned {
		if !wanted[rule.GetName()] {
			if err := client.IgnoreNotFound(r.Client.Delete(ctx, rule)); err != nil {
				return metav1.Condition{}, 0, err
			}
		}
	}
	for _, rule := range rules {
		observed, err := getObject(ctx, r.Client, fabric.OvnSnatRuleGVK, rule.GetName())
		if err != nil {
			return metav1.Condition{}, 0, err
		}
		if !fabric.SnatRuleReady(observed) {
			return r.stop(ctx, gateway, &Violation{Reason: ReasonFabricNotReady, Message: "waiting for Fabric to program the SNAT rules"})
		}
	}
	return trueCondition(v1alpha1.ConditionReady, gateway.Generation, ReasonProgrammed), 0, nil
}

func (r *InternetGatewayReconciler) owned(ctx context.Context, gateway *v1alpha1.InternetGateway) ([]*unstructured.Unstructured, error) {
	rules, err := listObjects(ctx, r.Client, fabric.OvnSnatRuleGVK, client.MatchingLabels{LabelManaged: "true"})
	if meta.IsNoMatchError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(rules, func(rule *unstructured.Unstructured) bool { return !ownedBy(rule, gateway) }), nil
}

func (r *InternetGatewayReconciler) deleteOwned(ctx context.Context, gateway *v1alpha1.InternetGateway) error {
	owned, err := r.owned(ctx, gateway)
	if err != nil {
		return err
	}
	for _, rule := range owned {
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, rule)); err != nil {
			return err
		}
	}
	return nil
}

func (r *InternetGatewayReconciler) finalize(ctx context.Context, gateway *v1alpha1.InternetGateway) (reconcile.Result, error) {
	if err := r.deleteOwned(ctx, gateway); err != nil {
		return reconcile.Result{}, err
	}
	remaining, err := r.owned(ctx, gateway)
	if err != nil || len(remaining) > 0 {
		return reconcile.Result{RequeueAfter: waitRequeue}, err
	}
	return reconcile.Result{}, releaseFinalizer(ctx, r.Client, gateway)
}
