package network

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabrictest"
)

type subnetFixture struct {
	c         client.Client
	tenant    string
	network   v1alpha1.Network
	namespace string
}

func readyNetwork(t *testing.T, c client.Client, tenant string) v1alpha1.Network {
	t.Helper()
	network := networkOf(unique(t, "net"), tenant, "10.100.0.0/16")
	requireAccepted(t, c, network)
	reconcileNetwork(t, c, network.Name)
	if err := fabrictest.MarkVpcStandby(context.Background(), c, network.Name); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, network.Name)
	return *network
}

func newSubnetFixture(t *testing.T) subnetFixture {
	t.Helper()
	c := cleanEnv(t)
	tenant := unique(t, "acme")
	return subnetFixture{c: c, tenant: tenant, network: readyNetwork(t, c, tenant), namespace: newNamespace(t, tenant, "prod")}
}

func (f subnetFixture) create(t *testing.T, namespace, name, cidr string) *v1alpha1.Subnet {
	t.Helper()
	subnet := policySubnet(namespace, name, f.network.Name, cidr)
	requireAccepted(t, f.c, subnet)
	return subnet
}

func reconcileSubnet(t *testing.T, c client.Client, namespace, name string) reconcile.Result {
	t.Helper()
	result, err := (&SubnetReconciler{Client: c}).Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func getSubnet(t *testing.T, c client.Client, namespace, name string) v1alpha1.Subnet {
	t.Helper()
	var subnet v1alpha1.Subnet
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &subnet); err != nil {
		t.Fatal(err)
	}
	return subnet
}

func fabricSubnetOf(c client.Client, namespace, name string) (*unstructured.Unstructured, error) {
	return getObject(context.Background(), c, fabric.SubnetGVK, FabricName(namespace, name))
}

func attachmentOf(c client.Client, namespace, name string) (*unstructured.Unstructured, error) {
	u := newObject(attachmentGVK, name)
	return u, c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, u)
}

func requireSubnetReason(t *testing.T, c client.Client, namespace, name string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	requireCondition(t, getSubnet(t, c, namespace, name).Status.Conditions, v1alpha1.ConditionReady, status, reason)
}

func requireNoFabricSubnet(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	if _, err := fabricSubnetOf(c, namespace, name); !apierrors.IsNotFound(err) {
		t.Fatalf("no Fabric Subnet expected: %v", err)
	}
}

func TestSubnetReconcilerCreatesFabricSubnetAndAttachment(t *testing.T) {
	f := newSubnetFixture(t)
	f.create(t, f.namespace, "default", "10.100.1.0/24")
	if result := reconcileSubnet(t, f.c, f.namespace, "default"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	fabricSubnet, err := fabricSubnetOf(f.c, f.namespace, "default")
	if err != nil {
		t.Fatal(err)
	}
	if provider, _, _ := unstructured.NestedString(fabricSubnet.Object, "spec", "provider"); provider != "default."+f.namespace+".fabric" {
		t.Fatalf("provider %q", provider)
	}
	if vpc, _, _ := unstructured.NestedString(fabricSubnet.Object, "spec", "vpc"); vpc != f.network.Name {
		t.Fatalf("vpc %q", vpc)
	}
	nad, err := attachmentOf(f.c, f.namespace, "default")
	if err != nil {
		t.Fatal(err)
	}
	if config, _, _ := unstructured.NestedString(nad.Object, "spec", "config"); config != AttachmentConfig(f.namespace, "default") || len(nad.GetOwnerReferences()) != 1 {
		t.Fatalf("attachment %v", nad.Object)
	}
	requireSubnetReason(t, f.c, f.namespace, "default", metav1.ConditionFalse, ReasonFabricNotReady)
	if err := fabrictest.MarkSubnetReady(context.Background(), f.c, fabricSubnet.GetName()); err != nil {
		t.Fatal(err)
	}
	if result := reconcileSubnet(t, f.c, f.namespace, "default"); result.RequeueAfter != 0 {
		t.Fatalf("result %+v", result)
	}
	requireSubnetReason(t, f.c, f.namespace, "default", metav1.ConditionTrue, ReasonApplied)
}

func TestSubnetReconcilerWaitsForTheNetwork(t *testing.T) {
	c := cleanEnv(t)
	tenant := unique(t, "acme")
	network := networkOf(unique(t, "net"), tenant, "10.100.0.0/16")
	requireAccepted(t, c, network)
	namespace := newNamespace(t, tenant, "prod")
	requireAccepted(t, c, policySubnet(namespace, "default", network.Name, "10.100.1.0/24"))
	reconcileSubnet(t, c, namespace, "default")
	requireSubnetReason(t, c, namespace, "default", metav1.ConditionFalse, ReasonNetworkNotReady)
	requireNoFabricSubnet(t, c, namespace, "default")
}

func TestSubnetReconcilerReportsMissingNetwork(t *testing.T) {
	c := cleanEnv(t)
	namespace := newNamespace(t, unique(t, "acme"), "prod")
	requireAccepted(t, c, policySubnet(namespace, "default", "missing", "10.100.1.0/24"))
	reconcileSubnet(t, c, namespace, "default")
	requireSubnetReason(t, c, namespace, "default", metav1.ConditionFalse, ReasonNetworkNotFound)
	requireNoFabricSubnet(t, c, namespace, "default")
}

func TestSubnetReconcilerRejectsCIDROutsideNetwork(t *testing.T) {
	f := newSubnetFixture(t)
	f.create(t, f.namespace, "outside", "10.200.1.0/24")
	reconcileSubnet(t, f.c, f.namespace, "outside")
	requireSubnetReason(t, f.c, f.namespace, "outside", metav1.ConditionFalse, ReasonCIDROutsideNetwork)
	requireNoFabricSubnet(t, f.c, f.namespace, "outside")
}

func TestSubnetReconcilerRejectsOverlapKeepingTheOlder(t *testing.T) {
	f := newSubnetFixture(t)
	second := newNamespace(t, f.tenant, "zz")
	f.create(t, f.namespace, "old", "10.100.1.0/24")
	f.create(t, second, "young", "10.100.1.0/24")
	reconcileSubnet(t, f.c, f.namespace, "old")
	reconcileSubnet(t, f.c, second, "young")
	requireSubnetReason(t, f.c, f.namespace, "old", metav1.ConditionFalse, ReasonFabricNotReady)
	requireSubnetReason(t, f.c, second, "young", metav1.ConditionFalse, ReasonCIDROverlap)
	requireNoFabricSubnet(t, f.c, second, "young")
	if _, err := fabricSubnetOf(f.c, f.namespace, "old"); err != nil {
		t.Fatal(err)
	}
}

func TestSubnetReconcilerReportsFabricConflict(t *testing.T) {
	f := newSubnetFixture(t)
	f.create(t, f.namespace, "default", "10.100.1.0/24")
	foreign := fabric.Subnet(fabric.Meta{Name: FabricName(f.namespace, "default"), Annotations: map[string]string{AnnotationOwnerName: "x", AnnotationOwnerUID: "foreign"}}, fabric.SubnetSpec{CIDR: "10.250.0.0/24", Gateway: "10.250.0.1"})
	if err := f.c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireSubnetReason(t, f.c, f.namespace, "default", metav1.ConditionFalse, ReasonFabricConflict)
	if cidr, _, _ := unstructured.NestedString(foreign.Object, "spec", "cidrBlock"); cidr != "10.250.0.0/24" {
		t.Fatal("the foreign Subnet must stay untouched")
	}
	if _, err := attachmentOf(f.c, f.namespace, "default"); !apierrors.IsNotFound(err) {
		t.Fatalf("no attachment expected: %v", err)
	}
}

func TestSubnetReconcilerRequiresLabeledNamespace(t *testing.T) {
	f := newSubnetFixture(t)
	bare := unique(t, "bare")
	requireAccepted(t, f.c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bare}})
	requireAccepted(t, f.c, policySubnet(bare, "default", f.network.Name, "10.100.1.0/24"))
	reconcileSubnet(t, f.c, bare, "default")
	requireSubnetReason(t, f.c, bare, "default", metav1.ConditionFalse, ReasonNamespaceNotLabeled)
	requireNoFabricSubnet(t, f.c, bare, "default")
}

func TestSubnetReconcilerRejectsANetworkOfAnotherTenant(t *testing.T) {
	f := newSubnetFixture(t)
	foreign := newNamespace(t, unique(t, "other"), "prod")
	requireAccepted(t, f.c, policySubnet(foreign, "default", f.network.Name, "10.100.1.0/24"))
	reconcileSubnet(t, f.c, foreign, "default")
	requireSubnetReason(t, f.c, foreign, "default", metav1.ConditionFalse, ReasonNetworkNotAllowed)
	requireNoFabricSubnet(t, f.c, foreign, "default")
}

func TestSubnetReconcilerRejectsInvalidGateway(t *testing.T) {
	f := newSubnetFixture(t)
	subnet := policySubnet(f.namespace, "default", f.network.Name, "10.100.1.0/24")
	subnet.Spec.Gateway = "10.100.1.255"
	requireAccepted(t, f.c, subnet)
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireSubnetReason(t, f.c, f.namespace, "default", metav1.ConditionFalse, ReasonGatewayInvalid)
	requireNoFabricSubnet(t, f.c, f.namespace, "default")
}

func TestSubnetReconcilerRejectsInvalidExcludeIPs(t *testing.T) {
	f := newSubnetFixture(t)
	subnet := policySubnet(f.namespace, "default", f.network.Name, "10.100.1.0/24")
	subnet.Spec.ExcludeIPs = []string{"10.100.2.5"}
	requireAccepted(t, f.c, subnet)
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireSubnetReason(t, f.c, f.namespace, "default", metav1.ConditionFalse, ReasonExcludeIPsInvalid)
}

func TestSubnetDNSServersFromSettingsReachDHCP(t *testing.T) {
	f := newSubnetFixture(t)
	setSetting(t, "network.dns-servers", "1.1.1.1,8.8.8.8")
	f.create(t, f.namespace, "default", "10.100.1.0/24")
	reconcileSubnet(t, f.c, f.namespace, "default")
	fabricSubnet, err := fabricSubnetOf(f.c, f.namespace, "default")
	if err != nil {
		t.Fatal(err)
	}
	if options, _, _ := unstructured.NestedString(fabricSubnet.Object, "spec", "dhcpV4Options"); options != "dns_server=1.1.1.1;8.8.8.8" {
		t.Fatalf("options %q", options)
	}
}

func TestSubnetDeletionWaitsForUsedAddresses(t *testing.T) {
	f := newSubnetFixture(t)
	ctx := context.Background()
	subnet := f.create(t, f.namespace, "default", "10.100.1.0/24")
	reconcileSubnet(t, f.c, f.namespace, "default")
	fabricName := FabricName(f.namespace, "default")
	if err := fabrictest.SetSubnetUsedAddresses(ctx, f.c, fabricName, 2); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(ctx, subnet); err != nil {
		t.Fatal(err)
	}
	if result := reconcileSubnet(t, f.c, f.namespace, "default"); result.RequeueAfter != terminatingRequeue {
		t.Fatalf("result %+v", result)
	}
	requireSubnetReason(t, f.c, f.namespace, "default", metav1.ConditionFalse, ReasonInUse)
	if _, err := fabricSubnetOf(f.c, f.namespace, "default"); err != nil {
		t.Fatal("the Fabric Subnet must stay while addresses are in use")
	}
	if _, err := attachmentOf(f.c, f.namespace, "default"); err != nil {
		t.Fatal("the attachment must stay while addresses are in use")
	}
	if err := fabrictest.SetSubnetUsedAddresses(ctx, f.c, fabricName, 0); err != nil {
		t.Fatal(err)
	}
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireNoFabricSubnet(t, f.c, f.namespace, "default")
	if _, err := attachmentOf(f.c, f.namespace, "default"); !apierrors.IsNotFound(err) {
		t.Fatalf("the attachment must be deleted: %v", err)
	}
	var gone v1alpha1.Subnet
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: f.namespace, Name: "default"}, &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("the finalizer must be gone: %v", err)
	}
}

func TestSubnetDeletionOfARejectedSubnetLeavesForeignFabricObjects(t *testing.T) {
	f := newSubnetFixture(t)
	ctx := context.Background()
	foreign := fabric.Subnet(fabric.Meta{Name: FabricName(f.namespace, "default"), Annotations: map[string]string{AnnotationOwnerName: "x", AnnotationOwnerUID: "foreign"}}, fabric.SubnetSpec{CIDR: "10.250.0.0/24", Gateway: "10.250.0.1"})
	if err := f.c.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	subnet := f.create(t, f.namespace, "default", "10.100.1.0/24")
	reconcileSubnet(t, f.c, f.namespace, "default")
	if err := f.c.Delete(ctx, subnet); err != nil {
		t.Fatal(err)
	}
	reconcileSubnet(t, f.c, f.namespace, "default")
	if _, err := fabricSubnetOf(f.c, f.namespace, "default"); err != nil {
		t.Fatalf("the foreign Fabric Subnet must survive: %v", err)
	}
}

func TestSubnetTwoNamespacesSameNameGetDifferentFabricSubnets(t *testing.T) {
	f := newSubnetFixture(t)
	other := newNamespace(t, f.tenant, "stage")
	f.create(t, f.namespace, "default", "10.100.1.0/24")
	f.create(t, other, "default", "10.100.2.0/24")
	reconcileSubnet(t, f.c, f.namespace, "default")
	reconcileSubnet(t, f.c, other, "default")
	a, errA := fabricSubnetOf(f.c, f.namespace, "default")
	b, errB := fabricSubnetOf(f.c, other, "default")
	if errA != nil || errB != nil || a.GetName() == b.GetName() {
		t.Fatalf("Fabric Subnets %v %v", errA, errB)
	}
}
