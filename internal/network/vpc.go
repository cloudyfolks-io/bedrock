package network

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

const (
	firstVrfID  = 1000
	lastVrfID   = 65535
	defaultCIDR = "0.0.0.0/0"
)

type VpcInput struct {
	Network v1alpha1.Network
	Config  Config
	VrfID   uint32

	Attached      []string
	AttachedRoute string
	Peers         []PeerLink
}

type External struct {
	Requested bool
	Reason    string
	Message   string
}

func PlanVpc(in VpcInput) (fabric.VpcSpec, External) {
	spec, external := planExternal(in)
	return withRouting(spec, in), external
}

func withRouting(spec fabric.VpcSpec, in VpcInput) fabric.VpcSpec {
	spec.Peerings = vpcPeerings(in.Peers)
	spec.StaticRoutes = vpcStaticRoutes(in)
	return spec
}

func vpcPeerings(links []PeerLink) []fabric.Peering {
	peerings := make([]fabric.Peering, len(links))
	for i, link := range links {
		peerings[i] = fabric.Peering{RemoteVpc: link.RemoteVpc, LocalConnectIP: link.LocalIP}
	}
	return peerings
}

func vpcStaticRoutes(in VpcInput) []fabric.StaticRoute {
	var routes []fabric.StaticRoute
	add := func(route fabric.StaticRoute) {
		if !slices.ContainsFunc(routes, func(r fabric.StaticRoute) bool { return r.CIDR == route.CIDR }) {
			routes = append(routes, route)
		}
	}
	for _, route := range in.Network.Spec.StaticRoutes {
		add(fabric.StaticRoute{CIDR: route.CIDR, NextHop: route.NextHop})
	}
	for _, link := range in.Peers {
		for _, cidr := range link.RemoteRanges {
			add(fabric.StaticRoute{CIDR: cidr, NextHop: link.RemoteIP})
		}
	}
	return routes
}

func planExternal(in VpcInput) (fabric.VpcSpec, External) {
	if !in.Network.Spec.EnableExternal {
		return fabric.VpcSpec{}, External{}
	}
	if len(in.Config.ProviderNetworks) == 0 {
		return fabric.VpcSpec{}, External{Requested: true, Reason: ReasonNoProviderNetwork, Message: "enableExternal needs a provider network; set network.provider-networks"}
	}
	if in.Config.EIPMode == EIPModeBGP {
		return planBGP(in)
	}
	return planL2(in)
}

func planL2(in VpcInput) (fabric.VpcSpec, External) {
	kept := keptPools(in.Config, in.Attached)
	pools := slices.Concat(kept, unattachedTenantPools(in.Config, kept))
	if len(pools) == 0 {
		return fabric.VpcSpec{}, External{Requested: true, Reason: ReasonNoProviderNetwork, Message: "enableExternal in l2 mode needs a provider network with the role tenant; set network.provider-networks"}
	}
	subnets := make([]string, len(pools))
	for i, pn := range pools {
		subnets[i] = ExternalSubnetName(pn.Name)
	}
	nextHop := pools[0].Gateway
	if len(kept) > 0 {
		nextHop = cmp.Or(in.AttachedRoute, nextHop)
	}
	return fabric.VpcSpec{
		EnableExternal:       true,
		ExtraExternalSubnets: subnets,
		DefaultRoute:         &fabric.StaticRoute{CIDR: defaultCIDR, NextHop: nextHop},
	}, External{Requested: true, Reason: ReasonConnected}
}

func keptPools(cfg Config, attached []string) []v1alpha1.ProviderNetworkSpec {
	var kept []v1alpha1.ProviderNetworkSpec
	for _, subnet := range attached {
		if i := slices.IndexFunc(cfg.ProviderNetworks, func(pn v1alpha1.ProviderNetworkSpec) bool { return ExternalSubnetName(pn.Name) == subnet }); i >= 0 {
			kept = append(kept, cfg.ProviderNetworks[i])
		}
	}
	return kept
}

func unattachedTenantPools(cfg Config, kept []v1alpha1.ProviderNetworkSpec) []v1alpha1.ProviderNetworkSpec {
	tenants := slices.DeleteFunc(slices.Clone(cfg.ProviderNetworks), func(pn v1alpha1.ProviderNetworkSpec) bool {
		return !IsTenantPool(pn) || slices.ContainsFunc(kept, func(k v1alpha1.ProviderNetworkSpec) bool { return k.Name == pn.Name })
	})
	slices.SortFunc(tenants, func(a, b v1alpha1.ProviderNetworkSpec) int { return strings.Compare(a.Name, b.Name) })
	return tenants
}

func planBGP(in VpcInput) (fabric.VpcSpec, External) {
	if missing := MissingBGPSettings(in.Config); len(missing) > 0 {
		return fabric.VpcSpec{}, External{Requested: true, Reason: ReasonBGPNotConfigured, Message: "BGP mode needs these settings: " + strings.Join(missing, ", ")}
	}
	transit, _ := ProviderOf(in.Config, in.Config.BGP.Transit)
	subnet := ExternalSubnetName(transit.Name)
	return fabric.VpcSpec{
		EnableExternal:       true,
		EnableBfd:            true,
		ExtraExternalSubnets: []string{subnet},
		DefaultRoute:         &fabric.StaticRoute{CIDR: defaultCIDR, NextHop: transit.Gateway},
		Dynamic:              &fabric.DynamicRouting{VrfID: in.VrfID, ExternalSubnet: subnet, Redistribute: []string{"nat", "lb"}, LocalOnly: true},
	}, External{Requested: true, Reason: ReasonConnected}
}

func NextVrfID(used []uint32) (uint32, error) {
	for id := uint32(firstVrfID); id <= lastVrfID; id++ {
		if !slices.Contains(used, id) {
			return id, nil
		}
	}
	return 0, fmt.Errorf("all VRF ids from %d to %d are in use", firstVrfID, lastVrfID)
}
