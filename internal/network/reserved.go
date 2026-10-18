package network

import (
	"fmt"
	"net/netip"
	"strings"
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

func SettingReserved(cfg Config) []ReservedRange {
	ranges := make([]ReservedRange, len(cfg.ReservedCIDRs))
	for i, cidr := range cfg.ReservedCIDRs {
		ranges[i] = ReservedRange{Name: "reserved " + cidr, CIDR: cidr}
	}
	return ranges
}

func ReservedRanges(cfg Config, joinCIDR, apiVIP string, nodeIPs []string) []ReservedRange {
	var ranges []ReservedRange
	for _, pn := range cfg.ProviderNetworks {
		ranges = append(ranges, ReservedRange{Name: "provider network " + pn.Name, CIDR: pn.CIDR})
	}
	ranges = append(ranges, SettingReserved(cfg)...)
	if joinCIDR != "" {
		ranges = append(ranges, ReservedRange{Name: "join subnet", CIDR: joinCIDR})
	}
	ranges = append(ranges, hostRange("management", apiVIP)...)
	for _, ip := range nodeIPs {
		ranges = append(ranges, hostRange("management", ip)...)
	}
	return ranges
}

func firstOverlap(prefix netip.Prefix, reserved []ReservedRange) (ReservedRange, bool) {
	for _, r := range reserved {
		other, err := netip.ParsePrefix(r.CIDR)
		if err == nil && prefix.Overlaps(other) {
			return r, true
		}
	}
	return ReservedRange{}, false
}

func ReservedOverlap(cidr string, reserved []ReservedRange) *Violation {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil
	}
	if r, found := firstOverlap(prefix, reserved); found {
		return &Violation{Reason: ReasonCIDRReserved, Message: fmt.Sprintf("the Subnet cidr overlaps the %s range; use another cidr", r.Name)}
	}
	return nil
}

func NetworkReservedOverlap(ranges []string, reserved []ReservedRange) *Violation {
	for _, cidr := range ranges {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			continue
		}
		if r, found := firstOverlap(prefix, reserved); found {
			return &Violation{Reason: ReasonCIDRReserved, Message: fmt.Sprintf("the Network cidr %s overlaps the %s range; use another cidr", cidr, r.Name)}
		}
	}
	return nil
}

func AllowedRangeViolation(cidr string, allowed []string) *Violation {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil || insideAny(prefix, allowed) {
		return nil
	}
	return &Violation{Reason: ReasonCIDROutsideAllowedRanges, Message: fmt.Sprintf("the Subnet cidr must be inside one of the allowed ranges %s; the setting network.subnet-allowed-ranges holds them", strings.Join(allowed, ", "))}
}
