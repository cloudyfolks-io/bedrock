package network

import (
	"cmp"
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const moveRequeue = time.Second

type ElasticIPReconciler struct {
	Client client.Client
}

type outcome struct {
	condition metav1.Condition
	address   string
	pool      string
	requeue   time.Duration
}

func (r *ElasticIPReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var eip v1alpha1.ElasticIP
	if err := r.Client.Get(ctx, req.NamespacedName, &eip); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !eip.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &eip)
	}
	if err := ensureFinalizer(ctx, r.Client, &eip); err != nil {
		return reconcile.Result{}, err
	}
	result, err := r.decide(ctx, eip)
	if err != nil {
		return reconcile.Result{}, err
	}
	return r.publish(ctx, eip, result), nil
}

func (r *ElasticIPReconciler) publish(ctx context.Context, eip v1alpha1.ElasticIP, o outcome) reconcile.Result {
	body := statusBody(eip, v1alpha1.ElasticIPStatus{
		CommonStatus:    v1alpha1.CommonStatus{ObservedGeneration: eip.Generation, Conditions: withCondition(eip.Status.Conditions, o.condition)},
		Address:         o.address,
		ProviderNetwork: cmp.Or(o.pool, eip.Status.ProviderNetwork),
	})
	if err := applyStatus(ctx, r.Client, body); err != nil && o.requeue == 0 {
		return reconcile.Result{RequeueAfter: waitRequeue}
	}
	return reconcile.Result{RequeueAfter: o.requeue}
}

func statusBody(eip v1alpha1.ElasticIP, status v1alpha1.ElasticIPStatus) *v1alpha1.ElasticIP {
	return &v1alpha1.ElasticIP{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ElasticIP"},
		ObjectMeta: metav1.ObjectMeta{Namespace: eip.Namespace, Name: eip.Name},
		Status:     status,
	}
}

func stopped(eip v1alpha1.ElasticIP, address string, v *Violation) outcome {
	return outcome{condition: violationCondition(v1alpha1.ConditionReady, eip.Generation, v), address: address, requeue: requeueFor(v)}
}

func requeueFor(v *Violation) time.Duration {
	switch v.Reason {
	case ReasonExternalNotConnected, ReasonWaitingForAddress, ReasonFRRAgentMissing, ReasonAllocating, ReasonFabricNotReady, ReasonNoPoolCapacity:
		return waitRequeue
	}
	return 0
}

func (r *ElasticIPReconciler) decide(ctx context.Context, eip v1alpha1.ElasticIP) (outcome, error) {
	known := eip.Status.Address
	var namespace corev1.Namespace
	if err := r.Client.Get(ctx, client.ObjectKey{Name: eip.Namespace}, &namespace); err != nil {
		return outcome{}, err
	}
	tenant, project := namespace.Labels[v1alpha1.LabelTenant], namespace.Labels[v1alpha1.LabelProject]
	if tenant == "" || project == "" {
		return stopped(eip, known, &Violation{Reason: ReasonNamespaceNotLabeled, Message: "the namespace has no tenant and project labels; only project namespaces hold elastic IPs"}), nil
	}
	cfg, err := LoadConfig(ctx, r.Client)
	if err != nil {
		return outcome{}, err
	}
	if v, err := quotaViolation(ctx, r.Client, eip, tenant); err != nil || v != nil {
		if err != nil {
			return outcome{}, err
		}
		return stopped(eip, known, v), nil
	}
	eip, v, err := r.withPool(ctx, eip, cfg)
	if err != nil {
		return outcome{}, err
	}
	if v != nil {
		return stopped(eip, known, v), nil
	}
	result, err := r.reserveIn(ctx, eip, cfg, tenant, project)
	result.pool = PoolOf(eip)
	return result, err
}

func (r *ElasticIPReconciler) withPool(ctx context.Context, eip v1alpha1.ElasticIP, cfg Config) (v1alpha1.ElasticIP, *Violation, error) {
	if PoolOf(eip) != "" {
		return eip, nil, nil
	}
	usage, err := r.tenantPoolUsage(ctx, cfg)
	if err != nil {
		return eip, nil, err
	}
	name, found := LeastUsed(usage)
	if !found {
		return eip, &Violation{Reason: ReasonNoPoolCapacity, Message: "every tenant provider network is full, or none exists"}, nil
	}
	eip.Status.ProviderNetwork = name
	return eip, nil, applyStatus(ctx, r.Client, statusBody(eip, eip.Status))
}

func (r *ElasticIPReconciler) tenantPoolUsage(ctx context.Context, cfg Config) ([]PoolUsage, error) {
	var usage []PoolUsage
	for _, name := range TenantPoolNames(cfg) {
		subnet, err := getObject(ctx, r.Client, fabric.SubnetGVK, ExternalSubnetName(name))
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		usage = append(usage, PoolUsage{Name: name, Using: fabric.SubnetUsedAddresses(subnet), Available: fabric.SubnetAvailableAddresses(subnet)})
	}
	return usage, nil
}

func (r *ElasticIPReconciler) reserveIn(ctx context.Context, eip v1alpha1.ElasticIP, cfg Config, tenant, project string) (outcome, error) {
	known := eip.Status.Address
	provider, found := ProviderOf(cfg, PoolOf(eip))
	if !found {
		return stopped(eip, known, &Violation{Reason: ReasonUnknownProviderNetwork, Message: fmt.Sprintf("the provider network %q is not in network.provider-networks", PoolOf(eip))}), nil
	}
	if v := CheckAddress(provider, eip.Spec.Address); v != nil {
		return stopped(eip, known, v), nil
	}
	if v, err := r.addressTaken(ctx, eip); err != nil || v != nil {
		if err != nil {
			return outcome{}, err
		}
		return stopped(eip, known, v), nil
	}
	return r.reserve(ctx, eip, cfg, tenant, project)
}

func (r *ElasticIPReconciler) addressTaken(ctx context.Context, eip v1alpha1.ElasticIP) (*Violation, error) {
	if eip.Spec.Address == "" {
		return nil, nil
	}
	var all v1alpha1.ElasticIPList
	if err := r.Client.List(ctx, &all); err != nil {
		return nil, err
	}
	for _, other := range all.Items {
		sameAddress := other.Spec.Address == eip.Spec.Address || other.Status.Address == eip.Spec.Address
		if other.UID != eip.UID && PoolOf(other) == PoolOf(eip) && sameAddress && Older(&other, &eip) {
			return &Violation{Reason: ReasonAddressInUse, Message: fmt.Sprintf("the address %s is already assigned", eip.Spec.Address)}, nil
		}
	}
	return nil, nil
}

func (r *ElasticIPReconciler) reserve(ctx context.Context, eip v1alpha1.ElasticIP, cfg Config, tenant, project string) (outcome, error) {
	name := FabricName(eip.Namespace, eip.Name)
	existing, err := getObject(ctx, r.Client, fabric.OvnEipGVK, name)
	if err == nil && !ownedBy(existing, &eip) {
		return stopped(eip, eip.Status.Address, &Violation{Reason: ReasonFabricConflict, Message: fmt.Sprintf("a Fabric OvnEip named %s belongs to another owner", name)}), nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return outcome{}, err
	}
	meta := fabric.Meta{Name: name, Labels: ObjectLabels("ElasticIP", eip.Name, tenant, project), Annotations: OwnerAnnotations(&eip)}
	if err := ssa.Apply(ctx, r.Client, fabric.OvnEip(meta, ExternalSubnetName(PoolOf(eip)), eip.Spec.Address), v1alpha1.NetworkFieldManager); err != nil {
		return outcome{}, err
	}
	observed, err := getObject(ctx, r.Client, fabric.OvnEipGVK, name)
	if err != nil {
		return outcome{}, err
	}
	address := fabric.EipAddress(observed)
	if address == "" && fabric.EipFailed(observed) {
		return stopped(eip, eip.Status.Address, &Violation{Reason: ReasonFabricNotReady, Message: "Fabric refused the request for this address"}), nil
	}
	if address == "" {
		return stopped(eip, eip.Status.Address, &Violation{Reason: ReasonAllocating, Message: "waiting for Fabric to allocate an address"}), nil
	}
	return r.attach(ctx, eip, cfg, meta, address)
}

func (r *ElasticIPReconciler) attach(ctx context.Context, eip v1alpha1.ElasticIP, cfg Config, meta fabric.Meta, address string) (outcome, error) {
	switch {
	case eip.Spec.Target == nil:
		return r.detached(ctx, eip, address, ReasonReserved)
	case eip.Spec.Target.Kind == v1alpha1.ElasticIPTargetLoadBalancer:
		return r.attachLoadBalancer(ctx, eip, address)
	}
	return r.attachVM(ctx, eip, cfg, meta, address)
}

func (r *ElasticIPReconciler) detached(ctx context.Context, eip v1alpha1.ElasticIP, address, reason string) (outcome, error) {
	if err := r.removeFip(ctx, eip); err != nil {
		return outcome{}, err
	}
	return outcome{condition: trueCondition(v1alpha1.ConditionReady, eip.Generation, reason), address: address}, nil
}

func (r *ElasticIPReconciler) removeFip(ctx context.Context, eip v1alpha1.ElasticIP) error {
	fip, err := getObject(ctx, r.Client, fabric.OvnFipGVK, FabricName(eip.Namespace, eip.Name))
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	if !ownedBy(fip, &eip) {
		return nil
	}
	return client.IgnoreNotFound(r.Client.Delete(ctx, fip))
}

func (r *ElasticIPReconciler) attachLoadBalancer(ctx context.Context, eip v1alpha1.ElasticIP, address string) (outcome, error) {
	var lb v1alpha1.LoadBalancer
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: eip.Namespace, Name: eip.Spec.Target.Name}, &lb)
	if err != nil && !apierrors.IsNotFound(err) {
		return outcome{}, err
	}
	if apierrors.IsNotFound(err) {
		return r.refuse(ctx, eip, address, ReasonTargetNotFound, fmt.Sprintf("the LoadBalancer %q does not exist", eip.Spec.Target.Name))
	}
	if lb.Spec.ElasticIPRef != eip.Name {
		return r.refuse(ctx, eip, address, ReasonTargetMismatch, fmt.Sprintf("the LoadBalancer %q uses the ElasticIP %q as its frontend", lb.Name, lb.Spec.ElasticIPRef))
	}
	return r.detached(ctx, eip, address, ReasonAttached)
}

func (r *ElasticIPReconciler) refuse(ctx context.Context, eip v1alpha1.ElasticIP, address, reason, message string) (outcome, error) {
	if err := r.removeFip(ctx, eip); err != nil {
		return outcome{}, err
	}
	return stopped(eip, address, &Violation{Reason: reason, Message: message}), nil
}

func pickInterface(vm v1alpha1.VirtualMachine, target v1alpha1.ElasticIPTarget) (v1alpha1.VMNetwork, bool) {
	for _, n := range vm.Spec.Networks {
		if target.SubnetRef == "" || n.SubnetRef == target.SubnetRef {
			return n, true
		}
	}
	return v1alpha1.VMNetwork{}, false
}

func (r *ElasticIPReconciler) attachVM(ctx context.Context, eip v1alpha1.ElasticIP, cfg Config, meta fabric.Meta, address string) (outcome, error) {
	var vm v1alpha1.VirtualMachine
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: eip.Namespace, Name: eip.Spec.Target.Name}, &vm)
	if apierrors.IsNotFound(err) {
		return r.refuse(ctx, eip, address, ReasonTargetNotFound, fmt.Sprintf("the VirtualMachine %q does not exist", eip.Spec.Target.Name))
	}
	if err != nil {
		return outcome{}, err
	}
	nic, found := pickInterface(vm, *eip.Spec.Target)
	if !found {
		return r.refuse(ctx, eip, address, ReasonTargetMismatch, fmt.Sprintf("the VirtualMachine %q has no interface on the Subnet %q", vm.Name, eip.Spec.Target.SubnetRef))
	}
	if nic.ElasticIPRef != "" && nic.ElasticIPRef != eip.Name {
		return r.refuse(ctx, eip, address, ReasonTargetMismatch, fmt.Sprintf("the interface of the VirtualMachine %q names the ElasticIP %q", vm.Name, nic.ElasticIPRef))
	}
	if taken, err := r.interfaceTaken(ctx, eip, vm, nic); err != nil || taken {
		if err != nil {
			return outcome{}, err
		}
		return r.refuse(ctx, eip, address, ReasonTargetInUse, "another ElasticIP uses this interface")
	}
	path, err := r.externalPath(ctx, eip, cfg, nic)
	if err != nil || path != nil {
		if err != nil {
			return outcome{}, err
		}
		return stopped(eip, address, path), nil
	}
	return r.attachAddress(ctx, eip, meta, address, vm, nic)
}

func (r *ElasticIPReconciler) interfaceTaken(ctx context.Context, eip v1alpha1.ElasticIP, vm v1alpha1.VirtualMachine, nic v1alpha1.VMNetwork) (bool, error) {
	var all v1alpha1.ElasticIPList
	if err := r.Client.List(ctx, &all, client.InNamespace(eip.Namespace)); err != nil {
		return false, err
	}
	for _, other := range all.Items {
		target := other.Spec.Target
		if other.UID == eip.UID || target == nil || target.Kind != v1alpha1.ElasticIPTargetVirtualMachine || target.Name != vm.Name || !Older(&other, &eip) {
			continue
		}
		if otherNic, found := pickInterface(vm, *target); found && otherNic.SubnetRef == nic.SubnetRef {
			return true, nil
		}
	}
	return false, nil
}

func (r *ElasticIPReconciler) externalPath(ctx context.Context, eip v1alpha1.ElasticIP, cfg Config, nic v1alpha1.VMNetwork) (*Violation, error) {
	var subnet v1alpha1.Subnet
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: eip.Namespace, Name: nic.SubnetRef}, &subnet)
	if apierrors.IsNotFound(err) {
		return &Violation{Reason: ReasonTargetNotFound, Message: fmt.Sprintf("the Subnet %q does not exist", nic.SubnetRef)}, nil
	}
	if err != nil {
		return nil, err
	}
	var network v1alpha1.Network
	err = r.Client.Get(ctx, client.ObjectKey{Name: subnet.Spec.NetworkRef}, &network)
	if apierrors.IsNotFound(err) || (err == nil && !v1alpha1.IsConditionTrue(network.Status.Conditions, v1alpha1.ConditionReady)) {
		return notConnected(fmt.Sprintf("the Network %q is not ready", subnet.Spec.NetworkRef)), nil
	}
	if err != nil {
		return nil, err
	}
	in, err := r.pathInput(ctx, eip, cfg, network)
	if err != nil {
		return nil, err
	}
	if v := ExternalPath(in); v != nil {
		return v, nil
	}
	return frrAgentGap(ctx, r.Client, cfg)
}

func (r *ElasticIPReconciler) pathInput(ctx context.Context, eip v1alpha1.ElasticIP, cfg Config, network v1alpha1.Network) (ExternalPathInput, error) {
	in := ExternalPathInput{Mode: cfg.EIPMode, NetworkExternal: network.Spec.EnableExternal, ProviderNetwork: PoolOf(eip), Transit: cfg.BGP.Transit}
	vpc, err := getObject(ctx, r.Client, fabric.VpcGVK, VpcName(network.Name))
	if apierrors.IsNotFound(err) {
		return in, nil
	}
	if err != nil {
		return in, err
	}
	routing, _, _ := unstructured.NestedBool(vpc.Object, "spec", "dynamicRouting", "enabled")
	in.Vpc = ObservedVpc{Standby: fabric.VpcStandby(vpc), ExternalSubnets: fabric.VpcExternalSubnets(vpc), DynamicRouting: routing}
	port, err := getObject(ctx, r.Client, fabric.OvnEipGVK, vpc.GetName()+"-"+RouterPortSubnet(cfg.EIPMode, PoolOf(eip), cfg.BGP.Transit))
	if err == nil {
		in.RouterPort = ObservedPort{Exists: true, Ready: fabric.EipReady(port), Address: fabric.EipAddress(port)}
	}
	return in, client.IgnoreNotFound(err)
}

func (r *ElasticIPReconciler) attachAddress(ctx context.Context, eip v1alpha1.ElasticIP, meta fabric.Meta, address string, vm v1alpha1.VirtualMachine, nic v1alpha1.VMNetwork) (outcome, error) {
	ips, err := listObjects(ctx, r.Client, fabric.IPGVK)
	if err != nil {
		return outcome{}, err
	}
	ipName, found := FindIP(ips, eip.Namespace, vm.Name, FabricName(eip.Namespace, nic.SubnetRef))
	if !found {
		return stopped(eip, address, &Violation{Reason: ReasonWaitingForAddress, Message: fmt.Sprintf("the VirtualMachine %q has no address yet", vm.Name)}), nil
	}
	existing, err := getObject(ctx, r.Client, fabric.OvnFipGVK, meta.Name)
	if err == nil && fabric.FipIPName(existing) != ipName {
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, existing)); err != nil {
			return outcome{}, err
		}
		return outcome{condition: falseCondition(v1alpha1.ConditionReady, eip.Generation, ReasonFabricNotReady, "moving the OvnFip to the new target"), address: address, requeue: moveRequeue}, nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return outcome{}, err
	}
	if err := ssa.Apply(ctx, r.Client, fabric.OvnFip(meta, meta.Name, ipName), v1alpha1.NetworkFieldManager); err != nil {
		return outcome{}, err
	}
	fip, err := getObject(ctx, r.Client, fabric.OvnFipGVK, meta.Name)
	if err != nil {
		return outcome{}, err
	}
	if !fabric.FipReady(fip) {
		return outcome{condition: falseCondition(v1alpha1.ConditionReady, eip.Generation, ReasonFabricNotReady, "waiting for Fabric to program the OvnFip"), address: address, requeue: waitRequeue}, nil
	}
	return outcome{condition: trueCondition(v1alpha1.ConditionReady, eip.Generation, ReasonAttached), address: address}, nil
}

func (r *ElasticIPReconciler) finalize(ctx context.Context, eip *v1alpha1.ElasticIP) (reconcile.Result, error) {
	name := FabricName(eip.Namespace, eip.Name)
	for _, gvk := range []schema.GroupVersionKind{fabric.OvnFipGVK, fabric.OvnEipGVK} {
		object, err := getObject(ctx, r.Client, gvk, name)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return reconcile.Result{}, err
		}
		if !ownedBy(object, eip) {
			continue
		}
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, object)); err != nil {
			return reconcile.Result{}, err
		}
		if present, err := exists(ctx, r.Client, gvk, name); err != nil || present {
			return reconcile.Result{RequeueAfter: waitRequeue}, err
		}
	}
	return reconcile.Result{}, releaseFinalizer(ctx, r.Client, eip)
}
