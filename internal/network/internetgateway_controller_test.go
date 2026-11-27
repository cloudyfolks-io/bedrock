package network

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabrictest"
)

func igwTarget(name string) *v1alpha1.ElasticIPTarget {
	return &v1alpha1.ElasticIPTarget{Kind: v1alpha1.ElasticIPTargetInternetGateway, Name: name}
}

func internetGatewayFor(namespace, name, network, eip string) *v1alpha1.InternetGateway {
	return &v1alpha1.InternetGateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       v1alpha1.InternetGatewaySpec{NetworkRef: network, ElasticIPRef: eip},
	}
}

func reconcileGateway(t *testing.T, c client.Client, namespace, name string) reconcile.Result {
	t.Helper()
	return reconcileGatewayWith(t, c, namespace, name, knowsEverything)
}

func reconcileGatewayWith(t *testing.T, c client.Client, namespace, name string, kinds KindProbe) reconcile.Result {
	t.Helper()
	result, err := (&InternetGatewayReconciler{Client: c, Kinds: kinds}).Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireGateway(t *testing.T, c client.Client, namespace, name string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	var gw v1alpha1.InternetGateway
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &gw); err != nil {
		t.Fatal(err)
	}
	requireCondition(t, gw.Status.Conditions, v1alpha1.ConditionReady, status, reason)
}

func snatRulesOf(t *testing.T, c client.Client, name string) []*unstructured.Unstructured {
	t.Helper()
	rules, err := listObjects(context.Background(), c, fabric.OvnSnatRuleGVK, client.MatchingLabels{v1alpha1.LabelKind: "InternetGateway", v1alpha1.LabelName: name})
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func snatCidrs(rules []*unstructured.Unstructured) []string {
	var cidrs []string
	for _, rule := range rules {
		cidr, _, _ := unstructured.NestedString(rule.Object, "spec", "v4IpCidr")
		cidrs = append(cidrs, cidr)
	}
	slices.Sort(cidrs)
	return cidrs
}

func markSnatRulesReady(t *testing.T, c client.Client, name string) {
	t.Helper()
	for _, rule := range snatRulesOf(t, c, name) {
		if err := fabrictest.MarkSnatRuleReady(context.Background(), c, rule.GetName()); err != nil {
			t.Fatal(err)
		}
	}
}

func (f externalFixture) provisionGateway(t *testing.T, name, eip string) {
	t.Helper()
	f.provisionGatewayOf(t, internetGatewayFor(f.namespace, name, f.network.Name, eip))
}

func (f externalFixture) provisionGatewayOf(t *testing.T, gateway *v1alpha1.InternetGateway) {
	t.Helper()
	eip := gateway.Spec.ElasticIPRef
	requireAccepted(t, f.c, gateway)
	f.createEIP(t, eip, "", igwTarget(gateway.Name))
	reconcileElasticIP(t, f.c, f.namespace, eip)
	allocate(t, f, eip, "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, eip)
}

func markSubnetReadyInBedrock(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	var subnet v1alpha1.Subnet
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &subnet); err != nil {
		t.Fatal(err)
	}
	subnet.Status.Conditions = withCondition(subnet.Status.Conditions, trueCondition(v1alpha1.ConditionReady, subnet.Generation, ReasonApplied))
	if err := c.Status().Update(context.Background(), &subnet); err != nil {
		t.Fatal(err)
	}
}

func newGatewayFixture(t *testing.T) externalFixture {
	t.Helper()
	f := newExternalFixture(t)
	f.provisionGateway(t, "gw", "ip1")
	return f
}

func newRangesGatewayFixture(t *testing.T) externalFixture {
	t.Helper()
	network := networkOfRanges(unique(t, "net"), unique(t, "acme"), "10.100.0.0/16", "172.16.0.0/12")
	network.Spec.EnableExternal = true
	f := buildFixture(t, network, attachRouterPort)
	f.provisionGateway(t, "gw", "ip1")
	return f
}

func TestInternetGatewayWritesOneRulePerRangeOfTheNetwork(t *testing.T) {
	f := newRangesGatewayFixture(t)
	if result := reconcileGateway(t, f.c, f.namespace, "gw"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	rules := snatRulesOf(t, f.c, "gw")
	if got := snatCidrs(rules); !slices.Equal(got, []string{"10.100.0.0/16", "172.16.0.0/12"}) {
		t.Fatalf("cidrs %v", got)
	}
	for _, rule := range rules {
		spec, _, _ := unstructured.NestedMap(rule.Object, "spec")
		if spec["ovnEip"] != FabricName(f.namespace, "ip1") || spec["vpc"] != f.network.Name {
			t.Fatalf("spec %v", spec)
		}
		if _, uid, ok := OwnerOf(rule); !ok || uid == "" {
			t.Fatal("the owner annotations are missing")
		}
		if rule.GetLabels()[LabelManaged] != "true" || rule.GetLabels()[v1alpha1.LabelTenant] != f.tenant {
			t.Fatalf("labels %v", rule.GetLabels())
		}
	}
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonFabricNotReady)
	markSnatRulesReady(t, f.c, "gw")
	if result := reconcileGateway(t, f.c, f.namespace, "gw"); result.RequeueAfter != 0 {
		t.Fatalf("result %+v", result)
	}
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionTrue, ReasonProgrammed)
}

func TestInternetGatewayWritesTheSubnetFormWithASubnetRef(t *testing.T) {
	f := newExternalFixture(t)
	requireAccepted(t, f.c, policySubnet(f.namespace, "other", f.network.Name, "10.100.2.0/24"))
	markSubnetReadyInBedrock(t, f.c, f.namespace, "other")
	gateway := internetGatewayFor(f.namespace, "gw", f.network.Name, "ip1")
	gateway.Spec.SubnetRef = "other"
	f.provisionGatewayOf(t, gateway)
	reconcileGateway(t, f.c, f.namespace, "gw")
	rules := snatRulesOf(t, f.c, "gw")
	if len(rules) != 1 {
		t.Fatalf("rules %d", len(rules))
	}
	spec, _, _ := unstructured.NestedMap(rules[0].Object, "spec")
	if spec["vpcSubnet"] != FabricName(f.namespace, "other") || spec["ovnEip"] != FabricName(f.namespace, "ip1") || len(spec) != 2 {
		t.Fatalf("spec %v", spec)
	}
}

func TestInternetGatewayRefusesASubnetOfAnotherNetwork(t *testing.T) {
	f := newExternalFixture(t)
	foreign := networkOf(unique(t, "net"), f.tenant, "10.200.0.0/16")
	requireAccepted(t, f.c, foreign)
	requireAccepted(t, f.c, policySubnet(f.namespace, "elsewhere", foreign.Name, "10.200.1.0/24"))
	gateway := internetGatewayFor(f.namespace, "gw", f.network.Name, "ip1")
	gateway.Spec.SubnetRef = "elsewhere"
	f.provisionGatewayOf(t, gateway)
	reconcileGateway(t, f.c, f.namespace, "gw")
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonTargetNetworkMismatch)
	if len(snatRulesOf(t, f.c, "gw")) != 0 {
		t.Fatal("no rule may be written")
	}
}

func TestInternetGatewayRefusesAMissingSubnet(t *testing.T) {
	f := newExternalFixture(t)
	gateway := internetGatewayFor(f.namespace, "gw", f.network.Name, "ip1")
	gateway.Spec.SubnetRef = "ghost"
	f.provisionGatewayOf(t, gateway)
	reconcileGateway(t, f.c, f.namespace, "gw")
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonRefNotFound)
}

func TestInternetGatewayWritesARuleForARangeAddedLater(t *testing.T) {
	f := newGatewayFixture(t)
	reconcileGateway(t, f.c, f.namespace, "gw")
	if got := snatCidrs(snatRulesOf(t, f.c, "gw")); !slices.Equal(got, []string{"10.100.0.0/16"}) {
		t.Fatalf("cidrs %v", got)
	}
	var network v1alpha1.Network
	if err := f.c.Get(context.Background(), client.ObjectKey{Name: f.network.Name}, &network); err != nil {
		t.Fatal(err)
	}
	network.Spec.CIDRs, network.Spec.CIDR = []string{"10.100.0.0/16", "192.168.0.0/16"}, ""
	if err := f.c.Update(context.Background(), &network); err != nil {
		t.Fatal(err)
	}
	reconcileGateway(t, f.c, f.namespace, "gw")
	if got := snatCidrs(snatRulesOf(t, f.c, "gw")); !slices.Equal(got, []string{"10.100.0.0/16", "192.168.0.0/16"}) {
		t.Fatalf("cidrs %v", got)
	}
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonFabricNotReady)
}

func TestInternetGatewayRemovesTheRulesOfARangeThatLeftTheNetwork(t *testing.T) {
	f := newRangesGatewayFixture(t)
	reconcileGateway(t, f.c, f.namespace, "gw")
	stale := SnatRuleName(f.network.Name, "198.51.100.0/24")
	owned := snatRulesOf(t, f.c, "gw")[0]
	extra := fabric.OvnSnatRule(fabric.Meta{Name: stale, Labels: owned.GetLabels(), Annotations: owned.GetAnnotations()}, FabricName(f.namespace, "ip1"), f.network.Name, "198.51.100.0/24")
	if err := f.c.Create(context.Background(), extra); err != nil {
		t.Fatal(err)
	}
	reconcileGateway(t, f.c, f.namespace, "gw")
	if got := snatCidrs(snatRulesOf(t, f.c, "gw")); !slices.Equal(got, []string{"10.100.0.0/16", "172.16.0.0/12"}) {
		t.Fatalf("cidrs %v", got)
	}
}

func TestInternetGatewayRefusesAnElasticIPOfAnotherTarget(t *testing.T) {
	targets := map[string]func(externalFixture) *v1alpha1.ElasticIPTarget{
		"vm": func(f externalFixture) *v1alpha1.ElasticIPTarget {
			f.addVM(t, "vm1", "", "10.100.1.5")
			return vmTarget("vm1")
		},
		"lb":   func(f externalFixture) *v1alpha1.ElasticIPTarget { return lbTarget("lb1") },
		"none": func(externalFixture) *v1alpha1.ElasticIPTarget { return nil },
		"gw2":  func(externalFixture) *v1alpha1.ElasticIPTarget { return igwTarget("other") },
	}
	for name, target := range targets {
		t.Run(name, func(t *testing.T) {
			f := newExternalFixture(t)
			requireAccepted(t, f.c, internetGatewayFor(f.namespace, "gw", f.network.Name, "ip1"))
			f.createEIP(t, "ip1", "", target(f))
			reconcileElasticIP(t, f.c, f.namespace, "ip1")
			allocate(t, f, "ip1", "91.246.31.20")
			reconcileElasticIP(t, f.c, f.namespace, "ip1")
			reconcileGateway(t, f.c, f.namespace, "gw")
			requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonTargetMismatch)
			if len(snatRulesOf(t, f.c, "gw")) != 0 {
				t.Fatal("no rule may be written")
			}
		})
	}
}

func TestLoadBalancerRefusesAnElasticIPOfAnInternetGateway(t *testing.T) {
	f := newGatewayFixture(t)
	requireAccepted(t, f.c, loadBalancerFor(f.namespace, "lb1", "ip1"))
	reconcileLoadBalancer(t, f.c, f.namespace, "lb1", knowsEverything)
	requireLB(t, f.c, f.namespace, "lb1", metav1.ConditionFalse, ReasonTargetMismatch)
	if len(rulesOf(t, f.c, "lb1")) != 0 {
		t.Fatal("the LoadBalancer must not use the address")
	}
}

func TestElasticIPOfAnInternetGatewayHasNoFipAndIsAttached(t *testing.T) {
	f := newGatewayFixture(t)
	requireNoFip(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionTrue, ReasonAttached)
}

func TestElasticIPOfAnInternetGatewayNeedsTheGatewayToNameIt(t *testing.T) {
	f := newExternalFixture(t)
	requireAccepted(t, f.c, internetGatewayFor(f.namespace, "gw", f.network.Name, "someone-else"))
	f.createEIP(t, "ip1", "", igwTarget("gw"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonTargetMismatch)
}

func TestElasticIPOfAnInternetGatewayNeedsTheGatewayToExist(t *testing.T) {
	f := newExternalFixture(t)
	f.createEIP(t, "ip1", "", igwTarget("ghost"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	allocate(t, f, "ip1", "91.246.31.20")
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	requireEIP(t, f.c, f.namespace, "ip1", metav1.ConditionFalse, ReasonTargetNotFound)
}

func TestMovingTheElasticIPAwayFromTheGatewayRemovesTheRules(t *testing.T) {
	f := newGatewayFixture(t)
	f.addVM(t, "vm1", "", "10.100.1.5")
	reconcileGateway(t, f.c, f.namespace, "gw")
	if len(snatRulesOf(t, f.c, "gw")) == 0 {
		t.Fatal("the gateway must write its rules first")
	}
	eip := getElasticIP(t, f.c, f.namespace, "ip1")
	eip.Spec.Target = vmTarget("vm1")
	if err := f.c.Update(context.Background(), &eip); err != nil {
		t.Fatal(err)
	}
	reconcileGateway(t, f.c, f.namespace, "gw")
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonTargetMismatch)
	if len(snatRulesOf(t, f.c, "gw")) != 0 {
		t.Fatal("the rules must go when the address belongs to another target")
	}
}

func TestInternetGatewayWaitsForTheElasticIPAddress(t *testing.T) {
	f := newExternalFixture(t)
	requireAccepted(t, f.c, internetGatewayFor(f.namespace, "gw", f.network.Name, "ip1"))
	f.createEIP(t, "ip1", "", igwTarget("gw"))
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	if result := reconcileGateway(t, f.c, f.namespace, "gw"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonElasticIPNotReady)
	if len(snatRulesOf(t, f.c, "gw")) != 0 {
		t.Fatal("no rule may be written")
	}
}

func TestInternetGatewayWithoutAnElasticIPKeepsNoRules(t *testing.T) {
	f := newGatewayFixture(t)
	reconcileGateway(t, f.c, f.namespace, "gw")
	eip := getElasticIP(t, f.c, f.namespace, "ip1")
	if err := f.c.Delete(context.Background(), &eip); err != nil {
		t.Fatal(err)
	}
	reconcileElasticIP(t, f.c, f.namespace, "ip1")
	reconcileGateway(t, f.c, f.namespace, "gw")
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonElasticIPNotFound)
	if len(snatRulesOf(t, f.c, "gw")) != 0 {
		t.Fatal("the rules must go with the address")
	}
}

func TestInternetGatewayReleasesTheRulesOfAnElasticIPThatIsBeingDeleted(t *testing.T) {
	f := newGatewayFixture(t)
	reconcileGateway(t, f.c, f.namespace, "gw")
	eip := getElasticIP(t, f.c, f.namespace, "ip1")
	if err := f.c.Delete(context.Background(), &eip); err != nil {
		t.Fatal(err)
	}
	held := getElasticIP(t, f.c, f.namespace, "ip1")
	if held.DeletionTimestamp.IsZero() {
		t.Fatal("the finalizer must keep the ElasticIP")
	}
	reconcileGateway(t, f.c, f.namespace, "gw")
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonElasticIPNotFound)
	if len(snatRulesOf(t, f.c, "gw")) != 0 {
		t.Fatal("the rules must go so the address can be released")
	}
}

func TestInternetGatewayRefusesASecondGatewayOfANetwork(t *testing.T) {
	f := newGatewayFixture(t)
	second := newNamespace(t, f.tenant, "zz")
	requireAccepted(t, f.c, internetGatewayFor(second, "gw2", f.network.Name, "ip2"))
	eip := elasticIPOf(second, "ip2", "")
	eip.Spec.Target = igwTarget("gw2")
	requireAccepted(t, f.c, eip)
	reconcileElasticIP(t, f.c, second, "ip2")
	if err := fabrictest.MarkEipAllocated(context.Background(), f.c, FabricName(second, "ip2"), "91.246.31.21"); err != nil {
		t.Fatal(err)
	}
	reconcileElasticIP(t, f.c, second, "ip2")
	reconcileGateway(t, f.c, f.namespace, "gw")
	reconcileGateway(t, f.c, second, "gw2")
	requireGateway(t, f.c, second, "gw2", metav1.ConditionFalse, ReasonGatewayExists)
	if len(snatRulesOf(t, f.c, "gw2")) != 0 || len(snatRulesOf(t, f.c, "gw")) == 0 {
		t.Fatal("only the older gateway may hold rules")
	}
	older := &v1alpha1.InternetGateway{ObjectMeta: metav1.ObjectMeta{Namespace: f.namespace, Name: "gw"}}
	if err := f.c.Delete(context.Background(), older); err != nil {
		t.Fatal(err)
	}
	reconcileGateway(t, f.c, f.namespace, "gw")
	reconcileGateway(t, f.c, second, "gw2")
	requireGateway(t, f.c, second, "gw2", metav1.ConditionFalse, ReasonFabricNotReady)
	if len(snatRulesOf(t, f.c, "gw2")) == 0 {
		t.Fatal("the second gateway must take over when the first is gone")
	}
}

func TestInternetGatewayOfAnotherTenantDoesNotPreemptTheNetworkOwner(t *testing.T) {
	f := newExternalFixture(t)
	foreign := newNamespace(t, unique(t, "evil"), "prod")
	requireAccepted(t, f.c, internetGatewayFor(foreign, "squat", f.network.Name, "ip9"))
	time.Sleep(1100 * time.Millisecond)
	f.provisionGateway(t, "gw", "ip1")
	reconcileGateway(t, f.c, f.namespace, "gw")
	before := snatRuleNames(t, f.c, "gw")
	if len(before) == 0 {
		t.Fatal("the gateway of the owner must write its rules")
	}
	reconcileGateway(t, f.c, foreign, "squat")
	requireGateway(t, f.c, foreign, "squat", metav1.ConditionFalse, ReasonNetworkNotOwned)
	reconcileGateway(t, f.c, f.namespace, "gw")
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonFabricNotReady)
	if after := snatRuleNames(t, f.c, "gw"); !slices.Equal(before, after) {
		t.Fatalf("rules before %v after %v", before, after)
	}
}

func snatRuleNames(t *testing.T, c client.Client, gateway string) []string {
	t.Helper()
	var names []string
	for _, rule := range snatRulesOf(t, c, gateway) {
		names = append(names, rule.GetName())
	}
	slices.Sort(names)
	return names
}

func TestInternetGatewayKeepsItsRulesOnATransientRefusal(t *testing.T) {
	ctx := context.Background()
	causes := map[string]struct {
		cause  func(*testing.T, externalFixture)
		reason string
	}{
		"network disconnected": {
			cause: func(t *testing.T, f externalFixture) {
				if err := deleteObject(ctx, f.c, fabric.OvnEipGVK, f.network.Name+"-provider-public"); err != nil {
					t.Fatal(err)
				}
			},
			reason: ReasonExternalNotConnected,
		},
		"elastic ip not ready": {
			cause: func(t *testing.T, f externalFixture) {
				eip := getElasticIP(t, f.c, f.namespace, "ip1")
				eip.Status.Conditions = withCondition(eip.Status.Conditions, falseCondition(v1alpha1.ConditionReady, eip.Generation, ReasonFabricNotReady, "moving"))
				if err := f.c.Status().Update(ctx, &eip); err != nil {
					t.Fatal(err)
				}
			},
			reason: ReasonElasticIPNotReady,
		},
		"fabric conflict": {
			cause: func(t *testing.T, f externalFixture) {
				var network v1alpha1.Network
				if err := f.c.Get(ctx, client.ObjectKey{Name: f.network.Name}, &network); err != nil {
					t.Fatal(err)
				}
				network.Spec.CIDRs = append(slices.Clone(network.Spec.Ranges()), "192.168.0.0/16")
				network.Spec.CIDR = ""
				if err := f.c.Update(ctx, &network); err != nil {
					t.Fatal(err)
				}
				foreign := fabric.OvnSnatRule(fabric.Meta{Name: SnatRuleName(f.network.Name, "192.168.0.0/16"), Annotations: map[string]string{AnnotationOwnerName: "x", AnnotationOwnerUID: "foreign"}}, "e", f.network.Name, "192.168.0.0/16")
				if err := f.c.Create(ctx, foreign); err != nil {
					t.Fatal(err)
				}
			},
			reason: ReasonFabricConflict,
		},
	}
	for name, tc := range causes {
		t.Run(name, func(t *testing.T) {
			f := newRangesGatewayFixture(t)
			reconcileGateway(t, f.c, f.namespace, "gw")
			before := snatRuleNames(t, f.c, "gw")
			if len(before) != 2 {
				t.Fatalf("rules %v", before)
			}
			tc.cause(t, f)
			reconcileGateway(t, f.c, f.namespace, "gw")
			requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, tc.reason)
			if after := snatRuleNames(t, f.c, "gw"); !slices.Equal(before, after) {
				t.Fatalf("rules before %v after %v", before, after)
			}
		})
	}
	t.Run("api error", func(t *testing.T) {
		f := newRangesGatewayFixture(t)
		reconcileGateway(t, f.c, f.namespace, "gw")
		before := snatRuleNames(t, f.c, "gw")
		failing := watchingClient(t, interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, isEIP := obj.(*v1alpha1.ElasticIP); isEIP {
				return errors.New("the API server is unavailable")
			}
			return cl.Get(ctx, key, obj, opts...)
		}})
		request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: f.namespace, Name: "gw"}}
		if _, err := (&InternetGatewayReconciler{Client: failing, Kinds: knowsEverything}).Reconcile(ctx, request); err == nil {
			t.Fatal("the failure must be reported")
		}
		if after := snatRuleNames(t, f.c, "gw"); !slices.Equal(before, after) {
			t.Fatalf("rules before %v after %v", before, after)
		}
	})
}

func TestInternetGatewayDeletionRemovesTheRules(t *testing.T) {
	f := newRangesGatewayFixture(t)
	ctx := context.Background()
	reconcileGateway(t, f.c, f.namespace, "gw")
	rules := snatRulesOf(t, f.c, "gw")
	if len(rules) != 2 {
		t.Fatalf("rules %d", len(rules))
	}
	held := rules[0]
	held.SetFinalizers([]string{"fabric.test/hold"})
	if err := f.c.Update(ctx, held); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(ctx, &v1alpha1.InternetGateway{ObjectMeta: metav1.ObjectMeta{Namespace: f.namespace, Name: "gw"}}); err != nil {
		t.Fatal(err)
	}
	if result := reconcileGateway(t, f.c, f.namespace, "gw"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: "gw"}, &v1alpha1.InternetGateway{}); err != nil {
		t.Fatal("the finalizer must stay while a rule exists")
	}
	held, _ = getObject(ctx, f.c, fabric.OvnSnatRuleGVK, held.GetName())
	held.SetFinalizers(nil)
	if err := f.c.Update(ctx, held); err != nil {
		t.Fatal(err)
	}
	reconcileGateway(t, f.c, f.namespace, "gw")
	if len(snatRulesOf(t, f.c, "gw")) != 0 {
		t.Fatal("every rule must be deleted")
	}
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: "gw"}, &v1alpha1.InternetGateway{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the finalizer must be gone: %v", err)
	}
}

func TestInternetGatewayNeedsAConnectedNetwork(t *testing.T) {
	f := newDisconnectedFixture(t)
	f.provisionGateway(t, "gw", "ip1")
	if result := reconcileGateway(t, f.c, f.namespace, "gw"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonExternalNotConnected)
	if len(snatRulesOf(t, f.c, "gw")) != 0 {
		t.Fatal("no rule may be written")
	}
}

func TestInternetGatewayNeedsAnExternalNetwork(t *testing.T) {
	f := newPrivateFixture(t)
	f.provisionGateway(t, "gw", "ip1")
	reconcileGateway(t, f.c, f.namespace, "gw")
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonNetworkNotExternal)
	if len(snatRulesOf(t, f.c, "gw")) != 0 {
		t.Fatal("no rule may be written")
	}
}

func TestInternetGatewayNeedsANetworkOfTheTenant(t *testing.T) {
	f := newExternalFixture(t)
	other := newExternalFixture(t)
	requireAccepted(t, f.c, internetGatewayFor(f.namespace, "gw", other.network.Name, "ip1"))
	reconcileGateway(t, f.c, f.namespace, "gw")
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonNetworkNotOwned)
}

func TestInternetGatewayNeedsAnExistingNetwork(t *testing.T) {
	f := newExternalFixture(t)
	requireAccepted(t, f.c, internetGatewayFor(f.namespace, "gw", "ghost", "ip1"))
	reconcileGateway(t, f.c, f.namespace, "gw")
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonNetworkNotFound)
}

func TestInternetGatewayReportsARuleOfAnotherOwner(t *testing.T) {
	f := newGatewayFixture(t)
	foreign := fabric.OvnSnatRule(fabric.Meta{Name: SnatRuleName(f.network.Name, "10.100.0.0/16"), Annotations: map[string]string{AnnotationOwnerName: "x", AnnotationOwnerUID: "foreign"}}, "e", f.network.Name, "10.100.0.0/16")
	if err := f.c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	reconcileGateway(t, f.c, f.namespace, "gw")
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonFabricConflict)
}

func TestInternetGatewayNeedsTheFabricKind(t *testing.T) {
	f := newGatewayFixture(t)
	missing := func(gk schema.GroupKind) bool { return gk.Kind != "OvnSnatRule" }
	reconcileGatewayWith(t, f.c, f.namespace, "gw", missing)
	requireGateway(t, f.c, f.namespace, "gw", metav1.ConditionFalse, ReasonUnsupported)
}

func TestInternetGatewayRequiresALabeledNamespace(t *testing.T) {
	c := cleanEnv(t)
	publicSettings(t)
	bare := unique(t, "bare")
	requireAccepted(t, c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bare}})
	requireAccepted(t, c, internetGatewayFor(bare, "gw", "net", "ip1"))
	reconcileGateway(t, c, bare, "gw")
	requireGateway(t, c, bare, "gw", metav1.ConditionFalse, ReasonNamespaceNotLabeled)
}

func TestElasticIPOfAnInternetGatewayCountsTowardsTheQuota(t *testing.T) {
	f := quotaWorld(t, limitOf(1))
	gatewayIP := elasticIPOf(f.prod, "ip1", "")
	gatewayIP.Spec.ProviderNetwork = ""
	gatewayIP.Spec.Target = igwTarget("gw")
	requireAccepted(t, f.c, gatewayIP)
	reconcileElasticIP(t, f.c, f.prod, "ip1")
	requireProvisioned(t, f.c, f.prod, "ip1")
	f.request(t, f.prod, "ip2")
	reconcileElasticIP(t, f.c, f.prod, "ip2")
	requireWaitingForQuota(t, f.c, f.prod, "ip2")
}

func TestSweeperDeletesTheSnatRulesOfAGoneGateway(t *testing.T) {
	c := cleanEnv(t)
	old := time.Now().Add(-time.Hour)
	orphan := ownedObject(fabric.OvnSnatRule(fabric.Meta{Name: "acme-igw-abc"}, "e", "acme", "10.0.0.0/8"), "InternetGateway", "ns", "gw", "gone-uid", old)
	orphan.SetCreationTimestamp(metav1.Time{})
	if err := c.Create(context.Background(), orphan); err != nil {
		t.Fatal(err)
	}
	if err := (&Sweeper{Client: c, Now: afterTwoMinutes}).SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := getObject(context.Background(), c, fabric.OvnSnatRuleGVK, "acme-igw-abc"); !apierrors.IsNotFound(err) {
		t.Fatalf("the orphaned rule must be deleted: %v", err)
	}
}

func TestSnatRulesGoBeforeTheEipTheyUse(t *testing.T) {
	old := sweepEpoch.Add(-time.Hour)
	rule := ownedObject(fabric.OvnSnatRule(fabric.Meta{Name: "r"}, "e", "acme", "10.0.0.0/8"), "InternetGateway", "ns", "gw", "u", old)
	got := FindOrphans([]*unstructured.Unstructured{eipOf("eip", "gone", "u", old), rule}, ownersOf(), sweepEpoch, time.Minute)
	if len(got) != 2 || got[0].GVK != fabric.OvnSnatRuleGVK || got[1].GVK != fabric.OvnEipGVK {
		t.Fatalf("orphans %+v", got)
	}
}

func TestInternetGatewayConditionCarriesTheGeneration(t *testing.T) {
	f := newGatewayFixture(t)
	reconcileGateway(t, f.c, f.namespace, "gw")
	var gw v1alpha1.InternetGateway
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: f.namespace, Name: "gw"}, &gw); err != nil {
		t.Fatal(err)
	}
	ready := meta.FindStatusCondition(gw.Status.Conditions, v1alpha1.ConditionReady)
	if ready == nil || ready.ObservedGeneration != gw.Generation || gw.Status.ObservedGeneration != gw.Generation {
		t.Fatalf("status %+v", gw.Status)
	}
}
