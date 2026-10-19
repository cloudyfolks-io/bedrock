package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	maxProviderNameLength      = 30
	maxInterfaceLength         = 15
	maxVlan                    = 4094
	rangeSeparator             = ".."
	minHoldTime                = 3 * time.Second
	minKeepaliveTime           = time.Second
	maxBGPTimer                = 300 * time.Second
	maxPrefixLength            = 32
	maxAllocatablePrefixLength = 30
	maxTableID                 = 999
	firstReservedTableID       = 253
	lastReservedTableID        = 255
	minMTU                     = 576
	maxMTU                     = 9000
)

type BGPPeer struct {
	Address string
	ASN     uint32
}

func networkDefinitions() []Definition {
	return []Definition{
		{"network.eip-mode", "l2", "How elastic IPs reach the physical network.", oneOf("l2", "bgp")},
		{"network.provider-networks", "", "JSON list of provider networks with name, interface, vlan, cidr, gateway and excludeIPs.", providerNetworksValue},
		{"network.bgp.local-asn", "", "Local AS number of the FRR agent.", asnValue},
		{"network.bgp.router-id", "", "BGP router id.", ipv4Value},
		{"network.bgp.peers", "", "Comma list of <ipv4>@<asn> BGP peers.", bgpPeersValue},
		{"network.bgp.transit-network", "", "Provider network that carries the router ports in bgp mode.", dnsLabelValue},
		{"network.dns-servers", "", "Comma list of IPv4 resolvers that DHCP gives to guests. A Subnet that sets dnsServers uses its own list.", ipv4ListValue},
		{"network.mtu", "", "MTU in bytes, from 576 to 9000, of every Subnet that sets no mtu. Empty lets Fabric choose. Keep it at or below the underlay MTU minus 100. A running guest learns a new value at its next DHCP lease, but the pod interface of a VM keeps its MTU until the VM restarts, so raise it only with a planned VM restart.", intBetween(minMTU, maxMTU)},
		{"network.services.provider-network", "", "Name of the provider network with the role services. Every external Network attaches to it, gets a router port on it and a pinned address for source NAT. Empty turns the services plane off.", dnsLabelValue},
		{"network.services.identity-range", "", "IPv4 network inside the services provider network from which each Network gets one address for source NAT. Keep it in the excludeIPs of the provider network. The prefix must be 30 or shorter.", cidrWithin(maxAllocatablePrefixLength)},
		{"network.services.resolver", "", "IPv4 address of the resolver on the services plane. DHCP gives it to guests when network.dns-servers is empty.", ipv4Value},
		{"network.reserved-cidrs", "100.64.0.0/10", "Comma list of IPv4 networks that no Subnet and no Network range may overlap. They belong to the platform planes. Empty keeps the default.", cidrListValue},
		{"network.subnet-allowed-ranges", "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16", "Comma list of IPv4 networks. Every Subnet must be inside one of them. Use 0.0.0.0/0 to allow every address. Empty keeps the default.", cidrListValue},
		{"network.peering.range", "169.254.0.0/16", "IPv4 network from which each NetworkPeering without connectCIDR gets a /30 connect network. The prefix must be 30 or shorter. Empty keeps the default.", cidrWithin(maxAllocatablePrefixLength)},
		{"network.load-balancer.cidr", "100.69.0.0/24", "IPv4 network of the load balancer plane. Every Network gets one hidden Fabric Subnet with this range, and internal load balancers take their virtual addresses there. The range repeats in every Network, so keep it inside network.reserved-cidrs, and set it before the first Network exists. Bedrock removes it from network.bgp.advertise-filter, so BGP never advertises it.", cidrValue},
		{"network.load-balancer.gateway", "100.69.0.1", "IPv4 address of the load balancer plane gateway. It must be inside network.load-balancer.cidr and must not be its network or broadcast address.", ipv4Value},
		{"network.bgp.bfd", "true", "Enable BFD on the BGP peers of the FRR agent. The Fabric Vpc of a Network in bgp mode always has BFD, and this Setting does not change it.", boolean},
		{"network.bgp.hold-time", "9s", "BGP hold time in whole seconds, from 3s to 300s. A peer refuses a hold time of 1s or 2s.", wholeSecondsBetween(minHoldTime, maxBGPTimer)},
		{"network.bgp.keepalive-time", "3s", "BGP keepalive time in whole seconds, from 1s to 300s.", wholeSecondsBetween(minKeepaliveTime, maxBGPTimer)},
		{"network.bgp.graceful-restart", "true", "Keep the BGP routes while a peer restarts.", boolean},
		{"network.bgp.advertise-filter", "", "Comma list of <cidr> ge <n> le <n> entries, with the mask length <= ge <= le <= 32. The FRR agent advertises only the matching routes and the routes of the elastic IPs. Empty advertises only the elastic IP routes.", advertiseFilterValue},
		{"network.bgp.redistribute-tables", "", "Comma list of host routing table ids from 1 to 999 that the FRR agent redistributes. 253, 254 and 255 are reserved. Ids from 1000 up are the VRF ids of the VPCs. The list must not hold a VRF id that a Network pins in spec.vrfId.", tableIDsValue},
	}
}

func intBetween(minimum, maximum int) func(string) error {
	return func(v string) error {
		if v == "" {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < minimum || n > maximum {
			return fmt.Errorf("value %q must be an integer from %d to %d", v, minimum, maximum)
		}
		return nil
	}
}

func wholeSecondsBetween(minimum, maximum time.Duration) func(string) error {
	return func(v string) error {
		if v == "" {
			return nil
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return err
		}
		if d < minimum || d > maximum || d%time.Second != 0 {
			return fmt.Errorf("value %s must be whole seconds from %s to %s", d, minimum, maximum)
		}
		return nil
	}
}

func ParseAdvertiseFilter(v string) ([]string, error) {
	if v == "" {
		return nil, nil
	}
	items := strings.Split(v, ",")
	entries := make([]string, len(items))
	for i, item := range items {
		entry, err := parseFilterEntry(item)
		if err != nil {
			return nil, err
		}
		entries[i] = entry
	}
	return entries, nil
}

func parseFilterEntry(item string) (string, error) {
	fields := strings.Fields(item)
	if len(fields) != 5 || fields[1] != "ge" || fields[3] != "le" {
		return "", fmt.Errorf("advertise filter entry %q must look like <cidr> ge <n> le <n>", item)
	}
	prefix, err := netip.ParsePrefix(fields[0])
	if err != nil || !prefix.Addr().Is4() || prefix.Masked() != prefix {
		return "", fmt.Errorf("advertise filter entry %q: cidr must be a canonical IPv4 network", item)
	}
	ge, geErr := strconv.ParseUint(fields[2], 10, 8)
	le, leErr := strconv.ParseUint(fields[4], 10, 8)
	if geErr != nil || leErr != nil || ge < uint64(prefix.Bits()) || ge > le || le > maxPrefixLength {
		return "", fmt.Errorf("advertise filter entry %q: lengths must satisfy mask length <= ge <= le <= %d", item, maxPrefixLength)
	}
	return fmt.Sprintf("%s ge %d le %d", prefix, ge, le), nil
}

func advertiseFilterValue(v string) error {
	_, err := ParseAdvertiseFilter(v)
	return err
}

func ParseTableIDs(v string) ([]uint32, error) {
	if v == "" {
		return nil, nil
	}
	items := strings.Split(v, ",")
	ids := make([]uint32, len(items))
	for i, item := range items {
		id, err := strconv.ParseUint(item, 10, 32)
		reserved := id >= firstReservedTableID && id <= lastReservedTableID
		if err != nil || id < 1 || id > maxTableID || reserved {
			return nil, fmt.Errorf("table id %q must be an integer from 1 to %d outside %d to %d", item, maxTableID, firstReservedTableID, lastReservedTableID)
		}
		ids[i] = uint32(id)
	}
	return ids, nil
}

func tableIDsValue(v string) error {
	_, err := ParseTableIDs(v)
	return err
}

var providerRoles = []string{"", v1alpha1.ProviderRoleTenant, v1alpha1.ProviderRolePlatform, v1alpha1.ProviderRoleTransit, v1alpha1.ProviderRoleServices}

func providerNetworksValue(v string) error {
	_, err := DecodeProviderNetworks(v)
	return err
}

func DecodeProviderNetworks(raw string) ([]v1alpha1.ProviderNetworkSpec, error) {
	if raw == "" {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	var list []v1alpha1.ProviderNetworkSpec
	if err := decoder.Decode(&list); err != nil {
		return nil, fmt.Errorf("provider networks must be a JSON list: %w", err)
	}
	return list, ValidateProviderNetworks(list)
}

func EncodeProviderNetworks(list []v1alpha1.ProviderNetworkSpec) (string, error) {
	if len(list) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(list)
	return string(raw), err
}

func ValidateProviderNetworks(list []v1alpha1.ProviderNetworkSpec) error {
	seen := map[string]bool{}
	for _, pn := range list {
		if seen[pn.Name] {
			return fmt.Errorf("provider network %q is defined twice", pn.Name)
		}
		seen[pn.Name] = true
		if err := validateProviderNetwork(pn); err != nil {
			return fmt.Errorf("provider network %q: %w", pn.Name, err)
		}
	}
	return nil
}

func validateProviderNetwork(pn v1alpha1.ProviderNetworkSpec) error {
	if err := dnsLabelValue(pn.Name); err != nil || pn.Name == "" || len(pn.Name) > maxProviderNameLength {
		return fmt.Errorf("name must be a DNS label of at most %d characters", maxProviderNameLength)
	}
	if !slices.Contains(providerRoles, pn.Role) {
		return fmt.Errorf("role must be one of %s", strings.Join(providerRoles[1:], ", "))
	}
	if pn.Interface == "" || len(pn.Interface) > maxInterfaceLength {
		return fmt.Errorf("interface must have 1 to %d characters", maxInterfaceLength)
	}
	if pn.Vlan < 0 || pn.Vlan > maxVlan {
		return fmt.Errorf("vlan must be from 0 to %d", maxVlan)
	}
	prefix, err := netip.ParsePrefix(pn.CIDR)
	if err != nil || !prefix.Addr().Is4() || prefix.Masked() != prefix {
		return fmt.Errorf("cidr %q must be a canonical IPv4 network", pn.CIDR)
	}
	gateway, err := netip.ParseAddr(pn.Gateway)
	if err != nil || !gateway.Is4() || !prefix.Contains(gateway) {
		return fmt.Errorf("gateway %q must be an IPv4 address inside the cidr", pn.Gateway)
	}
	for _, exclude := range pn.ExcludeIPs {
		first, last, err := ParseAddressRange(exclude)
		if err != nil || !prefix.Contains(first) || !prefix.Contains(last) {
			return fmt.Errorf("excludeIPs entry %q must be an address or a range inside the cidr", exclude)
		}
	}
	return nil
}

func ParseAddressRange(value string) (netip.Addr, netip.Addr, error) {
	startText, endText, isRange := strings.Cut(value, rangeSeparator)
	first, err := netip.ParseAddr(startText)
	if err != nil {
		return netip.Addr{}, netip.Addr{}, err
	}
	if !isRange {
		return first, first, nil
	}
	last, err := netip.ParseAddr(endText)
	if err != nil {
		return netip.Addr{}, netip.Addr{}, err
	}
	if last.Less(first) {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("range %q is backwards", value)
	}
	return first, last, nil
}

func asnValue(v string) error {
	if v == "" {
		return nil
	}
	_, err := parseASN(v)
	return err
}

func parseASN(v string) (uint32, error) {
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("value %q is not an AS number from 1 to 4294967295", v)
	}
	return uint32(n), nil
}

func ipv4Value(v string) error {
	if v == "" {
		return nil
	}
	addr, err := netip.ParseAddr(v)
	if err != nil || !addr.Is4() {
		return fmt.Errorf("value %q is not an IPv4 address", v)
	}
	return nil
}

func ipv4ListValue(v string) error {
	if v == "" {
		return nil
	}
	for _, item := range strings.Split(v, ",") {
		if err := ipv4Value(item); err != nil || item == "" {
			return fmt.Errorf("value %q is not a comma list of IPv4 addresses", v)
		}
	}
	return nil
}

func cidrValue(v string) error {
	if v == "" {
		return nil
	}
	prefix, err := netip.ParsePrefix(v)
	if err != nil || !prefix.Addr().Is4() || prefix.Masked() != prefix {
		return fmt.Errorf("value %q is not a canonical IPv4 network", v)
	}
	return nil
}

func cidrListValue(v string) error {
	if v == "" {
		return nil
	}
	for _, item := range strings.Split(v, ",") {
		prefix, err := netip.ParsePrefix(item)
		if err != nil || !prefix.Addr().Is4() || prefix.Masked() != prefix {
			return fmt.Errorf("value %q is not a comma list of canonical IPv4 networks", v)
		}
	}
	return nil
}

func cidrWithin(maxBits int) func(string) error {
	return func(v string) error {
		if v == "" {
			return nil
		}
		prefix, err := netip.ParsePrefix(v)
		if err != nil || !prefix.Addr().Is4() || prefix.Masked() != prefix || prefix.Bits() > maxBits {
			return fmt.Errorf("value %q is not a canonical IPv4 network with a prefix of %d or shorter", v, maxBits)
		}
		return nil
	}
}

func dnsLabelValue(v string) error {
	if v == "" || len(validation.IsDNS1123Label(v)) == 0 {
		return nil
	}
	return fmt.Errorf("value %q is not a DNS label", v)
}

func ParseBGPPeers(v string) ([]BGPPeer, error) {
	if v == "" {
		return nil, nil
	}
	items := strings.Split(v, ",")
	peers := make([]BGPPeer, 0, len(items))
	for _, item := range items {
		address, asnText, found := strings.Cut(item, "@")
		if !found {
			return nil, fmt.Errorf("peer %q must look like <ipv4>@<asn>", item)
		}
		if err := ipv4Value(address); err != nil || address == "" {
			return nil, fmt.Errorf("peer %q has no IPv4 address", item)
		}
		asn, err := parseASN(asnText)
		if err != nil {
			return nil, fmt.Errorf("peer %q: %w", item, err)
		}
		peers = append(peers, BGPPeer{Address: address, ASN: asn})
	}
	return peers, nil
}

func bgpPeersValue(v string) error {
	_, err := ParseBGPPeers(v)
	return err
}
