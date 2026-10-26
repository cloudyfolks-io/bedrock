package network

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const sharedVlanPools = `[{"name":"public-a","interface":"bond0","cidr":"91.246.31.0/27","gateway":"91.246.31.1"},{"name":"public-b","interface":"bond0","cidr":"91.246.31.32/27","gateway":"91.246.31.33"}]`

const sharedInterfacePools = `[{"name":"transit","interface":"bond0","vlan":30,"cidr":"100.65.0.0/16","gateway":"100.65.0.1"},{"name":"public","interface":"bond0","vlan":20,"cidr":"91.246.31.0/24","gateway":"91.246.31.1"}]`

func countKind(objects []*unstructured.Unstructured, kind string) int {
	count := 0
	for _, o := range objects {
		if o.GetKind() == kind {
			count++
		}
	}
	return count
}

func TestRenderUnderlayTwoPoolsOnOneInterfaceAndVlanShareTheProviderNetworkAndVlan(t *testing.T) {
	objects, warnings := RenderUnderlay(configOf(t, map[string]string{"network.provider-networks": sharedVlanPools}))
	if len(warnings) != 0 {
		t.Fatalf("warnings %v", warnings)
	}
	if countKind(objects, "ProviderNetwork") != 1 || countKind(objects, "Vlan") != 1 || countKind(objects, "Subnet") != 2 {
		t.Fatalf("provider networks %d vlans %d subnets %d", countKind(objects, "ProviderNetwork"), countKind(objects, "Vlan"), countKind(objects, "Subnet"))
	}
	got := byKindAndName(objects)
	vlan := got["Vlan/public-a"]
	if vlan == nil || got["ProviderNetwork/public-a"] == nil {
		t.Fatalf("the first pool names the shared objects: %v", got)
	}
	if nestedString(t, vlan, "spec", "provider") != "public-a" {
		t.Fatalf("vlan %v", vlan.Object["spec"])
	}
	for _, name := range []string{"Subnet/provider-public-a", "Subnet/provider-public-b"} {
		if nestedString(t, got[name], "spec", "vlan") != "public-a" {
			t.Fatalf("%s vlan %v", name, got[name].Object["spec"])
		}
	}
	if nestedString(t, got["Subnet/provider-public-b"], "spec", "cidrBlock") != "91.246.31.32/27" {
		t.Fatal("the second pool keeps its own cidr")
	}
}

func TestRenderUnderlayTwoVlansOnOneInterfaceShareTheProviderNetwork(t *testing.T) {
	objects, _ := RenderUnderlay(configOf(t, map[string]string{"network.provider-networks": sharedInterfacePools}))
	if countKind(objects, "ProviderNetwork") != 1 || countKind(objects, "Vlan") != 2 {
		t.Fatalf("provider networks %d vlans %d", countKind(objects, "ProviderNetwork"), countKind(objects, "Vlan"))
	}
	got := byKindAndName(objects)
	for _, name := range []string{"Vlan/transit", "Vlan/public"} {
		if nestedString(t, got[name], "spec", "provider") != "transit" {
			t.Fatalf("%s %v", name, got[name].Object["spec"])
		}
	}
	if nestedString(t, got["Subnet/provider-public"], "spec", "vlan") != "public" {
		t.Fatal("each pool keeps its own vlan")
	}
}

func TestRenderUnderlayBGPTransitAndServicesOnOneInterfaceShareTheProviderNetwork(t *testing.T) {
	values := bgpValues(servicesValues(servicesBGPPools, nil))
	objects, _ := RenderUnderlay(configOf(t, values))
	got := byKindAndName(objects)
	if countKind(objects, "ProviderNetwork") != 1 || got["ProviderNetwork/transit"] == nil {
		t.Fatalf("provider networks %d", countKind(objects, "ProviderNetwork"))
	}
	if nestedString(t, got["Vlan/services"], "spec", "provider") != "transit" {
		t.Fatalf("vlan %v", got["Vlan/services"].Object["spec"])
	}
}
