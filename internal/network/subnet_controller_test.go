package network

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
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
	return readyNetworkOf(t, c, networkOf(unique(t, "net"), tenant, "10.100.0.0/16"))
}

func readyNetworkOf(t *testing.T, c client.Client, network *v1alpha1.Network) v1alpha1.Network {
	t.Helper()
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
	if result := reconcileSubnet(t, f.c, f.namespace, "default"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireSubnetReason(t, f.c, f.namespace, "default", metav1.ConditionFalse, ReasonWaitingForDefaultSecurityGroup)
	reconcileDefaultGroup(t, f.c, f.namespace)
	requireSubnetReasonAfter(t, f, ReasonWaitingForDefaultSecurityGroup)
	if err := fabrictest.MarkSecurityGroupProgrammed(context.Background(), f.c, DefaultSecurityGroupName(f.namespace)); err != nil {
		t.Fatal(err)
	}
	if result := reconcileSubnet(t, f.c, f.namespace, "default"); result.RequeueAfter != 0 {
		t.Fatalf("result %+v", result)
	}
	requireSubnetReason(t, f.c, f.namespace, "default", metav1.ConditionTrue, ReasonApplied)
}

func requireSubnetReasonAfter(t *testing.T, f subnetFixture, reason string) {
	t.Helper()
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireSubnetReason(t, f.c, f.namespace, "default", metav1.ConditionFalse, reason)
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

func TestSubnetReconcilerAcceptsSubnetsInEveryRangeOfTheNetwork(t *testing.T) {
	c := cleanEnv(t)
	tenant := unique(t, "acme")
	network := readyNetworkOf(t, c, networkOfRanges(unique(t, "net"), tenant, "10.100.0.0/16", "172.16.0.0/16", "192.168.0.0/24"))
	namespace := newNamespace(t, tenant, "prod")
	for name, cidr := range map[string]string{"ten": "10.100.1.0/24", "private": "172.16.7.0/24", "home": "192.168.0.0/25"} {
		requireAccepted(t, c, policySubnet(namespace, name, network.Name, cidr))
		reconcileSubnet(t, c, namespace, name)
		requireSubnetReason(t, c, namespace, name, metav1.ConditionFalse, ReasonFabricNotReady)
		if _, err := fabricSubnetOf(c, namespace, name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	requireAccepted(t, c, policySubnet(namespace, "stray", network.Name, "172.17.0.0/24"))
	reconcileSubnet(t, c, namespace, "stray")
	requireSubnetReason(t, c, namespace, "stray", metav1.ConditionFalse, ReasonCIDROutsideNetwork)
	requireNoFabricSubnet(t, c, namespace, "stray")
	got := getSubnet(t, c, namespace, "stray").Status.Conditions
	if message := meta.FindStatusCondition(got, v1alpha1.ConditionReady).Message; !strings.Contains(message, "one of the cidrs") {
		t.Fatalf("message %q", message)
	}
}

func TestSubnetReconcilerKeepsAnOldNetworkWithOnlyCIDRWorking(t *testing.T) {
	c := cleanEnv(t)
	tenant := unique(t, "acme")
	network := readyNetworkOf(t, c, networkOf(unique(t, "net"), tenant, "10.100.0.0/16"))
	if network.Spec.CIDR != "10.100.0.0/16" || len(network.Spec.CIDRs) != 0 {
		t.Fatalf("the Network must keep its old shape: %+v", network.Spec)
	}
	namespace := newNamespace(t, tenant, "prod")
	requireAccepted(t, c, policySubnet(namespace, "inside", network.Name, "10.100.1.0/24"))
	requireAccepted(t, c, policySubnet(namespace, "outside", network.Name, "172.16.1.0/24"))
	reconcileSubnet(t, c, namespace, "inside")
	reconcileSubnet(t, c, namespace, "outside")
	requireSubnetReason(t, c, namespace, "inside", metav1.ConditionFalse, ReasonFabricNotReady)
	requireSubnetReason(t, c, namespace, "outside", metav1.ConditionFalse, ReasonCIDROutsideNetwork)
	requireCondition(t, getNetwork(t, c, network.Name).Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonVpcReady)
}

func TestSubnetReconcilerRejectsCIDRInAReservedRange(t *testing.T) {
	f := newSubnetFixture(t)
	setSetting(t, "network.reserved-cidrs", "10.100.5.0/24")
	f.create(t, f.namespace, "reserved", "10.100.5.128/25")
	f.create(t, f.namespace, "free", "10.100.6.0/24")
	reconcileSubnet(t, f.c, f.namespace, "reserved")
	reconcileSubnet(t, f.c, f.namespace, "free")
	requireSubnetReason(t, f.c, f.namespace, "reserved", metav1.ConditionFalse, ReasonCIDRReserved)
	requireNoFabricSubnet(t, f.c, f.namespace, "reserved")
	got := getSubnet(t, f.c, f.namespace, "reserved").Status.Conditions
	if message := meta.FindStatusCondition(got, v1alpha1.ConditionReady).Message; !strings.Contains(message, "reserved 10.100.5.0/24") {
		t.Fatalf("message %q", message)
	}
	requireSubnetReason(t, f.c, f.namespace, "free", metav1.ConditionFalse, ReasonFabricNotReady)
}

func TestSubnetReconcilerRejectsCIDROutsideTheAllowedRanges(t *testing.T) {
	c := cleanEnv(t)
	tenant := unique(t, "acme")
	network := readyNetworkOf(t, c, networkOfRanges(unique(t, "net"), tenant, "10.100.0.0/16", "172.16.0.0/16", "203.0.0.0/16"))
	namespace := newNamespace(t, tenant, "prod")
	for name, cidr := range map[string]string{"ten": "10.100.1.0/24", "private": "172.16.1.0/24", "public": "203.0.1.0/24"} {
		requireAccepted(t, c, policySubnet(namespace, name, network.Name, cidr))
		reconcileSubnet(t, c, namespace, name)
	}
	requireSubnetReason(t, c, namespace, "ten", metav1.ConditionFalse, ReasonFabricNotReady)
	requireSubnetReason(t, c, namespace, "private", metav1.ConditionFalse, ReasonFabricNotReady)
	requireSubnetReason(t, c, namespace, "public", metav1.ConditionFalse, ReasonCIDROutsideAllowedRanges)
	requireNoFabricSubnet(t, c, namespace, "public")
	got := getSubnet(t, c, namespace, "public").Status.Conditions
	if message := meta.FindStatusCondition(got, v1alpha1.ConditionReady).Message; !strings.Contains(message, "network.subnet-allowed-ranges") || !strings.Contains(message, "10.0.0.0/8") {
		t.Fatalf("message %q", message)
	}

	setSetting(t, "network.subnet-allowed-ranges", "10.0.0.0/8")
	reconcileSubnet(t, c, namespace, "private")
	requireSubnetReason(t, c, namespace, "private", metav1.ConditionFalse, ReasonCIDROutsideAllowedRanges)
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
	requireSubnetReason(t, f.c, foreign, "default", metav1.ConditionFalse, ReasonNetworkNotOwned)
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

func requireDHCP(t *testing.T, c client.Client, namespace, name string, mtu int64, options string) {
	t.Helper()
	fabricSubnet, err := fabricSubnetOf(c, namespace, name)
	if err != nil {
		t.Fatal(err)
	}
	got, found, _ := unstructured.NestedInt64(fabricSubnet.Object, "spec", "mtu")
	if mtu == 0 && found || mtu != 0 && got != mtu {
		t.Fatalf("mtu %d (set %v), want %d", got, found, mtu)
	}
	gotOptions, optionsFound, _ := unstructured.NestedString(fabricSubnet.Object, "spec", "dhcpV4Options")
	if gotOptions != options || optionsFound != (options != "") {
		t.Fatalf("options %q (set %v), want %q", gotOptions, optionsFound, options)
	}
}

func TestSubnetDNSServersFromSettingsReachDHCP(t *testing.T) {
	f := newSubnetFixture(t)
	setSetting(t, "network.dns-servers", "1.1.1.1,8.8.8.8")
	f.create(t, f.namespace, "default", "10.100.1.0/24")
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireDHCP(t, f.c, f.namespace, "default", 0, "dns_server=1.1.1.1;8.8.8.8")
}

func TestSubnetWithoutSettingsGetsNoMTUAndNoDHCPOptions(t *testing.T) {
	f := newSubnetFixture(t)
	f.create(t, f.namespace, "default", "10.100.1.0/24")
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireDHCP(t, f.c, f.namespace, "default", 0, "")
}

func TestSubnetMTUFieldNeedsNoSetting(t *testing.T) {
	f := newSubnetFixture(t)
	subnet := policySubnet(f.namespace, "default", f.network.Name, "10.100.1.0/24")
	subnet.Spec.MTU = ptr.To(int32(1400))
	requireAccepted(t, f.c, subnet)
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireDHCP(t, f.c, f.namespace, "default", 1400, "mtu=1400")
}

func TestSubnetMTUSettingReachesTheFabricSubnet(t *testing.T) {
	f := newSubnetFixture(t)
	setSetting(t, "network.mtu", "9000")
	f.create(t, f.namespace, "default", "10.100.1.0/24")
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireDHCP(t, f.c, f.namespace, "default", 9000, "mtu=9000")
}

func TestSubnetFieldsWinOverSettings(t *testing.T) {
	f := newSubnetFixture(t)
	setSetting(t, "network.mtu", "9000")
	setSetting(t, "network.dns-servers", "1.1.1.1,8.8.8.8")
	subnet := policySubnet(f.namespace, "default", f.network.Name, "10.100.1.0/24")
	subnet.Spec.Gateway = "10.100.1.254"
	subnet.Spec.MTU = ptr.To(int32(1400))
	subnet.Spec.DNSServers = []string{"100.66.0.53", "100.66.0.54"}
	requireAccepted(t, f.c, subnet)
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireDHCP(t, f.c, f.namespace, "default", 1400, "mtu=1400,dns_server=100.66.0.53;100.66.0.54")
}

func TestSubnetFollowsASettingChange(t *testing.T) {
	f := newSubnetFixture(t)
	f.create(t, f.namespace, "default", "10.100.1.0/24")
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireDHCP(t, f.c, f.namespace, "default", 0, "")
	setSetting(t, "network.mtu", "1400")
	setSetting(t, "network.dns-servers", "9.9.9.9")
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireDHCP(t, f.c, f.namespace, "default", 1400, "mtu=1400,dns_server=9.9.9.9")
	setSetting(t, "network.mtu", "")
	reconcileSubnet(t, f.c, f.namespace, "default")
	requireDHCP(t, f.c, f.namespace, "default", 0, "dns_server=9.9.9.9")
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

func TestSubnetReconcilerIgnoresSubnetsOfOtherTenantsForOverlap(t *testing.T) {
	c := cleanEnv(t)
	tenant := unique(t, "zz")
	network := readyNetwork(t, c, tenant)
	mine := newNamespace(t, tenant, "prod")
	intruder := newNamespace(t, unique(t, "aa"), "prod")
	requireAccepted(t, c, policySubnet(intruder, "intruder", network.Name, "10.100.1.0/24"))
	requireAccepted(t, c, policySubnet(mine, "mine", network.Name, "10.100.1.0/24"))
	reconcileSubnet(t, c, intruder, "intruder")
	reconcileSubnet(t, c, mine, "mine")
	requireSubnetReason(t, c, intruder, "intruder", metav1.ConditionFalse, ReasonNetworkNotOwned)
	requireNoFabricSubnet(t, c, intruder, "intruder")
	requireSubnetReason(t, c, mine, "mine", metav1.ConditionFalse, ReasonFabricNotReady)
}

func reservedFixture(t *testing.T, cidr string) subnetFixture {
	t.Helper()
	c := cleanEnv(t)
	publicSettings(t)
	tenant := unique(t, "acme")
	network := networkOf(unique(t, "net"), tenant, cidr)
	requireAccepted(t, c, network)
	reconcileNetwork(t, c, network.Name)
	if err := fabrictest.MarkVpcStandby(context.Background(), c, network.Name); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, network.Name)
	return subnetFixture{c: c, tenant: tenant, network: *network, namespace: newNamespace(t, tenant, "prod")}
}

func TestSubnetReconcilerRejectsProviderNetworkRanges(t *testing.T) {
	setSetting(t, "network.subnet-allowed-ranges", "0.0.0.0/0")
	f := reservedFixture(t, "91.246.0.0/16")
	f.create(t, f.namespace, "clash", "91.246.31.0/24")
	f.create(t, f.namespace, "fine", "91.246.40.0/24")
	reconcileSubnet(t, f.c, f.namespace, "clash")
	reconcileSubnet(t, f.c, f.namespace, "fine")
	requireSubnetReason(t, f.c, f.namespace, "clash", metav1.ConditionFalse, ReasonCIDRReserved)
	requireNoFabricSubnet(t, f.c, f.namespace, "clash")
	requireSubnetReason(t, f.c, f.namespace, "fine", metav1.ConditionFalse, ReasonFabricNotReady)
}

func TestSubnetReconcilerRejectsTheJoinSubnet(t *testing.T) {
	setSetting(t, "network.reserved-cidrs", "192.0.2.0/24")
	f := reservedFixture(t, "100.64.0.0/16")
	f.create(t, f.namespace, "clash", "100.64.1.0/24")
	reconcileSubnet(t, f.c, f.namespace, "clash")
	requireSubnetReason(t, f.c, f.namespace, "clash", metav1.ConditionFalse, ReasonCIDRReserved)
	requireNoFabricSubnet(t, f.c, f.namespace, "clash")
}
