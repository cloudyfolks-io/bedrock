package network

import (
	"slices"
	"strings"
	"testing"
)

func servicesInput(t *testing.T, bgp bool, extra map[string]string, attached ...string) VpcInput {
	t.Helper()
	values := servicesValues(servicesL2Pools, extra)
	vrf := uint32(0)
	if bgp {
		values = bgpValues(servicesValues(servicesBGPPools, extra))
		vrf = 1000
	}
	in := vpcInput(t, true, values, vrf)
	in.Attached = attached
	return in
}

func TestPlanVpcL2AttachesTheServicesPlaneAfterTheTenantPools(t *testing.T) {
	spec, ext := PlanVpc(servicesInput(t, false, nil))
	if !slices.Equal(spec.ExtraExternalSubnets, []string{"provider-public", "provider-services"}) || ext.Reason != ReasonConnected {
		t.Fatalf("spec %+v ext %+v", spec, ext)
	}
	if spec.DefaultRoute == nil || spec.DefaultRoute.NextHop != "91.246.31.1" {
		t.Fatalf("the default route must follow the tenant pool: %+v", spec.DefaultRoute)
	}
}

func TestPlanVpcL2KeepsEveryEntryOfAnExistingVpcAndAddsTheServicesPlane(t *testing.T) {
	in := servicesInput(t, false, map[string]string{"network.provider-networks": `[{"name":"legacy","role":"platform","interface":"b","cidr":"192.0.2.0/24","gateway":"192.0.2.1"},` + servicesL2Pools[1:]}, "provider-legacy", "provider-public")
	in.AttachedRoute = "192.0.2.1"
	spec, _ := PlanVpc(in)
	if !slices.Equal(spec.ExtraExternalSubnets, []string{"provider-legacy", "provider-public", "provider-services"}) {
		t.Fatalf("subnets %v", spec.ExtraExternalSubnets)
	}
	if spec.DefaultRoute == nil || spec.DefaultRoute.NextHop != "192.0.2.1" {
		t.Fatalf("route %+v", spec.DefaultRoute)
	}
}

func TestPlanVpcL2ListIsStableOnceTheServicesPlaneIsAttached(t *testing.T) {
	first, _ := PlanVpc(servicesInput(t, false, nil))
	second, _ := PlanVpc(servicesInput(t, false, nil, first.ExtraExternalSubnets...))
	if !slices.Equal(first.ExtraExternalSubnets, second.ExtraExternalSubnets) {
		t.Fatalf("%v then %v", first.ExtraExternalSubnets, second.ExtraExternalSubnets)
	}
}

func TestPlanVpcL2KeepsTheServicesPlaneWhenItIsFirst(t *testing.T) {
	in := servicesInput(t, false, nil, "provider-services", "provider-public")
	in.AttachedRoute = "91.246.31.1"
	spec, _ := PlanVpc(in)
	if !slices.Equal(spec.ExtraExternalSubnets, []string{"provider-services", "provider-public"}) || spec.DefaultRoute.NextHop != "91.246.31.1" {
		t.Fatalf("spec %+v", spec)
	}
}

func TestPlanVpcBGPAttachesTransitAndServices(t *testing.T) {
	spec, ext := PlanVpc(servicesInput(t, true, nil))
	if !slices.Equal(spec.ExtraExternalSubnets, []string{"provider-transit", "provider-services"}) || ext.Reason != ReasonConnected {
		t.Fatalf("spec %+v ext %+v", spec, ext)
	}
	if spec.Dynamic == nil || spec.Dynamic.ExternalSubnet != "provider-transit" || spec.DefaultRoute.NextHop != "100.65.0.1" {
		t.Fatalf("spec %+v", spec)
	}
}

func TestPlanVpcBGPKeepsTheAttachmentsOfAnExistingVpc(t *testing.T) {
	spec, _ := PlanVpc(servicesInput(t, true, nil, "provider-transit", "provider-public"))
	if !slices.Equal(spec.ExtraExternalSubnets, []string{"provider-transit", "provider-public", "provider-services"}) {
		t.Fatalf("subnets %v", spec.ExtraExternalSubnets)
	}
	spec, _ = PlanVpc(servicesInput(t, true, nil, "provider-transit"))
	if !slices.Equal(spec.ExtraExternalSubnets, []string{"provider-transit", "provider-services"}) {
		t.Fatalf("subnets %v", spec.ExtraExternalSubnets)
	}
}

func TestPlanVpcBGPWithoutTheServicesPlaneKeepsTheTransitList(t *testing.T) {
	spec, _ := PlanVpc(vpcInput(t, true, bgpValues(nil), 1000))
	if !slices.Equal(spec.ExtraExternalSubnets, []string{"provider-transit"}) {
		t.Fatalf("subnets %v", spec.ExtraExternalSubnets)
	}
}

func TestPlanVpcKeepsTheListAndReportsAnIncompleteServicesPlane(t *testing.T) {
	for _, bgp := range []bool{false, true} {
		attached := []string{"provider-public"}
		if bgp {
			attached = []string{"provider-transit", "provider-public"}
		}
		spec, ext := PlanVpc(servicesInput(t, bgp, map[string]string{"network.services.identity-range": ""}, attached...))
		if ext.Reason != ReasonServicesNotConfigured || !strings.Contains(ext.Message, "network.services.identity-range") || !ext.Requested {
			t.Fatalf("bgp %v ext %+v", bgp, ext)
		}
		if !spec.EnableExternal || slices.Contains(spec.ExtraExternalSubnets, "provider-services") || !slices.Contains(spec.ExtraExternalSubnets, "provider-public") {
			t.Fatalf("bgp %v spec %+v", bgp, spec)
		}
	}
}

func TestPlanVpcPrivateNetworkNeverGetsTheServicesPlane(t *testing.T) {
	in := servicesInput(t, false, nil)
	in.Network.Spec.EnableExternal = false
	spec, ext := PlanVpc(in)
	if spec.EnableExternal || len(spec.ExtraExternalSubnets) != 0 || ext.Requested {
		t.Fatalf("spec %+v ext %+v", spec, ext)
	}
}
