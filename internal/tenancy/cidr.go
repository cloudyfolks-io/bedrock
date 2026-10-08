package tenancy

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
)

var ErrNoFreeSubnet = errors.New("no free subnet in the VPC")

func NextFreeSubnet(vpc netip.Prefix, used []netip.Prefix) (netip.Prefix, error) {
	vpc = vpc.Masked()
	if !vpc.Addr().Is4() || vpc.Bits() > SubnetPrefixBits {
		return netip.Prefix{}, fmt.Errorf("the VPC %s must be IPv4 with a prefix length of at most %d", vpc, SubnetPrefixBits)
	}
	for candidate := netip.PrefixFrom(vpc.Addr(), SubnetPrefixBits); candidate.IsValid() && vpc.Contains(candidate.Addr()); candidate = nextBlock(candidate) {
		if !slices.ContainsFunc(used, candidate.Overlaps) {
			return candidate, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("%w %s", ErrNoFreeSubnet, vpc)
}

func nextBlock(block netip.Prefix) netip.Prefix {
	last := lastAddress(block)
	next := last.Next()
	if !next.IsValid() {
		return netip.Prefix{}
	}
	return netip.PrefixFrom(next, block.Bits())
}

func lastAddress(block netip.Prefix) netip.Addr {
	raw := block.Addr().As4()
	hostBits := 32 - block.Bits()
	value := uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
	value |= uint32(1)<<hostBits - 1
	return netip.AddrFrom4([4]byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)})
}
