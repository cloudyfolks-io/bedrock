package network

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func existingProvider(name, iface string) *unstructured.Unstructured {
	return fabric.ProviderNetwork(underlayMeta("ProviderNetwork", name), iface, gatewayNodes())
}

func existingVlan(name string, id int, provider string) *unstructured.Unstructured {
	return fabric.Vlan(underlayMeta("Vlan", name), id, provider)
}

func renderOver(t *testing.T, values map[string]string, existing ...*unstructured.Unstructured) map[string]*unstructured.Unstructured {
	t.Helper()
	objects, _, err := RenderUnderlayOver(configOf(t, values), existing)
	if err != nil {
		t.Fatal(err)
	}
	return byKindAndName(objects)
}

func poolsOf(first, second string) map[string]string {
	pool := func(name, cidr, gateway string) string {
		return `{"name":"` + name + `","interface":"bond0","cidr":"` + cidr + `","gateway":"` + gateway + `"}`
	}
	a, b := pool("a", "91.246.31.0/27", "91.246.31.1"), pool("b", "91.246.31.32/27", "91.246.31.33")
	if first == "b" {
		return map[string]string{"network.provider-networks": "[" + b + "," + a + "]"}
	}
	_ = second
	return map[string]string{"network.provider-networks": "[" + a + "," + b + "]"}
}

func TestRenderUnderlayNewInterfaceNameIgnoresPoolOrder(t *testing.T) {
	for _, first := range []string{"a", "b"} {
		got := renderOver(t, poolsOf(first, ""))
		if got["ProviderNetwork/a"] == nil || got["Vlan/a"] == nil || len(got) != 4 {
			t.Fatalf("first %s: %v", first, got)
		}
	}
}

func TestRenderUnderlayReusesTheExistingProviderNetworkOfTheInterface(t *testing.T) {
	got := renderOver(t, poolsOf("a", ""), existingProvider("zeta", "bond0"))
	if got["ProviderNetwork/zeta"] == nil || got["ProviderNetwork/a"] != nil {
		t.Fatalf("%v", got)
	}
	if nestedString(t, got["Vlan/a"], "spec", "provider") != "zeta" {
		t.Fatalf("vlan %v", got["Vlan/a"].Object["spec"])
	}
}

func TestRenderUnderlayKeepsTheProviderNetworkWhenTheFirstPoolIsRemoved(t *testing.T) {
	values := map[string]string{"network.provider-networks": `[{"name":"b","interface":"bond0","cidr":"91.246.31.32/27","gateway":"91.246.31.33"}]`}
	got := renderOver(t, values, existingProvider("a", "bond0"), existingVlan("a", 0, "a"))
	if got["ProviderNetwork/a"] == nil || got["Vlan/a"] == nil || got["ProviderNetwork/b"] != nil || got["Vlan/b"] != nil {
		t.Fatalf("%v", got)
	}
	if nestedString(t, got["Subnet/provider-b"], "spec", "vlan") != "a" {
		t.Fatal("the subnet follows the existing vlan")
	}
}

func TestRenderUnderlayKeepsTheProviderNetworkWhenSwitchingToBGP(t *testing.T) {
	values := bgpValues(map[string]string{"network.provider-networks": sharedInterfacePools})
	got := renderOver(t, values, existingProvider("public", "bond0"), existingVlan("public", 20, "public"))
	if got["ProviderNetwork/public"] == nil || got["ProviderNetwork/transit"] != nil {
		t.Fatalf("%v", got)
	}
	if nestedString(t, got["Vlan/transit"], "spec", "provider") != "public" {
		t.Fatalf("vlan %v", got["Vlan/transit"].Object["spec"])
	}
}

func TestRenderUnderlayNeverChangesTheProviderOfAnExistingVlan(t *testing.T) {
	ready := existingProvider("owner", "bond0")
	_ = unstructured.SetNestedStringSlice(ready.Object, []string{"node-1"}, "status", "readyNodes")
	existing := []*unstructured.Unstructured{existingProvider("a", "bond0"), ready, existingVlan("a", 0, "a"), existingVlan("b", 0, "owner")}
	got := renderOver(t, poolsOf("a", ""), existing...)
	if got["ProviderNetwork/owner"] == nil || nestedString(t, got["Vlan/b"], "spec", "provider") != "owner" || got["Vlan/a"] != nil {
		t.Fatalf("%v", got)
	}
	if got["ProviderNetwork/a"] != nil {
		t.Fatal("the carrier is the provider network that is ready on nodes")
	}
}

func TestRenderUnderlayRefusesAServicesPoolWithoutACL(t *testing.T) {
	cfg := Config{ProviderNetworks: []v1alpha1.ProviderNetworkSpec{{Name: "services", Role: v1alpha1.ProviderRoleServices, Interface: "bond0", Vlan: 20, CIDR: "not-a-cidr", Gateway: "100.66.0.1"}}}
	objects, _, err := RenderUnderlayOver(cfg, nil)
	if err == nil || len(objects) != 0 {
		t.Fatalf("objects %d err %v", len(objects), err)
	}
}

const irCentralPools = `[{"name":"public-a","interface":"bond0","cidr":"91.246.31.0/27","gateway":"91.246.31.1"},` +
	`{"name":"public-b","interface":"bond0","cidr":"91.246.31.32/27","gateway":"91.246.31.33"},` +
	`{"name":"services","role":"services","interface":"bond0","vlan":20,"cidr":"100.66.0.0/15","gateway":"100.66.0.1"},` +
	`{"name":"transit","role":"transit","interface":"bond0","vlan":30,"cidr":"100.65.0.0/16","gateway":"100.65.0.1"}]`

func TestRenderUnderlayIrCentralLayout(t *testing.T) {
	got := renderOver(t, map[string]string{"network.provider-networks": irCentralPools})
	if countKind(mapValues(got), "ProviderNetwork") != 1 || countKind(mapValues(got), "Vlan") != 3 || countKind(mapValues(got), "Subnet") != 4 {
		t.Fatalf("%v", got)
	}
	if nestedString(t, got["ProviderNetwork/public-a"], "spec", "defaultInterface") != "bond0" {
		t.Fatal("interface")
	}
	for vlan, id := range map[string]int64{"Vlan/public-a": 0, "Vlan/services": 20, "Vlan/transit": 30} {
		if got[vlan] == nil {
			t.Fatalf("missing %s", vlan)
		}
		if found, _, _ := unstructured.NestedInt64(got[vlan].Object, "spec", "id"); found != id || nestedString(t, got[vlan], "spec", "provider") != "public-a" {
			t.Fatalf("%s %v", vlan, got[vlan].Object["spec"])
		}
	}
	for subnet, vlan := range map[string]string{"public-a": "public-a", "public-b": "public-a", "services": "services", "transit": "transit"} {
		if nestedString(t, got["Subnet/provider-"+subnet], "spec", "vlan") != vlan {
			t.Fatalf("subnet %s", subnet)
		}
	}
	requireDropACL(t, got["Subnet/provider-services"])
}

func mapValues(m map[string]*unstructured.Unstructured) []*unstructured.Unstructured {
	out := make([]*unstructured.Unstructured, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
