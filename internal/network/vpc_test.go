package network

import (
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func vpcInput(t *testing.T, external bool, values map[string]string, vrf uint32) VpcInput {
	t.Helper()
	return VpcInput{
		Network: v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: "acme"}, Spec: v1alpha1.NetworkSpec{TenantRef: "acme", CIDR: "10.100.0.0/16", EnableExternal: external}},
		Config:  configOf(t, values),
		VrfID:   vrf,
	}
}

func TestPlanVpcPrivate(t *testing.T) {
	spec, ext := PlanVpc(vpcInput(t, false, map[string]string{"network.provider-networks": publicNetworks}, 0))
	if spec.EnableExternal || len(spec.ExtraExternalSubnets) != 0 || spec.DefaultRoute != nil || spec.Dynamic != nil || ext.Requested {
		t.Fatalf("spec %+v ext %+v", spec, ext)
	}
}

func TestPlanVpcL2External(t *testing.T) {
	networks := `[{"name":"public","interface":"b","cidr":"91.246.31.0/24","gateway":"91.246.31.1"},{"name":"backup","interface":"b","cidr":"198.51.100.0/24","gateway":"198.51.100.1"}]`
	spec, ext := PlanVpc(vpcInput(t, true, map[string]string{"network.provider-networks": networks}, 0))
	if !spec.EnableExternal || !slices.Equal(spec.ExtraExternalSubnets, []string{"provider-public", "provider-backup"}) {
		t.Fatalf("spec %+v", spec)
	}
	if spec.DefaultRoute == nil || spec.DefaultRoute.CIDR != "0.0.0.0/0" || spec.DefaultRoute.NextHop != "91.246.31.1" || spec.Dynamic != nil {
		t.Fatalf("route %+v", spec.DefaultRoute)
	}
	if !ext.Requested || ext.Reason != ReasonConnected {
		t.Fatalf("ext %+v", ext)
	}
}

func TestPlanVpcNoProviderNetwork(t *testing.T) {
	spec, ext := PlanVpc(vpcInput(t, true, map[string]string{}, 0))
	if spec.EnableExternal || !ext.Requested || ext.Reason != ReasonNoProviderNetwork || ext.Message == "" {
		t.Fatalf("spec %+v ext %+v", spec, ext)
	}
}

func TestPlanVpcBGP(t *testing.T) {
	spec, ext := PlanVpc(vpcInput(t, true, bgpValues(nil), 1000))
	if !spec.EnableExternal || !slices.Equal(spec.ExtraExternalSubnets, []string{"provider-transit"}) || ext.Reason != ReasonConnected {
		t.Fatalf("spec %+v ext %+v", spec, ext)
	}
	want := fabric.DynamicRouting{VrfID: 1000, ExternalSubnet: "provider-transit", Redistribute: []string{"nat", "lb"}}
	if spec.Dynamic == nil || spec.Dynamic.VrfID != want.VrfID || spec.Dynamic.ExternalSubnet != want.ExternalSubnet || !slices.Equal(spec.Dynamic.Redistribute, want.Redistribute) {
		t.Fatalf("dynamic %+v", spec.Dynamic)
	}
}

func TestPlanVpcBGPIncomplete(t *testing.T) {
	spec, ext := PlanVpc(vpcInput(t, true, bgpValues(map[string]string{"network.bgp.peers": ""}), 1000))
	if spec.EnableExternal || ext.Reason != ReasonBGPNotConfigured || !strings.Contains(ext.Message, "network.bgp.peers") {
		t.Fatalf("spec %+v ext %+v", spec, ext)
	}
}

func TestNextVrfIDStartsAt1000(t *testing.T) {
	if id, err := NextVrfID(nil); err != nil || id != 1000 {
		t.Fatal(id, err)
	}
}

func TestNextVrfIDSkipsUsedIDs(t *testing.T) {
	if id, err := NextVrfID([]uint32{1000, 1001, 1003, 5}); err != nil || id != 1002 {
		t.Fatal(id, err)
	}
}

func TestNextVrfIDFailsWhenExhausted(t *testing.T) {
	used := make([]uint32, 0, 65536)
	for id := uint32(1000); id <= 65535; id++ {
		used = append(used, id)
	}
	if _, err := NextVrfID(used); err == nil {
		t.Fatal("no free id must fail")
	}
}
