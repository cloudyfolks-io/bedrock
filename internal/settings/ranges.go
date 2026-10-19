package settings

import (
	"fmt"
	"net/netip"
	"slices"
)

var bucketKeys = []string{"registry.bucket", "database.backup.bucket"}

var addressRangeKeys = []string{"database.vip-range", "network.services.identity-range", "network.load-balancer.cidr"}

func Conflict(key string, values map[string]string) error {
	switch {
	case slices.Contains(addressRangeKeys, key):
		return RangeConflict(values)
	case slices.Contains(bucketKeys, key):
		return BucketConflict(values)
	}
	return nil
}

func BucketConflict(values map[string]string) error {
	names := make([]string, len(bucketKeys))
	for i, key := range bucketKeys {
		names[i] = values[key]
		if names[i] == "" {
			def, _ := Lookup(key)
			names[i] = def.Default
		}
	}
	if names[0] == names[1] {
		return fmt.Errorf("%s and %s must differ, both are %q", bucketKeys[0], bucketKeys[1], names[0])
	}
	return nil
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
