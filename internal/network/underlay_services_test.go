package network

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func servicesSubnetOf(t *testing.T, values map[string]string) *unstructured.Unstructured {
	t.Helper()
	objects, _ := RenderUnderlay(configOf(t, values))
	subnet := byKindAndName(objects)["Subnet/provider-services"]
	if subnet == nil {
		t.Fatal("no services subnet rendered")
	}
	return subnet
}

func requireDropACL(t *testing.T, subnet *unstructured.Unstructured) {
	t.Helper()
	acls, _, _ := unstructured.NestedSlice(subnet.Object, "spec", "acls")
	if len(acls) != 1 {
		t.Fatalf("acls %v", acls)
	}
	acl := acls[0].(map[string]any)
	match, _ := acl["match"].(string)
	want := "ip4.src == 100.66.0.0/15 && ip4.src != 100.66.0.0/24 && ip4.dst == 100.66.0.0/15 && ip4.dst != 100.66.0.0/24"
	if acl["direction"] != "to-lport" || acl["priority"] != int64(2000) || acl["action"] != "drop" || match != want {
		t.Fatalf("acl %v", acl)
	}
}

func TestRenderUnderlayL2GivesTheServicesSubnetTheTenantDropACL(t *testing.T) {
	subnet := servicesSubnetOf(t, servicesValues(servicesL2Pools, nil))
	requireDropACL(t, subnet)
	if vlan, _, _ := unstructured.NestedString(subnet.Object, "spec", "vlan"); vlan != "services" {
		t.Fatalf("vlan %q", vlan)
	}
}

func TestRenderUnderlayBGPBacksTheServicesSubnetWithAVlanAndTheTenantDropACL(t *testing.T) {
	values := bgpValues(servicesValues(servicesBGPPools, nil))
	subnet := servicesSubnetOf(t, values)
	requireDropACL(t, subnet)
	objects, _ := RenderUnderlay(configOf(t, values))
	rendered := byKindAndName(objects)
	if rendered["ProviderNetwork/services"] == nil || rendered["Vlan/services"] == nil {
		t.Fatalf("the services pool needs a provider network and a vlan: %v", len(rendered))
	}
	public := rendered["Subnet/provider-public"]
	if _, found, _ := unstructured.NestedString(public.Object, "spec", "vlan"); public == nil || found {
		t.Fatal("the other pools stay overlay subnets")
	}
}

func TestRenderUnderlayLeavesOtherPoolsWithoutACLs(t *testing.T) {
	objects, _ := RenderUnderlay(configOf(t, servicesValues(servicesL2Pools, nil)))
	public := byKindAndName(objects)["Subnet/provider-public"]
	if _, found, _ := unstructured.NestedSlice(public.Object, "spec", "acls"); found {
		t.Fatal("only the services pool has an ACL")
	}
	objects, _ = RenderUnderlay(configOf(t, map[string]string{"network.provider-networks": servicesL2Pools}))
	if _, found, _ := unstructured.NestedSlice(byKindAndName(objects)["Subnet/provider-services"].Object, "spec", "acls"); found {
		t.Fatal("no ACL without the Setting")
	}
}

func TestRenderUnderlaySkipsTheACLForAPoolThatIsNotAServicesPool(t *testing.T) {
	values := servicesValues(servicesL2Pools, map[string]string{"network.services.provider-network": "public"})
	objects, _ := RenderUnderlay(configOf(t, values))
	if _, found, _ := unstructured.NestedSlice(byKindAndName(objects)["Subnet/provider-public"].Object, "spec", "acls"); found {
		t.Fatal("the role services is required")
	}
}
