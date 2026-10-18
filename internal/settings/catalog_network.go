package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	maxProviderNameLength = 30
	maxInterfaceLength    = 15
	maxVlan               = 4094
	rangeSeparator        = ".."
	minBGPTimer           = time.Second
	maxBGPTimer           = 300 * time.Second
	maxPrefixLength       = 32
	maxTableID            = 65535
	firstReservedTableID  = 253
	lastReservedTableID   = 255
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
		{"network.dns-servers", "", "Comma list of IPv4 resolvers that DHCP gives to guests.", ipv4ListValue},
		{"network.bgp.bfd", "true", "Enable BFD on every BGP peer.", boolean},
		{"network.bgp.hold-time", "9s", "BGP hold time, from 1s to 300s.", durationBetween(minBGPTimer, maxBGPTimer)},
		{"network.bgp.keepalive-time", "3s", "BGP keepalive time, from 1s to 300s.", durationBetween(minBGPTimer, maxBGPTimer)},
		{"network.bgp.graceful-restart", "true", "Keep the BGP routes while a peer restarts.", boolean},
		{"network.bgp.advertise-filter", "", "Comma list of <cidr> ge <n> le <n> entries. The FRR agent advertises only the matching routes. Empty advertises all routes.", advertiseFilterValue},
		{"network.bgp.redistribute-tables", "", "Comma list of host routing table ids from 1 to 65535 that the FRR agent redistributes. 253, 254 and 255 are reserved.", tableIDsValue},
	}
}

func durationBetween(minimum, maximum time.Duration) func(string) error {
	return func(v string) error {
		if v == "" {
			return nil
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return err
		}
		if d < minimum || d > maximum {
			return fmt.Errorf("value %s outside %s to %s", d, minimum, maximum)
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
	if geErr != nil || leErr != nil || ge > le || le > maxPrefixLength {
		return "", fmt.Errorf("advertise filter entry %q: lengths must satisfy 0 <= ge <= le <= %d", item, maxPrefixLength)
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
