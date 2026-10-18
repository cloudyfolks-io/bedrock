package network

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabrictest"
)

type externalFixture struct {
	c         client.Client
	tenant    string
	network   v1alpha1.Network
	namespace string
}

func buildFixture(t *testing.T, network *v1alpha1.Network, afterStandby func(*testing.T, client.Client, string)) externalFixture {
	t.Helper()
	c := cleanEnv(t)
	publicSettings(t)
	tenant := network.Spec.TenantRef
	requireAccepted(t, c, network)
	reconcileNetwork(t, c, network.Name)
	if err := fabrictest.MarkVpcStandby(context.Background(), c, network.Name); err != nil {
		t.Fatal(err)
	}
	afterStandby(t, c, network.Name)
	reconcileNetwork(t, c, network.Name)
	f := externalFixture{c: c, tenant: tenant, network: *network, namespace: newNamespace(t, tenant, "prod")}
	requireAccepted(t, c, policySubnet(f.namespace, "default", network.Name, "10.100.1.0/24"))
	return f
}

func attachRouterPort(t *testing.T, c client.Client, vpc string) {
	t.Helper()
	if err := fabrictest.CreateRouterPort(context.Background(), c, vpc, "provider-public", "91.246.31.11"); err != nil {
		t.Fatal(err)
	}
}

func withoutRouterPort(*testing.T, client.Client, string) {}

func externalNetwork(t *testing.T) *v1alpha1.Network {
	t.Helper()
	network := networkOf(unique(t, "net"), unique(t, "acme"), "10.100.0.0/16")
	network.Spec.EnableExternal = true
	return network
}

func newExternalFixture(t *testing.T) externalFixture {
	t.Helper()
	return buildFixture(t, externalNetwork(t), attachRouterPort)
}

func newDisconnectedFixture(t *testing.T) externalFixture {
	t.Helper()
	return buildFixture(t, externalNetwork(t), withoutRouterPort)
}

func newPrivateFixture(t *testing.T) externalFixture {
	t.Helper()
	return buildFixture(t, networkOf(unique(t, "net"), unique(t, "acme"), "10.100.0.0/16"), withoutRouterPort)
}

func (f externalFixture) addVM(t *testing.T, name, backReference, address string) {
	t.Helper()
	vm := virtualMachineUsing(f.namespace, name)
	vm.Spec.Networks[0].ElasticIPRef = backReference
	requireAccepted(t, f.c, vm)
	if err := fabrictest.CreateIP(context.Background(), f.c, name+"."+f.namespace+".default", f.namespace, name, FabricName(f.namespace, "default"), address); err != nil {
		t.Fatal(err)
	}
}

func (f externalFixture) createEIP(t *testing.T, name, address string, target *v1alpha1.ElasticIPTarget) *v1alpha1.ElasticIP {
	t.Helper()
	eip := elasticIPOf(f.namespace, name, address)
	eip.Spec.Target = target
	requireAccepted(t, f.c, eip)
	return eip
}

func vmTarget(name string) *v1alpha1.ElasticIPTarget {
	return &v1alpha1.ElasticIPTarget{Kind: v1alpha1.ElasticIPTargetVirtualMachine, Name: name}
}

func reconcileElasticIP(t *testing.T, c client.Client, namespace, name string) reconcile.Result {
	t.Helper()
	result, err := (&ElasticIPReconciler{Client: c}).Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func getElasticIP(t *testing.T, c client.Client, namespace, name string) v1alpha1.ElasticIP {
	t.Helper()
	var eip v1alpha1.ElasticIP
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &eip); err != nil {
		t.Fatal(err)
	}
	return eip
}

func requireEIP(t *testing.T, c client.Client, namespace, name string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	requireCondition(t, getElasticIP(t, c, namespace, name).Status.Conditions, v1alpha1.ConditionReady, status, reason)
}

func eipObject(c client.Client, namespace, name string) (*unstructured.Unstructured, error) {
	return getObject(context.Background(), c, fabric.OvnEipGVK, FabricName(namespace, name))
}

func fipObject(c client.Client, namespace, name string) (*unstructured.Unstructured, error) {
	return getObject(context.Background(), c, fabric.OvnFipGVK, FabricName(namespace, name))
}

func requireNoFip(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	if _, err := fipObject(c, namespace, name); !apierrors.IsNotFound(err) {
		t.Fatalf("no OvnFip expected: %v", err)
	}
}

func allocate(t *testing.T, f externalFixture, name, address string) {
	t.Helper()
	if err := fabrictest.MarkEipAllocated(context.Background(), f.c, FabricName(f.namespace, name), address); err != nil {
		t.Fatal(err)
	}
}

func TestElasticIPReserveAllocatesAnAddress(t *testing.T) {
	f := newExternalFixture(t)
	f.createEIP(t, "ip1", "", nil)
	if result := reconcileElasticIP(t, f.c, f.namespace, "ip1"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonAllocating)
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionTrue, ReasonReserved)
	if got := getElasticIP(t, f.c, f.namespace, "ip1").Status.Address; got != "91.246.31.20" {
		t.Fatalf("address %q", got)
	}
	eip, _ := eipObject(f.c, f.namespace, "ip1")
	if subnet, _, _ := unstructured.NestedString(eip.Object, "spec", "externalSubnet"); subnet != "provider-public" {
		t.Fatalf("external subnet %q", subnet)
	}
}

func TestElasticIPRequestedAddressIsPassedToFabric(t *testing.T) {
	f := newExternalFixture(t)
	f.createEIP(t, "ip1", "91.246.31.50", nil)
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	eip, err := eipObject(f.c, f.namespace, "ip1")
	if err != nil {
		t.Fatal(err)
	}
	if address, _, _ := unstructured.NestedString(eip.Object, "spec", "v4Ip"); address != "91.246.31.50" {
		t.Fatalf("address %q", address)
	}
}

func TestElasticIPReportsAnAddressThatFabricRefuses(t *testing.T) {
	f := newExternalFixture(t)
	f.createEIP(t, "ip1", "91.246.31.50", nil)
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	if err := fabrictest.MarkEipRefused(context.Background(), f.c, FabricName(f.namespace, "ip1"), "address 91.246.31.50 is used by tenant x"); err != nil {
		t.Fatal(err)
	}
	if result := reconcileElasticIP(t, f.c, f.namespace, "ip1"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonFabricNotReady)
	got := meta.FindStatusCondition(getElasticIP(t, f.c, f.namespace, "ip1").Status.Conditions, v1alpha1.ConditionReady)
	if got.Message != "Fabric refused the request for this address" {
		t.Fatalf("the Fabric text must not reach the tenant: %q", got.Message)
	}
}

func TestElasticIPUnknownProviderNetwork(t *testing.T) {
	f := newExternalFixture(t)
	eip := elasticIPOf(f.namespace, "ip1", "")
	eip.Spec.ProviderNetwork = "ghost"
	requireAccepted(t, f.c, eip)
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonUnknownProviderNetwork)
	if _, err := eipObject(f.c, f.namespace, "ip1"); !apierrors.IsNotFound(err) {
		t.Fatalf("nothing must be applied: %v", err)
	}
}

func TestElasticIPRejectsAddressOutsideTheProviderNetwork(t *testing.T) {
	f := newExternalFixture(t)
	f.createEIP(t, "ip1", "198.51.100.5", nil)
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonAddressOutsideProviderNetwork)
}

func TestElasticIPRejectsTheGatewayAddress(t *testing.T) {
	f := newExternalFixture(t)
	f.createEIP(t, "ip1", "91.246.31.1", nil)
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonAddressReserved)
}

func TestElasticIPDuplicateAddressKeepsTheOlder(t *testing.T) {
	f := newExternalFixture(t)
	second := newNamespace(t, f.tenant, "zz")
	f.createEIP(t, "ip1", "91.246.31.50", nil)
	eip := elasticIPOf(second, "ip2", "91.246.31.50")
	requireAccepted(t, f.c, eip)
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	reconcileElasticIP(t, f.c, second, "ip2")
	requireEIP(t, f.c, second, "ip2", metav1.ConditionFalse, ReasonAddressInUse)
	younger := getElasticIP(t, f.c, second, "ip2")
	if message := meta.FindStatusCondition(younger.Status.Conditions, v1alpha1.ConditionReady).Message; message != "the address 91.246.31.50 is already assigned" {
		t.Fatalf("message %q", message)
	}
	if _, err := eipObject(f.c, second, "ip2"); !apierrors.IsNotFound(err) {
		t.Fatalf("the younger ElasticIP must not reach Fabric: %v", err)
	}
	if _, err := eipObject(f.c, f.namespace, "ip1"); err != nil {
		t.Fatal(err)
	}
}

func TestElasticIPAttachCreatesFipOnTheVMAddress(t *testing.T) {
	f := newExternalFixture(t)
	f.addVM(t, "vm1", "", "10.100.1.5")
	f.createEIP(t, "ip1", "", vmTarget("vm1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	fip, err := fipObject(f.c, f.namespace, "ip1")
	if err != nil {
		t.Fatal(err)
	}
	if ipName := fabric.FipIPName(fip); ipName != "vm1."+f.namespace+".default" {
		t.Fatalf("ipName %q", ipName)
	}
	if eip, _, _ := unstructured.NestedString(fip.Object, "spec", "ovnEip"); eip != FabricName(f.namespace, "ip1") {
		t.Fatalf("ovnEip %q", eip)
	}
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonFabricNotReady)
	if err := fabrictest.MarkFipReady(context.Background(), f.c, fip.GetName()); err != nil {
		t.Fatal(err)
	}
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionTrue, ReasonAttached)
}

func TestElasticIPWaitsForTheVMAddress(t *testing.T) {
	f := newExternalFixture(t)
	requireAccepted(t, f.c, virtualMachineUsing(f.namespace, "vm1"))
	f.createEIP(t, "ip1", "", vmTarget("vm1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	if result := reconcileElasticIP(t, f.c, f.namespace, "ip1"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonWaitingForAddress)
	requireNoFip(t, f.c, f.namespace, "ip1")
}

func TestElasticIPFollowsATargetChange(t *testing.T) {
	f := newExternalFixture(t)
	f.addVM(t, "a", "", "10.100.1.5")
	f.addVM(t, "b", "", "10.100.1.6")
	eip := f.createEIP(t, "ip1", "", vmTarget("a"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	if fip, err := fipObject(f.c, f.namespace, "ip1"); err != nil || fabric.FipIPName(fip) != "a."+f.namespace+".default" {
		t.Fatalf("first OvnFip: %v", err)
	}
	current := getElasticIP(t, f.c, f.namespace, eip.Name)
	current.Spec.Target = vmTarget("b")
	if err := f.c.Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	if result := reconcileElasticIP(t, f.c, f.namespace, "ip1"); result.RequeueAfter == 0 {
		t.Fatalf("a replaced OvnFip needs a requeue: %+v", result)
	}
	requireNoFip(t, f.c, f.namespace, "ip1")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	if fip, err := fipObject(f.c, f.namespace, "ip1"); err != nil || fabric.FipIPName(fip) != "b."+f.namespace+".default" {
		t.Fatalf("second OvnFip: %v", err)
	}
}

func TestElasticIPDetachKeepsTheAddress(t *testing.T) {
	f := newExternalFixture(t)
	f.addVM(t, "vm1", "", "10.100.1.5")
	f.createEIP(t, "ip1", "", vmTarget("vm1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	current := getElasticIP(t, f.c, f.namespace, "ip1")
	current.Spec.Target = nil
	if err := f.c.Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireNoFip(t, f.c, f.namespace, "ip1")
	if _, err := eipObject(f.c, f.namespace, "ip1"); err != nil {
		t.Fatal("the OvnEip must stay")
	}
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionTrue, ReasonReserved)
	if got := getElasticIP(t, f.c, f.namespace, "ip1").Status.Address; got != "91.246.31.20" {
		t.Fatalf("address %q", got)
	}
}

func TestElasticIPTargetVMDeletedReleasesTheFip(t *testing.T) {
	f := newExternalFixture(t)
	f.addVM(t, "vm1", "", "10.100.1.5")
	f.createEIP(t, "ip1", "", vmTarget("vm1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	if err := f.c.Delete(context.Background(), virtualMachineUsing(f.namespace, "vm1")); err != nil {
		t.Fatal(err)
	}
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonTargetNotFound)
	requireNoFip(t, f.c, f.namespace, "ip1")
	if got := getElasticIP(t, f.c, f.namespace, "ip1").Status.Address; got != "91.246.31.20" {
		t.Fatalf("the address must be kept: %q", got)
	}
}

func TestElasticIPVMBackReferenceMismatch(t *testing.T) {
	f := newExternalFixture(t)
	f.addVM(t, "vm1", "other", "10.100.1.5")
	f.createEIP(t, "ip1", "", vmTarget("vm1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	before := getVM(t, f.c, f.namespace, "vm1").ResourceVersion
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonTargetMismatch)
	requireNoFip(t, f.c, f.namespace, "ip1")
	if getVM(t, f.c, f.namespace, "vm1").ResourceVersion != before {
		t.Fatal("the VM must never be written")
	}
}

func getVM(t *testing.T, c client.Client, namespace, name string) v1alpha1.VirtualMachine {
	t.Helper()
	var vm v1alpha1.VirtualMachine
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &vm); err != nil {
		t.Fatal(err)
	}
	return vm
}

func TestElasticIPVMEmptyBackReferenceIsFine(t *testing.T) {
	f := newExternalFixture(t)
	f.addVM(t, "vm1", "", "10.100.1.5")
	f.createEIP(t, "ip1", "", vmTarget("vm1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	if _, err := fipObject(f.c, f.namespace, "ip1"); err != nil {
		t.Fatal(err)
	}
}

func TestElasticIPVMBackReferenceToItselfIsFine(t *testing.T) {
	f := newExternalFixture(t)
	f.addVM(t, "vm1", "ip1", "10.100.1.5")
	f.createEIP(t, "ip1", "", vmTarget("vm1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	if _, err := fipObject(f.c, f.namespace, "ip1"); err != nil {
		t.Fatal(err)
	}
}

func TestElasticIPRefusesASecondElasticIPOnTheSameInterface(t *testing.T) {
	f := newExternalFixture(t)
	f.addVM(t, "vm1", "", "10.100.1.5")
	f.createEIP(t, "ip1", "", vmTarget("vm1"))
	f.createEIP(t, "ip2", "", vmTarget("vm1"))
	for _, name := range []string{"ip1", "ip2"} {
		reconcileElasticIP(t, f.c, f.namespace, name)
		allocate(t, f, name, map[string]string{"ip1": "91.246.31.20", "ip2": "91.246.31.21"}[name])
		reconcileElasticIP(t, f.c, f.namespace, name)
	}
	if _, err := fipObject(f.c, f.namespace, "ip1"); err != nil {
		t.Fatalf("the older ElasticIP keeps the interface: %v", err)
	}
	requireEIP(t, f.c, f.namespace, "ip2", metav1.ConditionFalse, ReasonTargetInUse)
	requireNoFip(t, f.c, f.namespace, "ip2")
}

func TestElasticIPNetworkNotExternal(t *testing.T) {
	f := newPrivateFixture(t)
	f.addVM(t, "vm1", "", "10.100.1.5")
	f.createEIP(t, "ip1", "", vmTarget("vm1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonNetworkNotExternal)
	requireNoFip(t, f.c, f.namespace, "ip1")
}

func TestElasticIPExternalNotConnected(t *testing.T) {
	f := newDisconnectedFixture(t)
	f.addVM(t, "vm1", "", "10.100.1.5")
	f.createEIP(t, "ip1", "", vmTarget("vm1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonExternalNotConnected)
	requireNoFip(t, f.c, f.namespace, "ip1")
}

func newBGPFixture(t *testing.T) externalFixture {
	t.Helper()
	c := cleanEnv(t)
	bgpSettings(t)
	tenant := unique(t, "acme")
	network := networkOf(unique(t, "net"), tenant, "10.100.0.0/16")
	network.Spec.EnableExternal = true
	requireAccepted(t, c, network)
	reconcileNetwork(t, c, network.Name)
	ctx := context.Background()
	if err := fabrictest.MarkVpcStandby(ctx, c, network.Name); err != nil {
		t.Fatal(err)
	}
	if err := fabrictest.CreateRouterPort(ctx, c, network.Name, "provider-transit", "192.0.2.11"); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, network.Name)
	f := externalFixture{c: c, tenant: tenant, network: *network, namespace: newNamespace(t, tenant, "prod")}
	requireAccepted(t, c, policySubnet(f.namespace, "default", network.Name, "10.100.1.0/24"))
	f.addVM(t, "vm1", "", "10.100.1.5")
	return f
}

func (f externalFixture) createPoolEIP(t *testing.T, name, providerNetwork string) {
	t.Helper()
	eip := elasticIPOf(f.namespace, name, "")
	eip.Spec.ProviderNetwork = providerNetwork
	eip.Spec.Target = vmTarget("vm1")
	requireAccepted(t, f.c, eip)
}

func TestElasticIPBGPNeedsTheFRRAgent(t *testing.T) {
	f := newBGPFixture(t)
	f.createPoolEIP(t, "ip1", "pool")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "198.51.100.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonFRRAgentMissing)
	requireNoFip(t, f.c, f.namespace, "ip1")
	installFRRAgent(t, f.c, 0)
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonFRRAgentMissing)
	setFRRAgentReady(t, f.c, 1)
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	if _, err := fipObject(f.c, f.namespace, "ip1"); err != nil {
		t.Fatalf("with the agent the reconcile continues: %v", err)
	}
}

func TestElasticIPBGPRejectsTransitNetwork(t *testing.T) {
	f := newBGPFixture(t)
	f.createPoolEIP(t, "ip1", "transit")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "192.0.2.50")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonUnsupported)
}

func loadBalancerFor(namespace, name, eip string) *v1alpha1.LoadBalancer {
	return &v1alpha1.LoadBalancer{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.LoadBalancerSpec{ElasticIPRef: eip, Listeners: []v1alpha1.LoadBalancerListener{{Port: 80, Protocol: v1alpha1.LoadBalancerProtocolTCP}}},
	}
}

func lbTarget(name string) *v1alpha1.ElasticIPTarget {
	return &v1alpha1.ElasticIPTarget{Kind: v1alpha1.ElasticIPTargetLoadBalancer, Name: name}
}

func TestElasticIPLoadBalancerTarget(t *testing.T) {
	f := newExternalFixture(t)
	requireAccepted(t, f.c, loadBalancerFor(f.namespace, "lb1", "ip1"))
	f.createEIP(t, "ip1", "", lbTarget("lb1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireNoFip(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionTrue, ReasonAttached)
}

func TestElasticIPLoadBalancerTargetMismatch(t *testing.T) {
	f := newExternalFixture(t)
	requireAccepted(t, f.c, loadBalancerFor(f.namespace, "lb1", "someone-else"))
	f.createEIP(t, "ip1", "", lbTarget("lb1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonTargetMismatch)
}

func TestElasticIPLoadBalancerTargetMissing(t *testing.T) {
	f := newExternalFixture(t)
	f.createEIP(t, "ip1", "", lbTarget("ghost"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonTargetNotFound)
}

func TestElasticIPReportsFabricConflict(t *testing.T) {
	f := newExternalFixture(t)
	f.createEIP(t, "ip1", "", nil)
	foreign := fabric.OvnEip(fabric.Meta{Name: FabricName(f.namespace, "ip1"), Annotations: map[string]string{AnnotationOwnerName: "x", AnnotationOwnerUID: "foreign"}}, "provider-public", "")
	if err := f.c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonFabricConflict)
}

func TestElasticIPRequiresLabeledNamespace(t *testing.T) {
	c := cleanEnv(t)
	publicSettings(t)
	bare := unique(t, "bare")
	requireAccepted(t, c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bare}})
	requireAccepted(t, c, elasticIPOf(bare, "ip1", ""))
	reconcileElasticIP(t, c, bare, "ip1")
	requireEIP(t, c, bare, "ip1", metav1.ConditionFalse, ReasonNamespaceNotLabeled)
}

func TestElasticIPDeletionOrdersFipBeforeEip(t *testing.T) {
	f := newExternalFixture(t)
	ctx := context.Background()
	f.addVM(t, "vm1", "", "10.100.1.5")
	eip := f.createEIP(t, "ip1", "", vmTarget("vm1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	fip, err := fipObject(f.c, f.namespace, "ip1")
	if err != nil {
		t.Fatal(err)
	}
	fip.SetFinalizers([]string{"fabric.test/hold"})
	if err := f.c.Update(ctx, fip); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(ctx, eip); err != nil {
		t.Fatal(err)
	}
	if result := reconcileElasticIP(t, f.c, f.namespace, "ip1"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	if _, err := eipObject(f.c, f.namespace, "ip1"); err != nil {
		t.Fatal("the OvnEip must stay while the OvnFip exists")
	}
	if err := f.c.Get(ctx, client.ObjectKeyFromObject(eip), &v1alpha1.ElasticIP{}); err != nil {
		t.Fatal("the finalizer must stay")
	}
	held, _ := fipObject(f.c, f.namespace, "ip1")
	held.SetFinalizers(nil)
	if err := f.c.Update(ctx, held); err != nil {
		t.Fatal(err)
	}
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	if _, err := eipObject(f.c, f.namespace, "ip1"); !apierrors.IsNotFound(err) {
		t.Fatalf("the OvnEip must be deleted: %v", err)
	}
	if err := f.c.Get(ctx, client.ObjectKeyFromObject(eip), &v1alpha1.ElasticIP{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the finalizer must be gone: %v", err)
	}
}
