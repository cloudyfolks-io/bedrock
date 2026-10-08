package network

import (
	"fmt"
	"net/netip"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const labelFabricSubnet = "fabric.cloudyfolks.io/subnet"

type ObservedVpc struct {
	Standby         bool
	ExternalSubnets []string
	DynamicRouting  bool
}

type ObservedPort struct {
	Exists  bool
	Ready   bool
	Address string
}

type ExternalPathInput struct {
	Mode            string
	NetworkExternal bool
	ProviderNetwork string
	Transit         string
	Vpc             ObservedVpc
	RouterPort      ObservedPort
}

func reserved(address string, why string) *Violation {
	return &Violation{Reason: ReasonAddressReserved, Message: fmt.Sprintf("the address %s is %s", address, why)}
}

func CheckAddress(provider v1alpha1.ProviderNetworkSpec, address string) *Violation {
	if address == "" {
		return nil
	}
	prefix, prefixErr := netip.ParsePrefix(provider.CIDR)
	addr, addrErr := netip.ParseAddr(address)
	if prefixErr != nil || addrErr != nil || !prefix.Contains(addr) {
		return &Violation{Reason: ReasonAddressOutsideProviderNetwork, Message: fmt.Sprintf("the address %q is not inside %s", address, provider.CIDR)}
	}
	switch {
	case address == provider.Gateway:
		return reserved(address, "the gateway of the provider network")
	case addr == prefix.Masked().Addr():
		return reserved(address, "the network address")
	case addr == lastAddress(prefix):
		return reserved(address, "the broadcast address")
	case excluded(provider.ExcludeIPs, addr):
		return reserved(address, "in an excluded range of the provider network")
	}
	return nil
}

func excluded(ranges []string, addr netip.Addr) bool {
	return slices.ContainsFunc(ranges, func(entry string) bool {
		first, last, err := settings.ParseAddressRange(entry)
		return err == nil && !addr.Less(first) && !last.Less(addr)
	})
}

func RouterPortSubnet(mode, providerNetwork, transit string) string {
	if mode == EIPModeBGP {
		return ExternalSubnetName(transit)
	}
	return ExternalSubnetName(providerNetwork)
}

func ExternalPath(in ExternalPathInput) *Violation {
	switch {
	case !in.NetworkExternal:
		return &Violation{Reason: ReasonNetworkNotExternal, Message: "the Network has no external connection; set enableExternal on the Network"}
	case in.Mode == EIPModeBGP && in.ProviderNetwork == in.Transit:
		return &Violation{Reason: ReasonUnsupported, Message: "the transit provider network cannot hold elastic IPs in bgp mode"}
	case !in.Vpc.Standby:
		return notConnected("the Vpc of the Network is not ready")
	case in.Mode == EIPModeBGP && !in.Vpc.DynamicRouting:
		return notConnected("the Vpc of the Network has no dynamic routing")
	}
	subnet := RouterPortSubnet(in.Mode, in.ProviderNetwork, in.Transit)
	switch {
	case !slices.Contains(in.Vpc.ExternalSubnets, subnet):
		return notConnected(fmt.Sprintf("the Vpc is not attached to %s yet", subnet))
	case !in.RouterPort.Exists || !in.RouterPort.Ready || in.RouterPort.Address == "":
		return notConnected(fmt.Sprintf("the router port on %s has no address yet", subnet))
	}
	return nil
}

func notConnected(message string) *Violation {
	return &Violation{Reason: ReasonExternalNotConnected, Message: message}
}

func FindIP(ips []*unstructured.Unstructured, namespace, vm, fabricSubnet string) (string, bool) {
	for _, ip := range ips {
		ipNamespace, pod, _ := fabric.IPOwner(ip)
		if ip.GetLabels()[labelFabricSubnet] == fabricSubnet && ipNamespace == namespace && pod == vm {
			return ip.GetName(), true
		}
	}
	return "", false
}
