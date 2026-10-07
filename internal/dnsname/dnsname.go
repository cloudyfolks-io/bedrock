package dnsname

import (
	"net/netip"
	"regexp"
)

const maxLength = 253

var pattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

func Valid(name string) bool {
	if _, err := netip.ParseAddr(name); err == nil {
		return false
	}
	return len(name) <= maxLength && pattern.MatchString(name)
}
