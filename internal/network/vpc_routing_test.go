package network

import (
	"slices"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

func linkTo(vpc, local, remote string, ranges ...string) PeerLink {
	return PeerLink{RemoteVpc: vpc, LocalIP: local, RemoteIP: remote, RemoteRanges: ranges}
}

func TestPlanVpcAddsPeeringsAndPeerRoutesToAPrivateNetwork(t *testing.T) {
	in := vpcInput(t, false, map[string]string{}, 0)
	in.Peers = []PeerLink{linkTo("b", "169.254.0.1/30", "169.254.0.2", "10.2.0.0/16", "10.3.0.0/16")}
	spec, ext := PlanVpc(in)
	if ext.Requested || spec.EnableExternal {
		t.Fatalf("spec %+v ext %+v", spec, ext)
	}
	if !slices.Equal(spec.Peerings, []fabric.Peering{{RemoteVpc: "b", LocalConnectIP: "169.254.0.1/30"}}) {
		t.Fatalf("peerings %+v", spec.Peerings)
	}
	want := []fabric.StaticRoute{{CIDR: "10.2.0.0/16", NextHop: "169.254.0.2"}, {CIDR: "10.3.0.0/16", NextHop: "169.254.0.2"}}
	if !slices.Equal(spec.StaticRoutes, want) {
		t.Fatalf("routes %+v", spec.StaticRoutes)
	}
}

func TestPlanVpcKeepsPeeringsWhenTheExternalPlanFails(t *testing.T) {
	in := vpcInput(t, true, map[string]string{}, 0)
	in.Peers = []PeerLink{linkTo("b", "169.254.0.1/30", "169.254.0.2", "10.2.0.0/16")}
	spec, ext := PlanVpc(in)
	if ext.Reason != ReasonNoProviderNetwork || len(spec.Peerings) != 1 || len(spec.StaticRoutes) != 1 {
		t.Fatalf("spec %+v ext %+v", spec, ext)
	}
}

func TestPlanVpcPutsTheRoutesOfTheNetworkBeforeThePeerRoutes(t *testing.T) {
	in := vpcInput(t, false, map[string]string{}, 0)
	in.Network.Spec.StaticRoutes = []v1alpha1.NetworkStaticRoute{{CIDR: "10.2.0.0/16", NextHop: "10.100.0.9"}, {CIDR: "10.60.0.0/16", NextHop: "10.100.0.8"}}
	in.Peers = []PeerLink{linkTo("b", "169.254.0.1/30", "169.254.0.2", "10.2.0.0/16", "10.3.0.0/16")}
	spec, _ := PlanVpc(in)
	want := []fabric.StaticRoute{{CIDR: "10.2.0.0/16", NextHop: "10.100.0.9"}, {CIDR: "10.60.0.0/16", NextHop: "10.100.0.8"}, {CIDR: "10.3.0.0/16", NextHop: "169.254.0.2"}}
	if !slices.Equal(spec.StaticRoutes, want) {
		t.Fatalf("routes %+v", spec.StaticRoutes)
	}
}

func TestPlanVpcLeavesTheDefaultRouteApartFromTheStaticRoutes(t *testing.T) {
	in := vpcInput(t, true, map[string]string{"network.provider-networks": publicNetworks}, 0)
	in.Peers = []PeerLink{linkTo("b", "169.254.0.1/30", "169.254.0.2", "10.2.0.0/16")}
	spec, _ := PlanVpc(in)
	if spec.DefaultRoute == nil || spec.DefaultRoute.CIDR != "0.0.0.0/0" || len(spec.StaticRoutes) != 1 {
		t.Fatalf("spec %+v", spec)
	}
}
