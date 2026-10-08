package network

import (
	"context"
	"testing"

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

func reconcileNetwork(t *testing.T, c client.Client, name string) reconcile.Result {
	t.Helper()
	result, err := (&NetworkReconciler{Client: c}).Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func getNetwork(t *testing.T, c client.Client, name string) v1alpha1.Network {
	t.Helper()
	var network v1alpha1.Network
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &network); err != nil {
		t.Fatal(err)
	}
	return network
}

func requireCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	got := meta.FindStatusCondition(conditions, conditionType)
	if got == nil || got.Status != status || got.Reason != reason {
		t.Fatalf("condition %s: want %s/%s, got %+v", conditionType, status, reason, got)
	}
}

func createNetwork(t *testing.T, c client.Client) v1alpha1.Network {
	t.Helper()
	network := networkOf(unique(t, "net"), "acme", "10.100.0.0/16")
	requireAccepted(t, c, network)
	return *network
}

func createExternalNetwork(t *testing.T, c client.Client) v1alpha1.Network {
	t.Helper()
	network := networkOf(unique(t, "net"), "acme", "10.100.0.0/16")
	network.Spec.EnableExternal = true
	requireAccepted(t, c, network)
	return *network
}

func publicSettings(t *testing.T) {
	t.Helper()
	setSetting(t, "network.provider-networks", publicNetworks)
}

func bgpSettings(t *testing.T) {
	t.Helper()
	for key, value := range bgpValues(nil) {
		setSetting(t, key, value)
	}
}

func vpcOf(t *testing.T, c client.Client, name string) *unstructured.Unstructured {
	t.Helper()
	vpc, err := getObject(context.Background(), c, fabric.VpcGVK, name)
	if err != nil {
		t.Fatal(err)
	}
	return vpc
}

func TestNetworkReconcilerCreatesVpc(t *testing.T) {
	c := cleanEnv(t)
	network := createNetwork(t, c)
	result := reconcileNetwork(t, c, network.Name)
	if result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	vpc := vpcOf(t, c, network.Name)
	labels := vpc.GetLabels()
	if labels[v1alpha1.LabelKind] != "Network" || labels[v1alpha1.LabelName] != network.Name || labels[v1alpha1.LabelTenant] != "acme" || labels[LabelManaged] != "true" {
		t.Fatalf("labels %v", labels)
	}
	if spec, _, _ := unstructured.NestedMap(vpc.Object, "spec"); len(spec) != 0 {
		t.Fatalf("a private Network has an empty Vpc spec, got %v", spec)
	}
	if _, uid, ok := OwnerOf(vpc); !ok || uid != network.UID {
		t.Fatal("owner annotations are missing")
	}
	got := getNetwork(t, c, network.Name)
	requireCondition(t, got.Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonVpcPending)
	if got.Status.VPC != network.Name || got.Status.ObservedGeneration != got.Generation {
		t.Fatalf("status %+v", got.Status)
	}
	if err := fabrictest.MarkVpcStandby(context.Background(), c, network.Name); err != nil {
		t.Fatal(err)
	}
	if result := reconcileNetwork(t, c, network.Name); result.RequeueAfter != 0 {
		t.Fatalf("a ready Network needs no requeue: %+v", result)
	}
	requireCondition(t, getNetwork(t, c, network.Name).Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonVpcReady)
}

func TestNetworkReconcilerRendersL2External(t *testing.T) {
	c := cleanEnv(t)
	publicSettings(t)
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	vpc := vpcOf(t, c, network.Name)
	external, _, _ := unstructured.NestedBool(vpc.Object, "spec", "enableExternal")
	subnets, _, _ := unstructured.NestedStringSlice(vpc.Object, "spec", "extraExternalSubnets")
	routes, _, _ := unstructured.NestedSlice(vpc.Object, "spec", "staticRoutes")
	if !external || len(subnets) != 1 || subnets[0] != "provider-public" || len(routes) != 1 {
		t.Fatalf("spec %v", vpc.Object["spec"])
	}
	requireCondition(t, getNetwork(t, c, network.Name).Status.Conditions, ConditionExternalReady, metav1.ConditionFalse, ReasonPending)
	ctx := context.Background()
	if err := fabrictest.MarkVpcStandby(ctx, c, network.Name); err != nil {
		t.Fatal(err)
	}
	if err := fabrictest.CreateRouterPort(ctx, c, network.Name, "provider-public", "91.246.31.5"); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, network.Name)
	requireCondition(t, getNetwork(t, c, network.Name).Status.Conditions, ConditionExternalReady, metav1.ConditionTrue, ReasonConnected)
}

func TestNetworkReconcilerReportsNoProviderNetwork(t *testing.T) {
	c := cleanEnv(t)
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	requireCondition(t, getNetwork(t, c, network.Name).Status.Conditions, ConditionExternalReady, metav1.ConditionFalse, ReasonNoProviderNetwork)
}

func TestNetworkReconcilerWithoutExternalHasNoExternalCondition(t *testing.T) {
	c := cleanEnv(t)
	network := createNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	if meta.FindStatusCondition(getNetwork(t, c, network.Name).Status.Conditions, ConditionExternalReady) != nil {
		t.Fatal("a private Network has no ExternalReady condition")
	}
}

func vrfOf(t *testing.T, c client.Client, name string) int64 {
	t.Helper()
	id, _, _ := unstructured.NestedInt64(vpcOf(t, c, name).Object, "spec", "dynamicRouting", "vrfId")
	return id
}

func TestNetworkReconcilerBGPKeepsTheVrfIDStable(t *testing.T) {
	c := cleanEnv(t)
	bgpSettings(t)
	first := createExternalNetwork(t, c)
	second := createExternalNetwork(t, c)
	reconcileNetwork(t, c, first.Name)
	reconcileNetwork(t, c, second.Name)
	a, b := vrfOf(t, c, first.Name), vrfOf(t, c, second.Name)
	if a < 1000 || b < 1000 || a == b {
		t.Fatalf("ids %d %d", a, b)
	}
	reconcileNetwork(t, c, first.Name)
	if vrfOf(t, c, first.Name) != a || getNetwork(t, c, first.Name).Annotations[AnnotationVrfID] == "" {
		t.Fatal("the id must not change")
	}
}

func TestNetworkReconcilerBGPSkipsIDsUsedByForeignVpcs(t *testing.T) {
	c := cleanEnv(t)
	bgpSettings(t)
	foreign := fabric.Vpc(fabric.Meta{Name: unique(t, "foreign")}, fabric.VpcSpec{EnableExternal: true, ExtraExternalSubnets: []string{"x"}, Dynamic: &fabric.DynamicRouting{VrfID: 1000, ExternalSubnet: "x", Redistribute: []string{"nat"}}})
	if err := c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	if got := vrfOf(t, c, network.Name); got != 1001 {
		t.Fatalf("id %d", got)
	}
}

func TestNetworkReconcilerReportsFabricConflict(t *testing.T) {
	c := cleanEnv(t)
	network := createNetwork(t, c)
	foreign := fabric.Vpc(fabric.Meta{Name: network.Name, Annotations: map[string]string{AnnotationOwnerName: "someone", AnnotationOwnerUID: "other-uid"}}, fabric.VpcSpec{EnableExternal: true, ExtraExternalSubnets: []string{"x"}})
	if err := c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, network.Name)
	requireCondition(t, getNetwork(t, c, network.Name).Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonFabricConflict)
	if external, _, _ := unstructured.NestedBool(vpcOf(t, c, network.Name).Object, "spec", "enableExternal"); !external {
		t.Fatal("the foreign Vpc must stay untouched")
	}
}

func TestNetworkReconcilerListsAcceptedSubnetsInStatus(t *testing.T) {
	c := cleanEnv(t)
	network := createNetwork(t, c)
	ns := newNamespace(t, "acme", unique(t, "p"))
	for _, s := range []*v1alpha1.Subnet{
		policySubnet(ns, "a-old", network.Name, "10.100.1.0/24"),
		policySubnet(ns, "b-young", network.Name, "10.100.1.128/25"),
		policySubnet(ns, "c-outside", network.Name, "10.200.0.0/24"),
		policySubnet(ns, "d-elsewhere", "other-network", "10.100.9.0/24"),
	} {
		requireAccepted(t, c, s)
	}
	reconcileNetwork(t, c, network.Name)
	got := getNetwork(t, c, network.Name).Status.Subnets
	if len(got) != 1 || got[0].Name != "a-old" || got[0].Namespace != ns || got[0].CIDR != "10.100.1.0/24" {
		t.Fatalf("status subnets %+v", got)
	}
}

func TestNetworkDeletionWaitsForSubnets(t *testing.T) {
	c := cleanEnv(t)
	ctx := context.Background()
	network := createNetwork(t, c)
	ns := newNamespace(t, "acme", unique(t, "p"))
	subnet := policySubnet(ns, "keep", network.Name, "10.100.1.0/24")
	requireAccepted(t, c, subnet)
	reconcileNetwork(t, c, network.Name)
	if err := c.Delete(ctx, &network); err != nil {
		t.Fatal(err)
	}
	result := reconcileNetwork(t, c, network.Name)
	if result.RequeueAfter != terminatingRequeue {
		t.Fatalf("result %+v", result)
	}
	requireCondition(t, getNetwork(t, c, network.Name).Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonTerminating)
	if _, err := getObject(ctx, c, fabric.VpcGVK, network.Name); err != nil {
		t.Fatal("the Vpc must stay while Subnets remain")
	}
	if err := c.Delete(ctx, subnet); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, network.Name)
	if _, err := getObject(ctx, c, fabric.VpcGVK, network.Name); !apierrors.IsNotFound(err) {
		t.Fatalf("the Vpc must be deleted: %v", err)
	}
	var gone v1alpha1.Network
	if err := c.Get(ctx, client.ObjectKey{Name: network.Name}, &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("the finalizer must be gone: %v", err)
	}
}

func TestNetworkReconcilerIgnoresForeignVpcs(t *testing.T) {
	c := cleanEnv(t)
	foreign := fabric.Vpc(fabric.Meta{Name: unique(t, "ovn-foreign")}, fabric.VpcSpec{})
	if err := c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	network := createNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	if err := c.Delete(context.Background(), &network); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, network.Name)
	if _, err := getObject(context.Background(), c, fabric.VpcGVK, foreign.GetName()); err != nil {
		t.Fatalf("a foreign Vpc must survive: %v", err)
	}
}

type foreignFixture struct {
	c         client.Client
	network   v1alpha1.Network
	namespace string
	intruder  string
}

func newForeignFixture(t *testing.T) foreignFixture {
	t.Helper()
	c := cleanEnv(t)
	tenant := unique(t, "zz")
	network := networkOf(unique(t, "net"), tenant, "10.100.0.0/16")
	requireAccepted(t, c, network)
	f := foreignFixture{c: c, network: *network, namespace: newNamespace(t, tenant, "prod"), intruder: newNamespace(t, unique(t, "aa"), "prod")}
	requireAccepted(t, c, policySubnet(f.intruder, "intruder", network.Name, "10.100.1.0/24"))
	return f
}

func TestNetworkReconcilerIgnoresSubnetsOfOtherTenants(t *testing.T) {
	f := newForeignFixture(t)
	requireAccepted(t, f.c, policySubnet(f.namespace, "mine", f.network.Name, "10.100.1.0/24"))
	reconcileNetwork(t, f.c, f.network.Name)
	got := getNetwork(t, f.c, f.network.Name).Status.Subnets
	if len(got) != 1 || got[0].Namespace != f.namespace {
		t.Fatalf("status subnets %+v", got)
	}
}

func TestNetworkDeletionIgnoresSubnetsOfOtherTenants(t *testing.T) {
	f := newForeignFixture(t)
	reconcileNetwork(t, f.c, f.network.Name)
	if err := f.c.Delete(context.Background(), &f.network); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, f.c, f.network.Name)
	var gone v1alpha1.Network
	if err := f.c.Get(context.Background(), client.ObjectKey{Name: f.network.Name}, &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("a foreign Subnet must not block the deletion: %v", err)
	}
}
