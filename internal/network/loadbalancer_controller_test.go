package network

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabrictest"
)

func knowsEverything(schema.GroupKind) bool { return true }

func reconcileLoadBalancer(t *testing.T, c client.Client, namespace, name string, kinds KindProbe) reconcile.Result {
	t.Helper()
	result, err := (&LoadBalancerReconciler{Client: c, Kinds: kinds}).Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireLB(t *testing.T, c client.Client, namespace, name string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	var lb v1alpha1.LoadBalancer
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &lb); err != nil {
		t.Fatal(err)
	}
	requireCondition(t, lb.Status.Conditions, v1alpha1.ConditionReady, status, reason)
}

func addressOf(t *testing.T, c client.Client, namespace, name string) string {
	t.Helper()
	var lb v1alpha1.LoadBalancer
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &lb); err != nil {
		t.Fatal(err)
	}
	return lb.Status.Address
}

func rulesOf(t *testing.T, c client.Client, name string) []*unstructured.Unstructured {
	t.Helper()
	rules, err := listObjects(context.Background(), c, fabric.RouterLBRuleGVK, client.MatchingLabels{v1alpha1.LabelKind: "LoadBalancer", v1alpha1.LabelName: name})
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func endpointsOf(rule *unstructured.Unstructured) []string {
	endpoints, _, _ := unstructured.NestedStringSlice(rule.Object, "spec", "endpoints")
	return endpoints
}

func newLBFixture(t *testing.T) externalFixture {
	t.Helper()
	f := newExternalFixture(t)
	f.addVM(t, "vm1", "", "10.100.1.5")
	f.addVM(t, "vm2", "", "10.100.1.6")
	lb := loadBalancerFor(f.namespace, "lb1", "ip1")
	lb.Spec.Listeners = []v1alpha1.LoadBalancerListener{
		{Port: 80, Protocol: "TCP", Targets: []v1alpha1.LoadBalancerTarget{{VMRef: "vm1", Port: 8080}, {VMRef: "vm2", Port: 8080}}},
		{Port: 53, Protocol: "UDP", Targets: []v1alpha1.LoadBalancerTarget{{VMRef: "vm1", Port: 5353}}},
	}
	requireAccepted(t, f.c, lb)
	f.createEIP(t, "ip1", "", lbTarget("lb1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	return f
}

func markRulesProgrammed(t *testing.T, c client.Client, name string) {
	t.Helper()
	for _, rule := range rulesOf(t, c, name) {
		if err := fabrictest.MarkRouterLBRuleProgrammed(context.Background(), c, rule.GetName(), "rlr-"+rule.GetName()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadBalancerProgramsRouterRules(t *testing.T) {
	f := newLBFixture(t)
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	rules := rulesOf(t, f.c, "lb1")
	if len(rules) != 2 {
		t.Fatalf("rules %d", len(rules))
	}
	byName := map[string]*unstructured.Unstructured{}
	for _, r := range rules {
		byName[r.GetName()] = r
	}
	tcp, udp := byName[ListenerRuleName(f.namespace, "lb1", 80, "TCP")], byName[ListenerRuleName(f.namespace, "lb1", 53, "UDP")]
	if tcp == nil || udp == nil || !slices.Equal(endpointsOf(tcp), []string{"10.100.1.5", "10.100.1.6"}) || !slices.Equal(endpointsOf(udp), []string{"10.100.1.5"}) {
		t.Fatalf("rules %v", byName)
	}
	if eip, _, _ := unstructured.NestedString(tcp.Object, "spec", "ovnEip"); eip != FabricName(f.namespace, "ip1") {
		t.Fatalf("ovnEip %q", eip)
	}
	if vpc, _, _ := unstructured.NestedString(tcp.Object, "spec", "vpc"); vpc != f.network.Name {
		t.Fatalf("vpc %q", vpc)
	}
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonFabricNotReady)
	if got := addressOf(t, f.c, f.namespace, "lb1"); got != "91.246.31.20" {
		t.Fatalf("status address %q", got)
	}
	markRulesProgrammed(t, f.c, "lb1")
	if result := reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything); result.RequeueAfter != 0 {
		t.Fatalf("result %+v", result)
	}
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionTrue, ReasonProgrammed)
}

func TestLoadBalancerTreatsADeletingElasticIPAsGone(t *testing.T) {
	f := newLBFixture(t)
	ctx := context.Background()
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if len(rulesOf(t, f.c, "lb1")) != 2 {
		t.Fatal("the rules must exist before the ElasticIP goes")
	}
	var eip v1alpha1.ElasticIP
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: "ip1"}, &eip); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(ctx, &eip); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Get(ctx, client.ObjectKeyFromObject(&eip), &eip); err != nil || eip.DeletionTimestamp.IsZero() {
		t.Fatalf("the ElasticIP must wait for its finalizer: %v", err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonElasticIPNotFound)
	if len(rulesOf(t, f.c, "lb1")) != 0 {
		t.Fatal("the rules must release the OvnEip")
	}
	if got := addressOf(t, f.c, f.namespace, "lb1"); got != "" {
		t.Fatalf("the address must go with the ElasticIP, got %q", got)
	}
}

func TestLoadBalancerRequiresAnExistingElasticIP(t *testing.T) {
	f := newExternalFixture(t)
	requireAccepted(t, f.c, loadBalancerFor(f.namespace, "lb1", "ghost"))
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonElasticIPNotFound)
}

func newInternalFixture(t *testing.T) externalFixture {
	t.Helper()
	f := newPrivateFixture(t)
	f.addVM(t, "vm1", "", "10.100.1.5")
	f.addVM(t, "vm2", "", "10.100.1.6")
	lb := loadBalancerFor(f.namespace, "lb1", "")
	lb.Spec.Listeners = []v1alpha1.LoadBalancerListener{{Port: 80, Protocol: "TCP", Targets: []v1alpha1.LoadBalancerTarget{{VMRef: "vm1", Port: 8080}, {VMRef: "vm2", Port: 8080}}}}
	requireAccepted(t, f.c, lb)
	return f
}

func switchRulesOf(t *testing.T, c client.Client, name string) []*unstructured.Unstructured {
	t.Helper()
	rules, err := listObjects(context.Background(), c, fabric.SwitchLBRuleGVK, client.MatchingLabels{v1alpha1.LabelKind: "LoadBalancer", v1alpha1.LabelName: name})
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func TestLoadBalancerWithoutElasticIPRefProgramsAnInternalVip(t *testing.T) {
	f := newInternalFixture(t)
	ctx := context.Background()
	if result := reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonFabricNotReady)
	if got := addressOf(t, f.c, f.namespace, "lb1"); got != "" {
		t.Fatalf("no address before the Vip has one, got %q", got)
	}
	vip, err := getObject(ctx, f.c, fabric.VipGVK, InternalVipName(f.namespace, "lb1"))
	if err != nil {
		t.Fatal(err)
	}
	if subnet, _, _ := unstructured.NestedString(vip.Object, "spec", "subnet"); subnet != LBSubnetName(f.network.Name) {
		t.Fatalf("subnet %q", subnet)
	}
	if kind, found, _ := unstructured.NestedString(vip.Object, "spec", "type"); found {
		t.Fatalf("the Vip must be a plain Vip, got type %q", kind)
	}
	if len(switchRulesOf(t, f.c, "lb1")) != 0 || len(rulesOf(t, f.c, "lb1")) != 0 {
		t.Fatal("no rule before the virtual address exists")
	}
	if err := fabrictest.MarkVipAllocated(ctx, f.c, vip.GetName(), "100.69.0.2"); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	rules := switchRulesOf(t, f.c, "lb1")
	if len(rules) != 1 || !slices.Equal(endpointsOf(rules[0]), []string{"10.100.1.5", "10.100.1.6"}) {
		t.Fatalf("rules %v", rules)
	}
	if address, _, _ := unstructured.NestedString(rules[0].Object, "spec", "vip"); address != "100.69.0.2" {
		t.Fatalf("vip %q", address)
	}
	if got := addressOf(t, f.c, f.namespace, "lb1"); got != "100.69.0.2" {
		t.Fatalf("status address %q", got)
	}
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonFabricNotReady)
	if err := fabrictest.MarkSwitchLBRuleProgrammed(ctx, f.c, rules[0].GetName(), "ns/slr"); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionTrue, ReasonProgrammed)
}

func vipsOf(t *testing.T, c client.Client) []*unstructured.Unstructured {
	t.Helper()
	vips, err := listObjects(context.Background(), c, fabric.VipGVK)
	if err != nil {
		t.Fatal(err)
	}
	return vips
}

func TestLoadBalancerInternalNeverWritesASwitchLBVip(t *testing.T) {
	f := newInternalFixture(t)
	ctx := context.Background()
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if err := fabrictest.MarkVipAllocated(ctx, f.c, InternalVipName(f.namespace, "lb1"), "100.69.0.2"); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	vips := vipsOf(t, f.c)
	if len(vips) != 1 {
		t.Fatalf("vips %d", len(vips))
	}
	if kind, _, _ := unstructured.NestedString(vips[0].Object, "spec", "type"); kind == "switch_lb_vip" {
		t.Fatal("switch_lb_vip must never be written")
	}
}

func TestLoadBalancerInternalClearsTheAddressWhenTheVipGoes(t *testing.T) {
	f := newInternalFixture(t)
	ctx := context.Background()
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if err := fabrictest.MarkVipAllocated(ctx, f.c, InternalVipName(f.namespace, "lb1"), "100.69.0.2"); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if got := addressOf(t, f.c, f.namespace, "lb1"); got != "100.69.0.2" {
		t.Fatalf("status address %q", got)
	}
	if err := deleteObject(ctx, f.c, fabric.VipGVK, InternalVipName(f.namespace, "lb1")); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if got := addressOf(t, f.c, f.namespace, "lb1"); got != "" {
		t.Fatalf("the address must go with the Vip, got %q", got)
	}
}

func TestLoadBalancerInternalWaitsForThePlaneSubnet(t *testing.T) {
	f := newInternalFixture(t)
	ctx := context.Background()
	if err := deleteObject(ctx, f.c, fabric.SubnetGVK, LBSubnetName(f.network.Name)); err != nil {
		t.Fatal(err)
	}
	if result := reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonFabricNotReady)
	if len(vipsOf(t, f.c)) != 0 {
		t.Fatal("no Vip before the plane Subnet exists")
	}
}

func TestLoadBalancerInternalMovesAnOldVipToThePlane(t *testing.T) {
	f := newInternalFixture(t)
	ctx := context.Background()
	var lb v1alpha1.LoadBalancer
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: "lb1"}, &lb); err != nil {
		t.Fatal(err)
	}
	oldName := FabricName(f.namespace, "lb1")
	old := fabric.Vip(fabric.Meta{Name: oldName, Labels: ObjectLabels("LoadBalancer", "lb1", f.tenant, "prod"), Annotations: OwnerAnnotations(&lb)}, f.namespace, FabricName(f.namespace, "default"))
	if err := unstructured.SetNestedField(old.Object, "switch_lb_vip", "spec", "type"); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if err := fabrictest.MarkVipAllocated(ctx, f.c, InternalVipName(f.namespace, "lb1"), "100.69.0.2"); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if _, err := getObject(ctx, f.c, fabric.VipGVK, oldName); !apierrors.IsNotFound(err) {
		t.Fatalf("the old Vip must be deleted: %v", err)
	}
	vips := vipsOf(t, f.c)
	if len(vips) != 1 || vips[0].GetName() != InternalVipName(f.namespace, "lb1") {
		t.Fatalf("vips %v", vips)
	}
	rules := switchRulesOf(t, f.c, "lb1")
	if address, _, _ := unstructured.NestedString(rules[0].Object, "spec", "vip"); len(rules) != 1 || address != "100.69.0.2" {
		t.Fatalf("rules %v", rules)
	}
}

func TestLoadBalancerInternalNeedsOneSubnet(t *testing.T) {
	f := newInternalFixture(t)
	ctx := context.Background()
	requireAccepted(t, f.c, policySubnet(f.namespace, "other", f.network.Name, "10.100.2.0/24"))
	vm := virtualMachineUsing(f.namespace, "vm3")
	vm.Spec.Networks[0].SubnetRef = "other"
	requireAccepted(t, f.c, vm)
	if err := fabrictest.CreateIP(ctx, f.c, "vm3."+f.namespace+".other", f.namespace, "vm3", FabricName(f.namespace, "other"), "10.100.2.5"); err != nil {
		t.Fatal(err)
	}
	var lb v1alpha1.LoadBalancer
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: "lb1"}, &lb); err != nil {
		t.Fatal(err)
	}
	lb.Spec.Listeners[0].Targets = append(lb.Spec.Listeners[0].Targets, v1alpha1.LoadBalancerTarget{VMRef: "vm3", Port: 8080})
	if err := f.c.Update(ctx, &lb); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonTargetNetworkMismatch)
}

func TestLoadBalancerInternalDeletionRemovesTheVipAndRules(t *testing.T) {
	f := newInternalFixture(t)
	ctx := context.Background()
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if err := fabrictest.MarkVipAllocated(ctx, f.c, InternalVipName(f.namespace, "lb1"), "100.69.0.2"); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	var lb v1alpha1.LoadBalancer
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: "lb1"}, &lb); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(ctx, &lb); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if len(switchRulesOf(t, f.c, "lb1")) != 0 {
		t.Fatal("the rules must be deleted")
	}
	if _, err := getObject(ctx, f.c, fabric.VipGVK, InternalVipName(f.namespace, "lb1")); !apierrors.IsNotFound(err) {
		t.Fatalf("the Vip must be deleted: %v", err)
	}
	if err := f.c.Get(ctx, client.ObjectKeyFromObject(&lb), &v1alpha1.LoadBalancer{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the finalizer must be gone: %v", err)
	}
}

func TestLoadBalancerInternalNeedsTheSwitchKinds(t *testing.T) {
	f := newInternalFixture(t)
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", func(gk schema.GroupKind) bool { return gk.Kind != "SwitchLBRule" })
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonUnsupported)
}

func TestLoadBalancerRequiresTheElasticIPToTargetIt(t *testing.T) {
	f := newExternalFixture(t)
	requireAccepted(t, f.c, loadBalancerFor(f.namespace, "lb1", "ip1"))
	f.createEIP(t, "ip1", "", nil)
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonTargetMismatch)
}

func TestLoadBalancerWaitsForTheElasticIP(t *testing.T) {
	f := newExternalFixture(t)
	requireAccepted(t, f.c, loadBalancerFor(f.namespace, "lb1", "ip1"))
	f.createEIP(t, "ip1", "", lbTarget("lb1"))
	if result := reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonElasticIPNotReady)
}

func TestLoadBalancerRejectsTargetsOnDifferentNetworks(t *testing.T) {
	f := newLBFixture(t)
	ctx := context.Background()
	second := networkOf(unique(t, "net"), f.tenant, "10.101.0.0/16")
	requireAccepted(t, f.c, second)
	reconcileNetwork(t, f.c, second.Name)
	requireAccepted(t, f.c, policySubnet(f.namespace, "other", second.Name, "10.101.1.0/24"))
	vm := virtualMachineUsing(f.namespace, "vm3")
	vm.Spec.Networks[0].SubnetRef = "other"
	requireAccepted(t, f.c, vm)
	if err := fabrictest.CreateIP(ctx, f.c, "vm3."+f.namespace+".other", f.namespace, "vm3", FabricName(f.namespace, "other"), "10.101.1.5"); err != nil {
		t.Fatal(err)
	}
	var lb v1alpha1.LoadBalancer
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: "lb1"}, &lb); err != nil {
		t.Fatal(err)
	}
	lb.Spec.Listeners[0].Targets = append(lb.Spec.Listeners[0].Targets, v1alpha1.LoadBalancerTarget{VMRef: "vm3", Port: 8080})
	if err := f.c.Update(ctx, &lb); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonTargetNetworkMismatch)
	if len(rulesOf(t, f.c, "lb1")) != 0 {
		t.Fatal("nothing must be applied")
	}
}

func TestLoadBalancerRejectsMissingTargetVM(t *testing.T) {
	f := newLBFixture(t)
	ctx := context.Background()
	var lb v1alpha1.LoadBalancer
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: "lb1"}, &lb); err != nil {
		t.Fatal(err)
	}
	lb.Spec.Listeners[1].Targets = []v1alpha1.LoadBalancerTarget{{VMRef: "ghost", Port: 5353}}
	if err := f.c.Update(ctx, &lb); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonTargetNotFound)
}

func TestLoadBalancerProgramsTheTargetsThatHaveAnAddress(t *testing.T) {
	f := newExternalFixture(t)
	f.addVM(t, "vm1", "", "10.100.1.5")
	requireAccepted(t, f.c, virtualMachineUsing(f.namespace, "vm2"))
	lb := loadBalancerFor(f.namespace, "lb1", "ip1")
	lb.Spec.Listeners[0].Targets = []v1alpha1.LoadBalancerTarget{{VMRef: "vm1", Port: 80}, {VMRef: "vm2", Port: 80}}
	requireAccepted(t, f.c, lb)
	f.createEIP(t, "ip1", "", lbTarget("lb1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	if result := reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonWaitingForAddress)
	rules := rulesOf(t, f.c, "lb1")
	if len(rules) != 1 || !slices.Equal(endpointsOf(rules[0]), []string{"10.100.1.5"}) {
		t.Fatalf("rules %v", rules)
	}
}

func TestLoadBalancerWithoutAnyAddressHasNoTargets(t *testing.T) {
	f := newExternalFixture(t)
	requireAccepted(t, f.c, virtualMachineUsing(f.namespace, "vm1"))
	lb := loadBalancerFor(f.namespace, "lb1", "ip1")
	lb.Spec.Listeners[0].Targets = []v1alpha1.LoadBalancerTarget{{VMRef: "vm1", Port: 80}}
	requireAccepted(t, f.c, lb)
	f.createEIP(t, "ip1", "", lbTarget("lb1"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonNoTargets)
	if len(rulesOf(t, f.c, "lb1")) != 0 {
		t.Fatal("no rule without an endpoint")
	}
}

func TestLoadBalancerRemovesStaleListeners(t *testing.T) {
	f := newLBFixture(t)
	ctx := context.Background()
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if len(rulesOf(t, f.c, "lb1")) != 2 {
		t.Fatal("two rules expected")
	}
	var lb v1alpha1.LoadBalancer
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: "lb1"}, &lb); err != nil {
		t.Fatal(err)
	}
	lb.Spec.Listeners = lb.Spec.Listeners[:1]
	if err := f.c.Update(ctx, &lb); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	rules := rulesOf(t, f.c, "lb1")
	if len(rules) != 1 || rules[0].GetName() != ListenerRuleName(f.namespace, "lb1", 80, "TCP") {
		t.Fatalf("rules %v", rules)
	}
}

func TestLoadBalancerDeletionRemovesRules(t *testing.T) {
	f := newLBFixture(t)
	ctx := context.Background()
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	var lb v1alpha1.LoadBalancer
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: "lb1"}, &lb); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(ctx, &lb); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	if len(rulesOf(t, f.c, "lb1")) != 0 {
		t.Fatal("the rules must be deleted")
	}
	if err := f.c.Get(ctx, client.ObjectKeyFromObject(&lb), &v1alpha1.LoadBalancer{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the finalizer must be gone: %v", err)
	}
}

func TestLoadBalancerUnsupportedWithoutRouterLBRuleKind(t *testing.T) {
	f := newLBFixture(t)
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", func(schema.GroupKind) bool { return false })
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonUnsupported)
	if len(rulesOf(t, f.c, "lb1")) != 0 {
		t.Fatal("nothing must be applied")
	}
}

func TestLoadBalancerRejectsDifferentTargetPorts(t *testing.T) {
	f := newLBFixture(t)
	ctx := context.Background()
	var lb v1alpha1.LoadBalancer
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: "lb1"}, &lb); err != nil {
		t.Fatal(err)
	}
	lb.Spec.Listeners[0].Targets[1].Port = 9090
	if err := f.c.Update(ctx, &lb); err != nil {
		t.Fatal(err)
	}
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonTargetPortsDiffer)
}

func TestLoadBalancerRequiresLabeledNamespace(t *testing.T) {
	c := cleanEnv(t)
	bare := unique(t, "bare")
	requireAccepted(t, c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bare}})
	requireAccepted(t, c, loadBalancerFor(bare, "lb1", "ip1"))
	reconcileLoadBalancer(t, c, bare, "lb1", knowsEverything)
	requireLB(t, c, bare, "lb1", metav1.ConditionFalse, ReasonNamespaceNotLabeled)
}
