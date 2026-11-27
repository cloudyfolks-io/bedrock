package network

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func irPools(providerName string) string {
	named := ""
	if providerName != "" {
		named = `,"providerName":"` + providerName + `"`
	}
	pool := func(name string, vlan, role, cidr, gateway string) string {
		return `{"name":"` + name + `","interface":"bond0"` + vlan + role + named + `,"cidr":"` + cidr + `","gateway":"` + gateway + `"}`
	}
	return "[" + strings.Join([]string{
		pool("platform", "", "", "10.9.0.0/24", "10.9.0.1"),
		pool("public", "", "", "91.246.31.0/24", "91.246.31.1"),
		pool("services", `,"vlan":20`, `,"role":"services"`, "100.66.0.0/15", "100.66.0.1"),
		pool("transit", `,"vlan":30`, "", "100.65.0.0/16", "100.65.0.1"),
	}, ",") + "]"
}

func vlanIDs(t *testing.T, objects []*unstructured.Unstructured) map[string]int64 {
	t.Helper()
	ids := map[string]int64{}
	for _, o := range objects {
		if o.GetKind() == "Vlan" {
			id, _, _ := unstructured.NestedInt64(o.Object, "spec", "id")
			ids[o.GetName()] = id
		}
	}
	return ids
}

func TestRenderUnderlayUsesTheExplicitCarrierName(t *testing.T) {
	objects, _ := RenderUnderlay(configOf(t, map[string]string{"network.provider-networks": irPools("public")}))
	got := byKindAndName(objects)
	if countKind(objects, "ProviderNetwork") != 1 || got["ProviderNetwork/public"] == nil {
		t.Fatalf("%v", got)
	}
	if nestedString(t, got["ProviderNetwork/public"], "spec", "defaultInterface") != "bond0" {
		t.Fatal("the carrier sits on bond0")
	}
	ids := vlanIDs(t, objects)
	if len(ids) != 3 || ids["platform"] != 0 || ids["services"] != 20 || ids["transit"] != 30 {
		t.Fatalf("vlans %v", ids)
	}
	for name := range ids {
		if nestedString(t, got["Vlan/"+name], "spec", "provider") != "public" {
			t.Fatalf("vlan %s %v", name, got["Vlan/"+name].Object["spec"])
		}
	}
}

func TestRenderUnderlayBGPKeepsTheIRLegBridge(t *testing.T) {
	values := bgpValues(map[string]string{"network.provider-networks": irPools("public")})
	objects, _ := RenderUnderlay(configOf(t, values))
	got := byKindAndName(objects)
	if got["ProviderNetwork/public"] == nil || countKind(objects, "ProviderNetwork") != 1 {
		t.Fatalf("%v", got)
	}
	ids := vlanIDs(t, objects)
	if len(ids) != 2 || ids["services"] != 20 || ids["transit"] != 30 {
		t.Fatalf("vlans %v", ids)
	}
	bridge := "br-" + got["ProviderNetwork/public"].GetName()
	if bridge+".20" != "br-public.20" || bridge+".30" != "br-public.30" {
		t.Fatalf("bridge %s", bridge)
	}
}

func TestRenderUnderlayExistingProviderNetworkBeatsTheExplicitName(t *testing.T) {
	values := map[string]string{"network.provider-networks": irPools("public")}
	got := renderOver(t, values, existingProvider("services", "bond0"))
	if got["ProviderNetwork/services"] == nil || got["ProviderNetwork/public"] != nil {
		t.Fatalf("%v", got)
	}
}

func TestRenderUnderlayWithoutExplicitNameKeepsTheLowestName(t *testing.T) {
	objects, _ := RenderUnderlay(configOf(t, map[string]string{"network.provider-networks": irPools("")}))
	got := byKindAndName(objects)
	if got["ProviderNetwork/platform"] == nil || got["ProviderNetwork/public"] != nil {
		t.Fatalf("%v", got)
	}
}
