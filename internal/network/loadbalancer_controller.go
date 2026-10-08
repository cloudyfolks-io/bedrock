package network

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

type LoadBalancerReconciler struct {
	Client client.Client
	Kinds  KindProbe
}

type resolvedTargets struct {
	addresses map[string]string
	vpc       string
}

func (r *LoadBalancerReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var lb v1alpha1.LoadBalancer
	if err := r.Client.Get(ctx, req.NamespacedName, &lb); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !lb.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &lb)
	}
	if err := ensureFinalizer(ctx, r.Client, &lb); err != nil {
		return reconcile.Result{}, err
	}
	cond, requeue, err := r.program(ctx, lb)
	if err != nil {
		return reconcile.Result{}, err
	}
	return r.publish(ctx, lb, cond, requeue), nil
}

func (r *LoadBalancerReconciler) publish(ctx context.Context, lb v1alpha1.LoadBalancer, cond metav1.Condition, requeue time.Duration) reconcile.Result {
	body := &v1alpha1.LoadBalancer{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "LoadBalancer"},
		ObjectMeta: metav1.ObjectMeta{Namespace: lb.Namespace, Name: lb.Name},
		Status:     v1alpha1.LoadBalancerStatus{CommonStatus: v1alpha1.CommonStatus{ObservedGeneration: lb.Generation, Conditions: withCondition(lb.Status.Conditions, cond)}},
	}
	if err := applyStatus(ctx, r.Client, body); err != nil && requeue == 0 {
		return reconcile.Result{RequeueAfter: waitRequeue}
	}
	return reconcile.Result{RequeueAfter: requeue}
}

func refused(lb v1alpha1.LoadBalancer, v *Violation) (metav1.Condition, time.Duration, error) {
	requeue := time.Duration(0)
	switch v.Reason {
	case ReasonElasticIPNotReady, ReasonFabricNotReady, ReasonWaitingForAddress:
		requeue = waitRequeue
	}
	return violationCondition(v1alpha1.ConditionReady, lb.Generation, v), requeue, nil
}

func (r *LoadBalancerReconciler) program(ctx context.Context, lb v1alpha1.LoadBalancer) (metav1.Condition, time.Duration, error) {
	var namespace corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: lb.Namespace}, &namespace); err != nil {
		return metav1.Condition{}, 0, err
	}
	tenant, project := namespace.Labels[v1alpha1.LabelTenant], namespace.Labels[v1alpha1.LabelProject]
	if tenant == "" || project == "" {
		return refused(lb, &Violation{Reason: ReasonNamespaceNotLabeled, Message: "the namespace has no tenant and project labels; only project namespaces hold load balancers"})
	}
	if v := missingKind(r.Kinds, "RouterLBRule"); v != nil {
		return refused(lb, v)
	}
	eip, v, err := r.elasticIP(ctx, lb)
	if err != nil || v != nil {
		if err != nil {
			return metav1.Condition{}, 0, err
		}
		return refused(lb, v)
	}
	targets, v, err := r.resolve(ctx, lb)
	if err != nil || v != nil {
		if err != nil {
			return metav1.Condition{}, 0, err
		}
		return refused(lb, v)
	}
	listeners, missing, v := BuildListeners(lb.Spec.Listeners, targets.addresses)
	if v != nil {
		return refused(lb, v)
	}
	withEndpoints := slices.DeleteFunc(slices.Clone(listeners), func(l Listener) bool { return len(l.Endpoints) == 0 })
	if len(withEndpoints) == 0 {
		return refused(lb, &Violation{Reason: ReasonNoTargets, Message: "no target has an address yet"})
	}
	rules := ListenerRules(ListenerInput{
		Namespace: lb.Namespace, Name: lb.Name, EIP: FabricName(eip.Namespace, eip.Name), Vpc: targets.vpc,
		Labels: ObjectLabels("LoadBalancer", lb.Name, tenant, project), Annotations: OwnerAnnotations(&lb), Listeners: withEndpoints,
	})
	return r.apply(ctx, lb, rules, missing)
}

func (r *LoadBalancerReconciler) elasticIP(ctx context.Context, lb v1alpha1.LoadBalancer) (v1alpha1.ElasticIP, *Violation, error) {
	if lb.Spec.ElasticIPRef == "" {
		return v1alpha1.ElasticIP{}, &Violation{Reason: ReasonUnsupported, Message: "a LoadBalancer without elasticIPRef is not supported yet; set elasticIPRef"}, nil
	}
	var eip v1alpha1.ElasticIP
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: lb.Namespace, Name: lb.Spec.ElasticIPRef}, &eip)
	if apierrors.IsNotFound(err) {
		return eip, &Violation{Reason: ReasonElasticIPNotFound, Message: fmt.Sprintf("the ElasticIP %q does not exist", lb.Spec.ElasticIPRef)}, nil
	}
	if err != nil {
		return eip, nil, err
	}
	target := eip.Spec.Target
	if target == nil || target.Kind != v1alpha1.ElasticIPTargetLoadBalancer || target.Name != lb.Name {
		return eip, &Violation{Reason: ReasonTargetMismatch, Message: fmt.Sprintf("the ElasticIP %q must name this LoadBalancer as its target", eip.Name)}, nil
	}
	if !v1alpha1.IsConditionTrue(eip.Status.Conditions, v1alpha1.ConditionReady) || eip.Status.Address == "" {
		return eip, &Violation{Reason: ReasonElasticIPNotReady, Message: fmt.Sprintf("the ElasticIP %q is not ready yet", eip.Name)}, nil
	}
	return eip, nil, nil
}

func (r *LoadBalancerReconciler) resolve(ctx context.Context, lb v1alpha1.LoadBalancer) (resolvedTargets, *Violation, error) {
	ips, err := listObjects(ctx, r.Client, fabric.IPGVK)
	if err != nil {
		return resolvedTargets{}, nil, err
	}
	resolved := resolvedTargets{addresses: map[string]string{}}
	for _, name := range targetNames(lb) {
		vpc, address, v, err := r.resolveTarget(ctx, lb, name, ips)
		if err != nil || v != nil {
			return resolvedTargets{}, v, err
		}
		if resolved.vpc != "" && resolved.vpc != vpc {
			return resolvedTargets{}, &Violation{Reason: ReasonTargetNetworkMismatch, Message: "all targets of a LoadBalancer must be on one Network"}, nil
		}
		resolved.vpc = vpc
		if address != "" {
			resolved.addresses[name] = address
		}
	}
	return resolved, nil, nil
}

func targetNames(lb v1alpha1.LoadBalancer) []string {
	var names []string
	for _, l := range lb.Spec.Listeners {
		for _, t := range l.Targets {
			if !slices.Contains(names, t.VMRef) {
				names = append(names, t.VMRef)
			}
		}
	}
	slices.Sort(names)
	return names
}

func (r *LoadBalancerReconciler) resolveTarget(ctx context.Context, lb v1alpha1.LoadBalancer, name string, ips []*unstructured.Unstructured) (vpc, address string, v *Violation, err error) {
	var vm v1alpha1.VirtualMachine
	if err = r.Client.Get(ctx, client.ObjectKey{Namespace: lb.Namespace, Name: name}, &vm); err != nil || len(vm.Spec.Networks) == 0 {
		if err == nil || apierrors.IsNotFound(err) {
			return "", "", &Violation{Reason: ReasonTargetNotFound, Message: fmt.Sprintf("the VirtualMachine %q does not exist or has no interface", name)}, nil
		}
		return "", "", nil, err
	}
	nic := vm.Spec.Networks[0]
	var subnet v1alpha1.Subnet
	if err = r.Client.Get(ctx, client.ObjectKey{Namespace: lb.Namespace, Name: nic.SubnetRef}, &subnet); err != nil {
		if apierrors.IsNotFound(err) {
			return "", "", &Violation{Reason: ReasonTargetNotFound, Message: fmt.Sprintf("the Subnet %q of the VirtualMachine %q does not exist", nic.SubnetRef, name)}, nil
		}
		return "", "", nil, err
	}
	var network v1alpha1.Network
	err = r.Client.Get(ctx, client.ObjectKey{Name: subnet.Spec.NetworkRef}, &network)
	if err != nil && !apierrors.IsNotFound(err) {
		return "", "", nil, err
	}
	if err != nil || network.Status.VPC == "" {
		return "", "", &Violation{Reason: ReasonFabricNotReady, Message: fmt.Sprintf("the Network %q has no Vpc yet", subnet.Spec.NetworkRef)}, nil
	}
	ipName, found := FindIP(ips, lb.Namespace, name, FabricName(lb.Namespace, nic.SubnetRef))
	if found {
		address = ipAddress(ips, ipName)
	}
	return network.Status.VPC, address, nil, nil
}

func ipAddress(ips []*unstructured.Unstructured, name string) string {
	for _, ip := range ips {
		if ip.GetName() == name {
			return fabric.IPAddress(ip)
		}
	}
	return ""
}

func (r *LoadBalancerReconciler) apply(ctx context.Context, lb v1alpha1.LoadBalancer, rules []*unstructured.Unstructured, missing []string) (metav1.Condition, time.Duration, error) {
	wanted := map[string]bool{}
	for _, rule := range rules {
		if existing, err := getObject(ctx, r.Client, fabric.RouterLBRuleGVK, rule.GetName()); err == nil && !ownedBy(existing, &lb) {
			return refused(lb, &Violation{Reason: ReasonFabricConflict, Message: fmt.Sprintf("a Fabric RouterLBRule named %s belongs to another owner", rule.GetName())})
		}
		if err := ssa.Apply(ctx, r.Client, rule, v1alpha1.NetworkFieldManager); err != nil {
			return metav1.Condition{}, 0, err
		}
		wanted[rule.GetName()] = true
	}
	owned, err := r.ownedRules(ctx, &lb)
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
	if len(missing) > 0 {
		return refused(lb, &Violation{Reason: ReasonWaitingForAddress, Message: fmt.Sprintf("the VirtualMachines %s have no address yet", strings.Join(missing, ", "))})
	}
	for name := range wanted {
		observed, err := getObject(ctx, r.Client, fabric.RouterLBRuleGVK, name)
		if err != nil {
			return metav1.Condition{}, 0, err
		}
		if fabric.RouterLBRuleService(observed) == "" {
			return refused(lb, &Violation{Reason: ReasonFabricNotReady, Message: "waiting for Fabric to program the rules"})
		}
	}
	return trueCondition(v1alpha1.ConditionReady, lb.Generation, ReasonProgrammed), 0, nil
}

func (r *LoadBalancerReconciler) ownedRules(ctx context.Context, lb *v1alpha1.LoadBalancer) ([]*unstructured.Unstructured, error) {
	rules, err := listObjects(ctx, r.Client, fabric.RouterLBRuleGVK)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(rules, func(rule *unstructured.Unstructured) bool { return !ownedBy(rule, lb) }), nil
}

func (r *LoadBalancerReconciler) finalize(ctx context.Context, lb *v1alpha1.LoadBalancer) (reconcile.Result, error) {
	owned, err := r.ownedRules(ctx, lb)
	if err != nil {
		return reconcile.Result{}, err
	}
	for _, rule := range owned {
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, rule)); err != nil {
			return reconcile.Result{}, err
		}
	}
	remaining, err := r.ownedRules(ctx, lb)
	if err != nil || len(remaining) > 0 {
		return reconcile.Result{RequeueAfter: waitRequeue}, err
	}
	return reconcile.Result{}, releaseFinalizer(ctx, r.Client, lb)
}
