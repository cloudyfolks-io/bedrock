package settings

import (
	"fmt"
	"net/netip"
)

var addressRangeKeys = []string{"database.vip-range", "network.services.identity-range", "network.load-balancer.cidr"}

func AddressRange(key string) bool {
	for _, candidate := range addressRangeKeys {
		if candidate == key {
			return true
		}
	}
	return false
}

func RangeConflict(values map[string]string) error {
	prefixes := make([]netip.Prefix, len(addressRangeKeys))
	for i, key := range addressRangeKeys {
		value := values[key]
		if value == "" {
			def, _ := Lookup(key)
			value = def.Default
		}
		prefixes[i], _ = netip.ParsePrefix(value)
	}
	for i := range prefixes {
		for j := i + 1; j < len(prefixes); j++ {
			if prefixes[i].IsValid() && prefixes[j].IsValid() && prefixes[i].Overlaps(prefixes[j]) {
				return fmt.Errorf("the ranges %s %s and %s %s overlap", addressRangeKeys[i], prefixes[i], addressRangeKeys[j], prefixes[j])
			}
		}
	}
	return nil
}
