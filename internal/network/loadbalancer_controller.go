package network

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	subnets   []string
}

type targetInfo struct {
	vpc     string
	subnet  string
	address string
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
	address, err := r.address(ctx, lb)
	if err != nil {
		return reconcile.Result{}, err
	}
	return r.publish(ctx, lb, cond, requeue, address), nil
}

func (r *LoadBalancerReconciler) address(ctx context.Context, lb v1alpha1.LoadBalancer) (string, error) {
	if lb.Spec.ElasticIPRef != "" {
		eip, v, err := r.elasticIP(ctx, lb)
		if err != nil || v != nil {
			return "", err
		}
		return eip.Status.Address, nil
	}
	vip, err := getObject(ctx, r.Client, fabric.VipGVK, InternalVipName(lb.Namespace, lb.Name))
	if apierrors.IsNotFound(err) || (err == nil && !ownedBy(vip, &lb)) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return fabric.VipAddress(vip), nil
}

func (r *LoadBalancerReconciler) publish(ctx context.Context, lb v1alpha1.LoadBalancer, cond metav1.Condition, requeue time.Duration, address string) reconcile.Result {
	body := &v1alpha1.LoadBalancer{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "LoadBalancer"},
		ObjectMeta: metav1.ObjectMeta{Namespace: lb.Namespace, Name: lb.Name},
		Status:     v1alpha1.LoadBalancerStatus{CommonStatus: v1alpha1.CommonStatus{ObservedGeneration: lb.Generation, Conditions: withCondition(lb.Status.Conditions, cond)}, Address: address},
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
	if lb.Spec.ElasticIPRef == "" {
		return r.programInternal(ctx, lb, tenant, project)
	}
	if v := missingKind(r.Kinds, "RouterLBRule"); v != nil {
		return refused(lb, v)
	}
	eip, v, err := r.elasticIP(ctx, lb)
	if err != nil || v != nil {
		if err != nil {
			return metav1.Condition{}, 0, err
		}
		if v.Reason == ReasonElasticIPNotFound {
			if err := r.deleteOwned(ctx, &lb); err != nil {
				return metav1.Condition{}, 0, err
			}
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
	var eip v1alpha1.ElasticIP
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: lb.Namespace, Name: lb.Spec.ElasticIPRef}, &eip)
	if apierrors.IsNotFound(err) {
		return eip, &Violation{Reason: ReasonElasticIPNotFound, Message: fmt.Sprintf("the ElasticIP %q does not exist", lb.Spec.ElasticIPRef)}, nil
	}
	if err != nil {
		return eip, nil, err
	}
	if !eip.DeletionTimestamp.IsZero() {
		return eip, &Violation{Reason: ReasonElasticIPNotFound, Message: fmt.Sprintf("the ElasticIP %q is being deleted", lb.Spec.ElasticIPRef)}, nil
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
		info, v, err := r.resolveTarget(ctx, lb, name, ips)
		if err != nil || v != nil {
			return resolvedTargets{}, v, err
		}
		if resolved.vpc != "" && resolved.vpc != info.vpc {
			return resolvedTargets{}, &Violation{Reason: ReasonTargetNetworkMismatch, Message: "all targets of a LoadBalancer must be on one Network"}, nil
		}
		resolved.vpc = info.vpc
		if !slices.Contains(resolved.subnets, info.subnet) {
			resolved.subnets = append(resolved.subnets, info.subnet)
		}
		if info.address != "" {
			resolved.addresses[name] = info.address
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

func (r *LoadBalancerReconciler) resolveTarget(ctx context.Context, lb v1alpha1.LoadBalancer, name string, ips []*unstructured.Unstructured) (info targetInfo, v *Violation, err error) {
	var vm v1alpha1.VirtualMachine
	if err = r.Client.Get(ctx, client.ObjectKey{Namespace: lb.Namespace, Name: name}, &vm); err != nil || len(vm.Spec.Networks) == 0 {
		if err == nil || apierrors.IsNotFound(err) {
			return targetInfo{}, &Violation{Reason: ReasonTargetNotFound, Message: fmt.Sprintf("the VirtualMachine %q does not exist or has no interface", name)}, nil
		}
		return targetInfo{}, nil, err
	}
	nic := vm.Spec.Networks[0]
	var subnet v1alpha1.Subnet
	if err = r.Client.Get(ctx, client.ObjectKey{Namespace: lb.Namespace, Name: nic.SubnetRef}, &subnet); err != nil {
		if apierrors.IsNotFound(err) {
			return targetInfo{}, &Violation{Reason: ReasonTargetNotFound, Message: fmt.Sprintf("the Subnet %q of the VirtualMachine %q does not exist", nic.SubnetRef, name)}, nil
		}
		return targetInfo{}, nil, err
	}
	var network v1alpha1.Network
	err = r.Client.Get(ctx, client.ObjectKey{Name: subnet.Spec.NetworkRef}, &network)
	if err != nil && !apierrors.IsNotFound(err) {
		return targetInfo{}, nil, err
	}
	if err != nil || network.Status.VPC == "" {
		return targetInfo{}, &Violation{Reason: ReasonFabricNotReady, Message: fmt.Sprintf("the Network %q has no Vpc yet", subnet.Spec.NetworkRef)}, nil
	}
	info = targetInfo{vpc: network.Status.VPC, subnet: nic.SubnetRef}
	if ipName, found := FindIP(ips, lb.Namespace, name, FabricName(lb.Namespace, nic.SubnetRef)); found {
		info.address = ipAddress(ips, ipName)
	}
	return info, nil, nil
}

func ipAddress(ips []*unstructured.Unstructured, name string) string {
	for _, ip := range ips {
		if ip.GetName() == name {
			return fabric.IPAddress(ip)
		}
	}
	return ""
}

func objectKey(u *unstructured.Unstructured) string {
	return u.GetKind() + "/" + u.GetName()
}

func isRule(u *unstructured.Unstructured) bool {
	return u.GetKind() == fabric.RouterLBRuleGVK.Kind || u.GetKind() == fabric.SwitchLBRuleGVK.Kind
}

func (r *LoadBalancerReconciler) apply(ctx context.Context, lb v1alpha1.LoadBalancer, objects []*unstructured.Unstructured, missing []string) (metav1.Condition, time.Duration, error) {
	wanted := map[string]bool{}
	for _, object := range objects {
		if existing, err := getObject(ctx, r.Client, object.GroupVersionKind(), object.GetName()); err == nil && !ownedBy(existing, &lb) {
			return refused(lb, &Violation{Reason: ReasonFabricConflict, Message: fmt.Sprintf("a Fabric %s named %s belongs to another owner", object.GetKind(), object.GetName())})
		}
		if err := ssa.Apply(ctx, r.Client, object, v1alpha1.NetworkFieldManager); err != nil {
			return metav1.Condition{}, 0, err
		}
		wanted[objectKey(object)] = true
	}
	owned, err := r.ownedObjects(ctx, &lb)
	if err != nil {
		return metav1.Condition{}, 0, err
	}
	for _, object := range owned {
		if !wanted[objectKey(object)] {
			if err := client.IgnoreNotFound(r.Client.Delete(ctx, object)); err != nil {
				return metav1.Condition{}, 0, err
			}
		}
	}
	if len(missing) > 0 {
		return refused(lb, &Violation{Reason: ReasonWaitingForAddress, Message: fmt.Sprintf("the VirtualMachines %s have no address yet", strings.Join(missing, ", "))})
	}
	for _, object := range objects {
		if !isRule(object) {
			continue
		}
		observed, err := getObject(ctx, r.Client, object.GroupVersionKind(), object.GetName())
		if err != nil {
			return metav1.Condition{}, 0, err
		}
		if fabric.RouterLBRuleService(observed) == "" {
			return refused(lb, &Violation{Reason: ReasonFabricNotReady, Message: "waiting for Fabric to program the rules"})
		}
	}
	return trueCondition(v1alpha1.ConditionReady, lb.Generation, ReasonProgrammed), 0, nil
}

var balancerKinds = []schema.GroupVersionKind{fabric.RouterLBRuleGVK, fabric.SwitchLBRuleGVK, fabric.VipGVK}

func (r *LoadBalancerReconciler) ownedObjects(ctx context.Context, lb *v1alpha1.LoadBalancer) ([]*unstructured.Unstructured, error) {
	var owned []*unstructured.Unstructured
	for _, gvk := range balancerKinds {
		objects, err := listObjects(ctx, r.Client, gvk)
		if meta.IsNoMatchError(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		owned = append(owned, slices.DeleteFunc(objects, func(o *unstructured.Unstructured) bool { return !ownedBy(o, lb) })...)
	}
	return owned, nil
}

func (r *LoadBalancerReconciler) programInternal(ctx context.Context, lb v1alpha1.LoadBalancer, tenant, project string) (metav1.Condition, time.Duration, error) {
	for _, kind := range []string{"SwitchLBRule", "Vip"} {
		if v := missingKind(r.Kinds, kind); v != nil {
			return refused(lb, v)
		}
	}
	targets, v, err := r.resolve(ctx, lb)
	if err != nil || v != nil {
		if err != nil {
			return metav1.Condition{}, 0, err
		}
		return refused(lb, v)
	}
	if len(targets.subnets) > 1 {
		return refused(lb, &Violation{Reason: ReasonTargetNetworkMismatch, Message: "all targets of an internal LoadBalancer must use one Subnet"})
	}
	listeners, missing, v := BuildListeners(lb.Spec.Listeners, targets.addresses)
	if v != nil {
		return refused(lb, v)
	}
	withEndpoints := slices.DeleteFunc(slices.Clone(listeners), func(l Listener) bool { return len(l.Endpoints) == 0 })
	if len(withEndpoints) == 0 {
		return refused(lb, &Violation{Reason: ReasonNoTargets, Message: "no target has an address yet"})
	}
	in := InternalInput{Namespace: lb.Namespace, Name: lb.Name, Labels: ObjectLabels("LoadBalancer", lb.Name, tenant, project), Annotations: OwnerAnnotations(&lb), Listeners: withEndpoints}
	plane := LBSubnetName(targets.vpc)
	if present, err := exists(ctx, r.Client, fabric.SubnetGVK, plane); err != nil || !present {
		if err != nil {
			return metav1.Condition{}, 0, err
		}
		return refused(lb, &Violation{Reason: ReasonFabricNotReady, Message: fmt.Sprintf("waiting for the load balancer Subnet %s of the Network", plane)})
	}
	vip := InternalVip(in, plane)
	address, v, err := r.allocateVip(ctx, lb, vip)
	if err != nil || v != nil {
		if err != nil {
			return metav1.Condition{}, 0, err
		}
		return refused(lb, v)
	}
	in.Vip = address
	return r.apply(ctx, lb, append([]*unstructured.Unstructured{vip}, InternalRules(in)...), missing)
}

func (r *LoadBalancerReconciler) allocateVip(ctx context.Context, lb v1alpha1.LoadBalancer, vip *unstructured.Unstructured) (string, *Violation, error) {
	if existing, err := getObject(ctx, r.Client, fabric.VipGVK, vip.GetName()); err == nil && !ownedBy(existing, &lb) {
		return "", &Violation{Reason: ReasonFabricConflict, Message: fmt.Sprintf("a Fabric Vip named %s belongs to another owner", vip.GetName())}, nil
	}
	if err := ssa.Apply(ctx, r.Client, vip, v1alpha1.NetworkFieldManager); err != nil {
		return "", nil, err
	}
	observed, err := getObject(ctx, r.Client, fabric.VipGVK, vip.GetName())
	if err != nil {
		return "", nil, err
	}
	address := fabric.VipAddress(observed)
	if address == "" {
		return "", &Violation{Reason: ReasonFabricNotReady, Message: "waiting for Fabric to allocate the virtual address"}, nil
	}
	return address, nil, nil
}

func (r *LoadBalancerReconciler) deleteOwned(ctx context.Context, lb *v1alpha1.LoadBalancer) error {
	owned, err := r.ownedObjects(ctx, lb)
	if err != nil {
		return err
	}
	for _, object := range owned {
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, object)); err != nil {
			return err
		}
	}
	return nil
}

func (r *LoadBalancerReconciler) finalize(ctx context.Context, lb *v1alpha1.LoadBalancer) (reconcile.Result, error) {
	if err := r.deleteOwned(ctx, lb); err != nil {
		return reconcile.Result{}, err
	}
	remaining, err := r.ownedObjects(ctx, lb)
	if err != nil || len(remaining) > 0 {
		return reconcile.Result{RequeueAfter: waitRequeue}, err
	}
	return reconcile.Result{}, releaseFinalizer(ctx, r.Client, lb)
}
