package network

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

type SubnetFacts struct {
	Namespace string
	Name      string
	CIDR      string
	Created   time.Time
}

type Violation struct {
	Reason  string
	Message string
}

func factsKey(f SubnetFacts) types.NamespacedName {
	return types.NamespacedName{Namespace: f.Namespace, Name: f.Name}
}

func compareFacts(a, b SubnetFacts) int {
	return cmp.Or(a.Created.Compare(b.Created), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
}

func insideAny(prefix netip.Prefix, ranges []string) bool {
	return slices.ContainsFunc(ranges, func(r string) bool {
		outer, err := netip.ParsePrefix(r)
		return err == nil && outer.Contains(prefix.Addr()) && prefix.Bits() >= outer.Bits()
	})
}

func VerdictsFor(networkRanges []string, subnets []SubnetFacts) map[types.NamespacedName]*Violation {
	ordered := slices.SortedFunc(slices.Values(subnets), compareFacts)
	verdicts := make(map[types.NamespacedName]*Violation, len(ordered))
	var winners []SubnetFacts
	for _, subnet := range ordered {
		prefix, err := netip.ParsePrefix(subnet.CIDR)
		if err != nil || !insideAny(prefix, networkRanges) {
			verdicts[factsKey(subnet)] = &Violation{Reason: ReasonCIDROutsideNetwork, Message: "the Subnet cidr must be inside one of the cidrs of its Network"}
			continue
		}
		verdicts[factsKey(subnet)] = overlapWith(prefix, winners)
		if verdicts[factsKey(subnet)] == nil {
			winners = append(winners, subnet)
		}
	}
	return verdicts
}

func overlapWith(prefix netip.Prefix, winners []SubnetFacts) *Violation {
	for _, winner := range winners {
		other, _ := netip.ParsePrefix(winner.CIDR)
		if prefix.Overlaps(other) {
			return &Violation{Reason: ReasonCIDROverlap, Message: "the Subnet cidr overlaps another Subnet of the same Network"}
		}
	}
	return nil
}

func AcceptedSubnets(subnets []SubnetFacts, verdicts map[types.NamespacedName]*Violation) []SubnetFacts {
	accepted := slices.DeleteFunc(slices.Clone(subnets), func(s SubnetFacts) bool { return verdicts[factsKey(s)] != nil })
	slices.SortFunc(accepted, func(a, b SubnetFacts) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return accepted
}

func DefaultGateway(cidr string) (string, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", err
	}
	return prefix.Masked().Addr().Next().String(), nil
}

func GatewayViolation(cidr, gateway string) *Violation {
	if gateway == "" {
		return nil
	}
	prefix, prefixErr := netip.ParsePrefix(cidr)
	addr, addrErr := netip.ParseAddr(gateway)
	if prefixErr != nil || addrErr != nil || !prefix.Contains(addr) || isNetworkOrBroadcast(prefix, addr) {
		return &Violation{Reason: ReasonGatewayInvalid, Message: fmt.Sprintf("the gateway %q must be a usable address inside %s", gateway, cidr)}
	}
	return nil
}

func isNetworkOrBroadcast(prefix netip.Prefix, addr netip.Addr) bool {
	if prefix.Bits() >= prefix.Addr().BitLen()-1 {
		return false
	}
	return addr == prefix.Masked().Addr() || addr == lastAddress(prefix)
}

func lastAddress(prefix netip.Prefix) netip.Addr {
	bytes := prefix.Masked().Addr().AsSlice()
	for bit := prefix.Bits(); bit < len(bytes)*8; bit++ {
		bytes[bit/8] |= 1 << (7 - bit%8)
	}
	addr, _ := netip.AddrFromSlice(bytes)
	return addr
}

func CheckExcludeIPs(cidr string, excludes []string) *Violation {
	prefix, err := netip.ParsePrefix(cidr)
	for _, exclude := range excludes {
		first, last, rangeErr := settings.ParseAddressRange(exclude)
		if err != nil || rangeErr != nil || !prefix.Contains(first) || !prefix.Contains(last) {
			return &Violation{Reason: ReasonExcludeIPsInvalid, Message: fmt.Sprintf("the excludeIPs entry %q must be an address or an ascending range inside %s", exclude, cidr)}
		}
	}
	return nil
}

func NetworkAllowed(namespaceTenant string, network v1alpha1.Network) *Violation {
	if namespaceTenant == "" || namespaceTenant != network.Spec.TenantRef {
		return &Violation{Reason: ReasonNetworkNotOwned, Message: "the Network belongs to another tenant"}
	}
	return nil
}
