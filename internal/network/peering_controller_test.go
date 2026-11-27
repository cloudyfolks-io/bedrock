package network

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabrictest"
)

func reconcilePeering(t *testing.T, c client.Client, name string) reconcile.Result {
	t.Helper()
	result, err := (&NetworkPeeringReconciler{Client: c}).Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func getPeering(t *testing.T, c client.Client, name string) v1alpha1.NetworkPeering {
	t.Helper()
	var peering v1alpha1.NetworkPeering
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &peering); err != nil {
		t.Fatal(err)
	}
	return peering
}

func createPeering(t *testing.T, c client.Client, name, a, b, connect string) {
	t.Helper()
	requireAccepted(t, c, &v1alpha1.NetworkPeering{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.NetworkPeeringSpec{NetworkRefs: []string{a, b}, ConnectCIDR: connect},
	})
}

func createRangedNetwork(t *testing.T, c client.Client, cidr string) string {
	t.Helper()
	network := networkOf(unique(t, "peer"), "acme", cidr)
	requireAccepted(t, c, network)
	return network.Name
}

func TestPeeringReconcilerRecordsDistinctConnectCIDRs(t *testing.T) {
	c := cleanEnv(t)
	a, b, d := createRangedNetwork(t, c, "10.1.0.0/16"), createRangedNetwork(t, c, "10.2.0.0/16"), createRangedNetwork(t, c, "10.3.0.0/16")
	createPeering(t, c, "peer-ab", a, b, "")
	createPeering(t, c, "peer-ad", a, d, "")
	reconcilePeering(t, c, "peer-ab")
	reconcilePeering(t, c, "peer-ad")
	first, second := getPeering(t, c, "peer-ab").Status.ConnectCIDR, getPeering(t, c, "peer-ad").Status.ConnectCIDR
	if first != "169.254.0.0/30" || second != "169.254.0.4/30" {
		t.Fatalf("connect CIDRs %q %q", first, second)
	}
	reconcilePeering(t, c, "peer-ab")
	if getPeering(t, c, "peer-ab").Status.ConnectCIDR != first {
		t.Fatal("the recorded connect CIDR must not change")
	}
}

func TestPeeringReconcilerAllocatesFromTheSetting(t *testing.T) {
	c := cleanEnv(t)
	setSetting(t, "network.peering.range", "10.77.0.0/24")
	a, b := createRangedNetwork(t, c, "10.1.0.0/16"), createRangedNetwork(t, c, "10.2.0.0/16")
	createPeering(t, c, "peer-ab", a, b, "")
	reconcilePeering(t, c, "peer-ab")
	if got := getPeering(t, c, "peer-ab").Status.ConnectCIDR; got != "10.77.0.0/30" {
		t.Fatalf("connect CIDR %q", got)
	}
}

func TestPeeringReconcilerUsesTheConnectCIDROfTheSpec(t *testing.T) {
	c := cleanEnv(t)
	a, b := createRangedNetwork(t, c, "10.1.0.0/16"), createRangedNetwork(t, c, "10.2.0.0/16")
	createPeering(t, c, "peer-ab", a, b, "169.254.9.0/30")
	reconcilePeering(t, c, "peer-ab")
	if got := getPeering(t, c, "peer-ab").Status.ConnectCIDR; got != "169.254.9.0/30" {
		t.Fatalf("connect CIDR %q", got)
	}
}

func TestPeeringReconcilerRefusesAConnectCIDRThatAnOlderPeeringHolds(t *testing.T) {
	c := cleanEnv(t)
	a, b, d := createRangedNetwork(t, c, "10.1.0.0/16"), createRangedNetwork(t, c, "10.2.0.0/16"), createRangedNetwork(t, c, "10.3.0.0/16")
	createPeering(t, c, "peer-ab", a, b, "169.254.9.0/30")
	createPeering(t, c, "peer-ad", a, d, "169.254.9.0/30")
	reconcilePeering(t, c, "peer-ab")
	reconcilePeering(t, c, "peer-ad")
	refused := getPeering(t, c, "peer-ad")
	requireCondition(t, refused.Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonConnectCIDRInUse)
	if refused.Status.ConnectCIDR != "" || getPeering(t, c, "peer-ab").Status.ConnectCIDR != "169.254.9.0/30" {
		t.Fatalf("status %+v", refused.Status)
	}
}

func TestPeeringReconcilerRefusesASecondPeeringOfTheSamePair(t *testing.T) {
	c := cleanEnv(t)
	a, b := createRangedNetwork(t, c, "10.1.0.0/16"), createRangedNetwork(t, c, "10.2.0.0/16")
	createPeering(t, c, "peer-ab", a, b, "")
	createPeering(t, c, "peer-ba", b, a, "")
	reconcilePeering(t, c, "peer-ab")
	reconcilePeering(t, c, "peer-ba")
	requireCondition(t, getPeering(t, c, "peer-ba").Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonPeeringExists)
}

func TestPeeringReconcilerReadsTheAPIForUniqueness(t *testing.T) {
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
		if _, isPeerings := list.(*v1alpha1.NetworkPeeringList); isPeerings {
			return nil
		}
		return cl.List(ctx, list, opts...)
	}})
	a, b, d := createRangedNetwork(t, c, "10.1.0.0/16"), createRangedNetwork(t, c, "10.2.0.0/16"), createRangedNetwork(t, c, "10.3.0.0/16")
	createPeering(t, c, "peer-ab", a, b, "")
	createPeering(t, c, "peer-ad", a, d, "")
	reconciler := &NetworkPeeringReconciler{Client: blind, Reader: c}
	for _, name := range []string{"peer-ab", "peer-ad"} {
		if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	first, second := getPeering(t, c, "peer-ab").Status.ConnectCIDR, getPeering(t, c, "peer-ad").Status.ConnectCIDR
	if first == "" || second == "" || first == second {
		t.Fatalf("connect CIDRs %q %q", first, second)
	}
}

func TestPeeringReconcilerNeedsBothNetworks(t *testing.T) {
	c := cleanEnv(t)
	a := createRangedNetwork(t, c, "10.1.0.0/16")
	createPeering(t, c, "peer-ab", a, "ghost", "")
	reconcilePeering(t, c, "peer-ab")
	peering := getPeering(t, c, "peer-ab")
	requireCondition(t, peering.Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonNetworkNotFound)
	if peering.Status.ConnectCIDR == "" {
		t.Fatal("the connect CIDR is recorded before the Networks exist")
	}
}

func TestPeeringReconcilerIsReadyWhenBothVpcsProgrammedIt(t *testing.T) {
	c := cleanEnv(t)
	a, b := createRangedNetwork(t, c, "10.1.0.0/16"), createRangedNetwork(t, c, "10.2.0.0/16")
	createPeering(t, c, "peer-ab", a, b, "")
	reconcilePeering(t, c, "peer-ab")
	reconcileNetwork(t, c, a)
	reconcileNetwork(t, c, b)
	if result := reconcilePeering(t, c, "peer-ab"); result.RequeueAfter != waitRequeue {
		t.Fatalf("result %+v", result)
	}
	requireCondition(t, getPeering(t, c, "peer-ab").Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonPending)
	if err := fabrictest.MarkVpcPeered(context.Background(), c, a, b); err != nil {
		t.Fatal(err)
	}
	reconcilePeering(t, c, "peer-ab")
	requireCondition(t, getPeering(t, c, "peer-ab").Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionFalse, ReasonPending)
	if err := fabrictest.MarkVpcPeered(context.Background(), c, b, a); err != nil {
		t.Fatal(err)
	}
	if result := reconcilePeering(t, c, "peer-ab"); result.RequeueAfter != 0 {
		t.Fatalf("result %+v", result)
	}
	requireCondition(t, getPeering(t, c, "peer-ab").Status.Conditions, v1alpha1.ConditionReady, metav1.ConditionTrue, ReasonProgrammed)
}

func peeringsOfVpc(t *testing.T, c client.Client, name string) []any {
	t.Helper()
	peerings, _, _ := unstructured.NestedSlice(vpcOf(t, c, name).Object, "spec", "vpcPeerings")
	return peerings
}

func routesOfVpc(t *testing.T, c client.Client, name string) []any {
	t.Helper()
	routes, _, _ := unstructured.NestedSlice(vpcOf(t, c, name).Object, "spec", "staticRoutes")
	return routes
}

func route(cidr, nextHop string) any {
	return map[string]any{"policy": "policyDst", "cidr": cidr, "nextHopIP": nextHop}
}

func peering(remote, local string) any {
	return map[string]any{"remoteVpc": remote, "localConnectIP": local}
}

func requireEqual(t *testing.T, label string, got, want []any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for i := range got {
		if !equalAny(got[i], want[i]) {
			t.Fatalf("%s: got %v, want %v", label, got, want)
		}
	}
}

func equalAny(a, b any) bool {
	x, y := a.(map[string]any), b.(map[string]any)
	if len(x) != len(y) {
		return false
	}
	for key, value := range x {
		if y[key] != value {
			return false
		}
	}
	return true
}

func TestNetworkReconcilerPeersBothSides(t *testing.T) {
	c := cleanEnv(t)
	a, b := createRangedNetwork(t, c, "10.1.0.0/16"), createRangedNetwork(t, c, "10.2.0.0/16")
	createPeering(t, c, "peer-ab", a, b, "")
	reconcilePeering(t, c, "peer-ab")
	reconcileNetwork(t, c, a)
	reconcileNetwork(t, c, b)
	requireEqual(t, "peerings of a", peeringsOfVpc(t, c, a), []any{peering(b, "169.254.0.1/30")})
	requireEqual(t, "routes of a", routesOfVpc(t, c, a), []any{route("10.2.0.0/16", "169.254.0.2")})
	requireEqual(t, "peerings of b", peeringsOfVpc(t, c, b), []any{peering(a, "169.254.0.2/30")})
	requireEqual(t, "routes of b", routesOfVpc(t, c, b), []any{route("10.1.0.0/16", "169.254.0.1")})
}

func TestNetworkReconcilerWritesNothingForAPeeringWithoutAConnectCIDR(t *testing.T) {
	c := cleanEnv(t)
	a, b := createRangedNetwork(t, c, "10.1.0.0/16"), createRangedNetwork(t, c, "10.2.0.0/16")
	createPeering(t, c, "peer-ab", a, b, "")
	reconcileNetwork(t, c, a)
	if len(peeringsOfVpc(t, c, a)) != 0 || len(routesOfVpc(t, c, a)) != 0 {
		t.Fatal("a peering without a connect CIDR must add nothing")
	}
}

func TestNetworkReconcilerRemovesThePeeringFromBothVpcsOnDeletion(t *testing.T) {
	c := cleanEnv(t)
	a, b := createRangedNetwork(t, c, "10.1.0.0/16"), createRangedNetwork(t, c, "10.2.0.0/16")
	createPeering(t, c, "peer-ab", a, b, "")
	reconcilePeering(t, c, "peer-ab")
	reconcileNetwork(t, c, a)
	reconcileNetwork(t, c, b)
	if len(peeringsOfVpc(t, c, a)) != 1 || len(peeringsOfVpc(t, c, b)) != 1 {
		t.Fatal("the peering must be programmed first")
	}
	if err := c.Delete(context.Background(), &v1alpha1.NetworkPeering{ObjectMeta: metav1.ObjectMeta{Name: "peer-ab"}}); err != nil {
		t.Fatal(err)
	}
	reconcileNetwork(t, c, a)
	reconcileNetwork(t, c, b)
	for _, name := range []string{a, b} {
		if len(peeringsOfVpc(t, c, name)) != 0 || len(routesOfVpc(t, c, name)) != 0 {
			t.Fatalf("the Vpc %s keeps a peering or a route", name)
		}
	}
}

func TestNetworkReconcilerWritesTheStaticRoutesOfTheNetwork(t *testing.T) {
	c := cleanEnv(t)
	network := networkOf(unique(t, "net"), "acme", "10.100.0.0/16")
	network.Spec.StaticRoutes = []v1alpha1.NetworkStaticRoute{{CIDR: "10.50.0.0/16", NextHop: "10.100.0.9"}}
	requireAccepted(t, c, network)
	reconcileNetwork(t, c, network.Name)
	requireEqual(t, "routes", routesOfVpc(t, c, network.Name), []any{route("10.50.0.0/16", "10.100.0.9")})
}

func TestNetworkReconcilerKeepsTheDefaultRouteBesideThePeerRoutes(t *testing.T) {
	c := cleanEnv(t)
	publicSettings(t)
	external := networkOf(unique(t, "net"), "acme", "10.100.0.0/16")
	external.Spec.EnableExternal = true
	requireAccepted(t, c, external)
	other := createRangedNetwork(t, c, "10.2.0.0/16")
	createPeering(t, c, "peer-ab", external.Name, other, "")
	reconcilePeering(t, c, "peer-ab")
	reconcileNetwork(t, c, external.Name)
	requireEqual(t, "routes", routesOfVpc(t, c, external.Name), []any{route("0.0.0.0/0", "91.246.31.1"), route("10.2.0.0/16", "169.254.0.2")})
	if enabled, _, _ := unstructured.NestedBool(vpcOf(t, c, external.Name).Object, "spec", "enableExternal"); !enabled {
		t.Fatal("the external connection must stay")
	}
}

func TestPeeringReconcilerKeepsTheConnectCIDROnAConfigError(t *testing.T) {
	c := cleanEnv(t)
	a, b := createRangedNetwork(t, c, "10.1.0.0/16"), createRangedNetwork(t, c, "10.2.0.0/16")
	createPeering(t, c, "peer-ab", a, b, "")
	reconcilePeering(t, c, "peer-ab")
	recorded := getPeering(t, c, "peer-ab").Status.ConnectCIDR
	if recorded == "" {
		t.Fatal("no connect CIDR recorded")
	}
	broken := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: "network.peering.range"}, Spec: v1alpha1.SettingSpec{Value: "not-a-cidr"}}
	if err := c.Create(context.Background(), broken); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), broken) })
	if _, err := (&NetworkPeeringReconciler{Client: c}).Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "peer-ab"}}); err == nil {
		t.Fatal("a config error must be returned for a requeue")
	}
	if got := getPeering(t, c, "peer-ab").Status.ConnectCIDR; got != recorded {
		t.Fatalf("connect CIDR %q, want %q", got, recorded)
	}
}
