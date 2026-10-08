package network

import (
	"fmt"
	"net/netip"
)

type ReservedRange struct {
	Name string
	CIDR string
}

func hostRange(name, address string) []ReservedRange {
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return nil
	}
	return []ReservedRange{{Name: name, CIDR: netip.PrefixFrom(addr, addr.BitLen()).String()}}
}

func ReservedRanges(cfg Config, joinCIDR, apiVIP string, nodeIPs []string) []ReservedRange {
	var ranges []ReservedRange
	for _, pn := range cfg.ProviderNetworks {
		ranges = append(ranges, ReservedRange{Name: "provider network " + pn.Name, CIDR: pn.CIDR})
	}
	if joinCIDR != "" {
		ranges = append(ranges, ReservedRange{Name: "join subnet", CIDR: joinCIDR})
	}
	ranges = append(ranges, hostRange("management", apiVIP)...)
	for _, ip := range nodeIPs {
		ranges = append(ranges, hostRange("management", ip)...)
	}
	return ranges
}

func ReservedOverlap(cidr string, reserved []ReservedRange) *Violation {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil
	}
	for _, r := range reserved {
		other, err := netip.ParsePrefix(r.CIDR)
		if err == nil && prefix.Overlaps(other) {
			return &Violation{Reason: ReasonCIDRReserved, Message: fmt.Sprintf("the Subnet cidr overlaps the %s range; use another cidr", r.Name)}
		}
	}
	return nil
}
