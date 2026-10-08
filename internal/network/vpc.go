package network

import (
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
}

type External struct {
	Requested bool
	Reason    string
	Message   string
}

func PlanVpc(in VpcInput) (fabric.VpcSpec, External) {
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
	subnets := make([]string, len(in.Config.ProviderNetworks))
	for i, pn := range in.Config.ProviderNetworks {
		subnets[i] = ExternalSubnetName(pn.Name)
	}
	first := in.Config.ProviderNetworks[0]
	return fabric.VpcSpec{
		EnableExternal:       true,
		ExtraExternalSubnets: subnets,
		DefaultRoute:         &fabric.StaticRoute{CIDR: defaultCIDR, NextHop: first.Gateway},
	}, External{Requested: true, Reason: ReasonConnected}
}

func planBGP(in VpcInput) (fabric.VpcSpec, External) {
	if missing := in.Config.MissingBGPSettings(); len(missing) > 0 {
		return fabric.VpcSpec{}, External{Requested: true, Reason: ReasonBGPNotConfigured, Message: "BGP mode needs these settings: " + strings.Join(missing, ", ")}
	}
	transit, _ := in.Config.Provider(in.Config.BGP.Transit)
	subnet := ExternalSubnetName(transit.Name)
	return fabric.VpcSpec{
		EnableExternal:       true,
		ExtraExternalSubnets: []string{subnet},
		DefaultRoute:         &fabric.StaticRoute{CIDR: defaultCIDR, NextHop: transit.Gateway},
		Dynamic:              &fabric.DynamicRouting{VrfID: in.VrfID, ExternalSubnet: subnet, Redistribute: []string{"nat", "lb"}},
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
