package network

import (
	"context"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabrictest"
)

func servicesSettings(t *testing.T, pools string, extra map[string]string) {
	t.Helper()
	for key, value := range servicesValues(pools, extra) {
		setSetting(t, key, value)
	}
}

func servicesBGPSettings(t *testing.T) {
	t.Helper()
	for key, value := range bgpValues(servicesValues(servicesBGPPools, nil)) {
		setSetting(t, key, value)
	}
}

func identityOf(t *testing.T, c client.Client, network string) string {
	t.Helper()
	return getNetwork(t, c, network).Status.ServicesIdentity
}

func eipNamed(t *testing.T, c client.Client, name string) *unstructured.Unstructured {
	t.Helper()
	eip, err := getObject(context.Background(), c, fabric.OvnEipGVK, name)
	if err != nil {
		t.Fatal(err)
	}
	return eip
}

func objectGone(t *testing.T, c client.Client, kind string, name string) bool {
	t.Helper()
	gvk := fabric.OvnEipGVK
	if kind == "OvnSnatRule" {
		gvk = fabric.OvnSnatRuleGVK
	}
	_, err := getObject(context.Background(), c, gvk, name)
	return apierrors.IsNotFound(err)
}

func specOf(object *unstructured.Unstructured, field string) any {
	value, _, _ := unstructured.NestedFieldNoCopy(object.Object, "spec", field)
	return value
}

func subnetsOfVpc(t *testing.T, c client.Client, name string) []string {
	t.Helper()
	subnets, _, _ := unstructured.NestedStringSlice(vpcOf(t, c, name).Object, "spec", "extraExternalSubnets")
	return subnets
}

func TestNetworkReconcilerAttachesTheServicesPlane(t *testing.T) {
	c := cleanEnv(t)
	servicesSettings(t, servicesL2Pools, nil)
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	if got := identityOf(t, c, network.Name); got != "100.67.0.1" {
		t.Fatalf("identity %q", got)
	}
	if got := subnetsOfVpc(t, c, network.Name); !slices.Equal(got, []string{"provider-public", "provider-services"}) {
		t.Fatalf("subnets %v", got)
	}
	port := eipNamed(t, c, network.Name+"-provider-services")
	if specOf(port, "type") != "lrp" || specOf(port, "externalSubnet") != "provider-services" || specOf(port, "v4Ip") != nil {
		t.Fatalf("port %v", port.Object["spec"])
	}
	identity := eipNamed(t, c, network.Name+"-services-snat")
	if specOf(identity, "type") != "nat" || specOf(identity, "v4Ip") != "100.67.0.1" || specOf(identity, "externalSubnet") != "provider-services" {
		t.Fatalf("identity %v", identity.Object["spec"])
	}
	for i, cidr := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		rule, err := getObject(context.Background(), c, fabric.OvnSnatRuleGVK, network.Name+"-services-snat-"+string(rune('0'+i)))
		if err != nil {
			t.Fatal(err)
		}
		if specOf(rule, "ovnEip") != network.Name+"-services-snat" || specOf(rule, "vpc") != network.Name || specOf(rule, "v4IpCidr") != cidr {
			t.Fatalf("rule %v", rule.Object["spec"])
		}
		if _, uid, ok := OwnerOf(rule); !ok || uid != network.UID {
			t.Fatal("the rule has no owner")
		}
	}
}

func TestNetworkReconcilerGivesEveryNetworkItsOwnIdentityAndKeepsIt(t *testing.T) {
	c := cleanEnv(t)
	servicesSettings(t, servicesL2Pools, nil)
	first, second := createExternalNetwork(t, c), createExternalNetwork(t, c)
	reconcileNetwork(t, c, first.Name)
	reconcileNetwork(t, c, second.Name)
	a, b := identityOf(t, c, first.Name), identityOf(t, c, second.Name)
	if a != "100.67.0.1" || b != "100.67.0.2" {
		t.Fatalf("identities %q %q", a, b)
	}
	if err := c.Delete(context.Background(), eipNamed(t, c, first.Name+"-services-snat")); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, first.Name)
	reconcileNetwork(t, c, second.Name)
	if identityOf(t, c, first.Name) != a || identityOf(t, c, second.Name) != b {
		t.Fatal("an identity must not change")
	}
	if got := specOf(eipNamed(t, c, first.Name+"-services-snat"), "v4Ip"); got != a {
		t.Fatalf("the identity address was written again as %v", got)
	}
}

func TestNetworkReconcilerReadsTheAPIForIdentityUniqueness(t *testing.T) {
	c := cleanEnv(t)
	_, cfg := testEnv(t)
	scheme, err := testScheme()
	if err != nil {
		t.Fatal(err)
	}
	watching, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	blind := interceptor.NewClient(watching, interceptor.Funcs{List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, isNetworks := list.(*v1alpha1.NetworkList); isNetworks {
			return nil
		}
		if u, isUnstructured := list.(*unstructured.UnstructuredList); isUnstructured && u.GetKind() == "OvnEipList" {
			return nil
		}
		return cl.List(ctx, list, opts...)
	}})
	servicesSettings(t, servicesL2Pools, nil)
	first, second := createExternalNetwork(t, c), createExternalNetwork(t, c)
	reconciler := &NetworkReconciler{Client: blind, Reader: c}
	for _, name := range []string{first.Name, second.Name} {
		if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := identityOf(t, c, first.Name), identityOf(t, c, second.Name)
	if a == "" || b == "" || a == b {
		t.Fatalf("identities %q %q", a, b)
	}
}

func TestNetworkReconcilerSkipsIdentityAddressesThatAreTaken(t *testing.T) {
	c := cleanEnv(t)
	servicesSettings(t, servicesL2Pools, nil)
	foreign := fabric.OvnEip(fabric.Meta{Name: unique(t, "foreign")}, "provider-services", "100.67.0.1")
	if err := c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	holder := createExternalNetwork(t, c)
	got := getNetwork(t, c, holder.Name)
	got.Status.ServicesIdentity = "100.67.0.2"
	if err := c.Status().Update(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	if got := identityOf(t, c, network.Name); got != "100.67.0.3" {
		t.Fatalf("identity %q", got)
	}
}

func TestNetworkReconcilerAdoptsTheIdentityOfItsOwnEipWhenTheStatusIsLost(t *testing.T) {
	c := cleanEnv(t)
	servicesSettings(t, servicesL2Pools, nil)
	other := createExternalNetwork(t, c)
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, other.Name)
	reconcileNetwork(t, c, network.Name)
	want := identityOf(t, c, network.Name)
	lost := getNetwork(t, c, network.Name)
	lost.Status.ServicesIdentity = ""
	if err := c.Status().Update(context.Background(), &lost); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: other.Name}}); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, network.Name)
	if got := identityOf(t, c, network.Name); got != want {
		t.Fatalf("identity %q, want %q", got, want)
	}
}

func TestNetworkReconcilerAddsTheServicesPlaneToAnExistingVpcAndKeepsItsEntries(t *testing.T) {
	c := cleanEnv(t)
	setSetting(t, "network.provider-networks", `[{"name":"legacy","role":"platform","interface":"b","cidr":"192.0.2.0/24","gateway":"192.0.2.1"},`+servicesL2Pools[1:])
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	changeFabricObject(t, c, fabric.VpcGVK, network.Name, func(vpc *unstructured.Unstructured) {
		_ = unstructured.SetNestedStringSlice(vpc.Object, []string{"provider-legacy", "provider-public"}, "spec", "extraExternalSubnets")
		_ = unstructured.SetNestedSlice(vpc.Object, []any{route("0.0.0.0/0", "192.0.2.1")}, "spec", "staticRoutes")
	})
	servicesSettings(t, `[{"name":"legacy","role":"platform","interface":"b","cidr":"192.0.2.0/24","gateway":"192.0.2.1"},`+servicesL2Pools[1:], nil)
	reconcileNetwork(t, c, network.Name)
	if got := subnetsOfVpc(t, c, network.Name); !slices.Equal(got, []string{"provider-legacy", "provider-public", "provider-services"}) {
		t.Fatalf("subnets %v", got)
	}
	requireEqual(t, "routes", routesOfVpc(t, c, network.Name), []any{route("0.0.0.0/0", "192.0.2.1")})
}

func TestNetworkReconcilerAttachesTheServicesPlaneInBGPMode(t *testing.T) {
	c := cleanEnv(t)
	servicesBGPSettings(t)
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	if got := subnetsOfVpc(t, c, network.Name); !slices.Equal(got, []string{"provider-transit", "provider-services"}) {
		t.Fatalf("subnets %v", got)
	}
	if identityOf(t, c, network.Name) != "100.67.0.1" {
		t.Fatal("the identity is missing")
	}
	if eipNamed(t, c, network.Name+"-provider-services") == nil {
		t.Fatal("the router port is missing")
	}
}

func TestNetworkReconcilerWritesNoServicesObjectsForAPrivateNetwork(t *testing.T) {
	c := cleanEnv(t)
	servicesSettings(t, servicesL2Pools, nil)
	network := createNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	if identityOf(t, c, network.Name) != "" || !objectGone(t, c, "OvnEip", network.Name+"-services-snat") || !objectGone(t, c, "OvnEip", network.Name+"-provider-services") {
		t.Fatal("a private Network has no services plane")
	}
}

func TestNetworkReconcilerReportsAnIncompleteServicesPlane(t *testing.T) {
	c := cleanEnv(t)
	servicesSettings(t, servicesL2Pools, map[string]string{"network.services.identity-range": ""})
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	requireCondition(t, getNetwork(t, c, network.Name).Status.Conditions, ConditionExternalReady, metav1.ConditionFalse, ReasonServicesNotConfigured)
	if got := subnetsOfVpc(t, c, network.Name); !slices.Equal(got, []string{"provider-public"}) {
		t.Fatalf("subnets %v", got)
	}
}

func TestNetworkReconcilerReportsAFullIdentityRange(t *testing.T) {
	c := cleanEnv(t)
	servicesSettings(t, servicesL2Pools, map[string]string{"network.services.identity-range": "100.67.0.0/30"})
	first, second, third := createExternalNetwork(t, c), createExternalNetwork(t, c), createExternalNetwork(t, c)
	for _, name := range []string{first.Name, second.Name, third.Name} {
		reconcileNetwork(t, c, name)
	}
	requireCondition(t, getNetwork(t, c, third.Name).Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonNoPoolCapacity)
	if identityOf(t, c, first.Name) == "" || identityOf(t, c, second.Name) == "" {
		t.Fatal("the first two Networks fit")
	}
}

func TestNetworkReconcilerWaitsForTheServicesIdentity(t *testing.T) {
	c := cleanEnv(t)
	servicesSettings(t, servicesL2Pools, nil)
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	ctx := context.Background()
	if err := fabrictest.MarkVpcStandby(ctx, c, network.Name); err != nil {
		t.Fatal(err)
	}
	if err := fabrictest.CreateRouterPort(ctx, c, network.Name, "provider-public", "91.246.31.5"); err != nil {
		t.Fatal(err)
	}
	if err := fabrictest.MarkEipAllocated(ctx, c, network.Name+"-provider-services", "100.66.1.5"); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, network.Name)
	requireCondition(t, getNetwork(t, c, network.Name).Status.Conditions, ConditionExternalReady, metav1.ConditionFalse, ReasonPending)
	if err := fabrictest.MarkEipAllocated(ctx, c, network.Name+"-services-snat", "100.67.0.1"); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, network.Name)
	requireCondition(t, getNetwork(t, c, network.Name).Status.Conditions, ConditionExternalReady, metav1.ConditionTrue, ReasonConnected)
}

func TestNetworkReconcilerRefusesAForeignServicesIdentityObject(t *testing.T) {
	c := cleanEnv(t)
	servicesSettings(t, servicesL2Pools, nil)
	network := createExternalNetwork(t, c)
	if err := c.Create(context.Background(), fabric.OvnEip(fabric.Meta{Name: network.Name + "-services-snat"}, "provider-services", "100.67.0.200")); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, network.Name)
	requireCondition(t, getNetwork(t, c, network.Name).Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonFabricConflict)
	if !apierrors.IsNotFound(func() error { _, err := getObject(context.Background(), c, fabric.VpcGVK, network.Name); return err }()) {
		t.Fatal("the Vpc must wait for the conflict to go")
	}
}

func TestNetworkReconcilerDropsTheIdentityObjectsWhenTheServicesPlaneIsOff(t *testing.T) {
	c := cleanEnv(t)
	servicesSettings(t, servicesL2Pools, nil)
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	setSetting(t, "network.services.provider-network", "")
	reconcileNetwork(t, c, network.Name)
	for _, name := range []string{network.Name + "-services-snat"} {
		if !objectGone(t, c, "OvnEip", name) {
			t.Fatalf("%s must go", name)
		}
	}
	for i := range 3 {
		if !objectGone(t, c, "OvnSnatRule", network.Name+"-services-snat-"+string(rune('0'+i))) {
			t.Fatal("the rules must go")
		}
	}
	if objectGone(t, c, "OvnEip", network.Name+"-provider-services") {
		t.Fatal("the router port stays while the Vpc is attached")
	}
	if identityOf(t, c, network.Name) != "100.67.0.1" {
		t.Fatal("the recorded identity stays reserved")
	}
	if got := subnetsOfVpc(t, c, network.Name); !slices.Equal(got, []string{"provider-public", "provider-services"}) {
		t.Fatalf("subnets %v", got)
	}
}

func TestNetworkReconcilerDeletesTheServicesPlaneBeforeTheVpc(t *testing.T) {
	c := cleanEnv(t)
	servicesSettings(t, servicesL2Pools, nil)
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	if err := c.Delete(context.Background(), &v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: network.Name}}); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, network.Name)
	for _, name := range []string{network.Name + "-services-snat", network.Name + "-provider-services"} {
		if !objectGone(t, c, "OvnEip", name) {
			t.Fatalf("%s must go", name)
		}
	}
	if !objectGone(t, c, "OvnSnatRule", network.Name+"-services-snat-0") {
		t.Fatal("the rules must go")
	}
	var gone v1alpha1.Network
	if err := c.Get(context.Background(), client.ObjectKey{Name: network.Name}, &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("the Network must be gone: %v", err)
	}
}

func TestSweeperDeletesOrphanedSnatRules(t *testing.T) {
	rule := fabric.OvnSnatRule(fabric.Meta{Name: "gone-services-snat-0", Labels: ObjectLabels("Network", "gone", "acme", ""), Annotations: map[string]string{
		AnnotationOwnerName: "gone", AnnotationOwnerUID: "uid-gone",
	}}, "gone-services-snat", "gone", "10.0.0.0/8")
	rule.SetCreationTimestamp(metav1.Time{})
	found := FindOrphans([]*unstructured.Unstructured{rule}, map[OwnerKey]types.UID{}, metav1.Now().Time, 0)
	if len(found) != 1 || found[0].GVK != fabric.OvnSnatRuleGVK {
		t.Fatalf("orphans %+v", found)
	}
	if deletionRank[fabric.OvnSnatRuleGVK.Kind] != 0 || !slices.Contains(sweptKinds, fabric.OvnSnatRuleGVK) {
		t.Fatal("the sweeper must delete the rules first")
	}
}

func TestNetworkReconcilerKeepsTheServicesObjectsWhenASettingIsWrong(t *testing.T) {
	c := cleanEnv(t)
	servicesSettings(t, servicesL2Pools, nil)
	network := createExternalNetwork(t, c)
	reconcileNetwork(t, c, network.Name)
	setSetting(t, "network.services.identity-range", "100.70.0.0/16")
	reconcileNetwork(t, c, network.Name)
	requireCondition(t, getNetwork(t, c, network.Name).Status.Conditions, ConditionExternalReady, metav1.ConditionFalse, ReasonServicesNotConfigured)
	if objectGone(t, c, "OvnEip", network.Name+"-services-snat") || objectGone(t, c, "OvnSnatRule", network.Name+"-services-snat-0") {
		t.Fatal("a wrong Setting must not delete the identity objects")
	}
	if identityOf(t, c, network.Name) != "100.67.0.1" {
		t.Fatal("the identity must stay recorded")
	}
}
