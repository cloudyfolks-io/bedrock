package settings

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
)

var bucketKeys = []string{"registry.bucket", "database.backup.bucket"}

var retentionKeys = []string{"database.backup.retention-days-min", "database.backup.retention-days-max"}

var addressRangeKeys = []string{"database.vip-range", "network.services.identity-range", "network.load-balancer.cidr", "network.tenant-management.workload-cidr", "network.tenant-management.managed-cidr"}

func Conflict(key string, values map[string]string) error {
	switch {
	case slices.Contains(addressRangeKeys, key):
		return RangeConflict(values)
	case slices.Contains(bucketKeys, key):
		return BucketConflict(values)
	case slices.Contains(retentionKeys, key):
		return RetentionConflict(values)
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

func RetentionConflict(values map[string]string) error {
	bounds := make([]int, len(retentionKeys))
	for i, key := range retentionKeys {
		value := values[key]
		if value == "" {
			def, _ := Lookup(key)
			value = def.Default
		}
		bounds[i], _ = strconv.Atoi(value)
	}
	if bounds[0] > bounds[1] {
		return fmt.Errorf("%s must not be above %s, they are %d and %d", retentionKeys[0], retentionKeys[1], bounds[0], bounds[1])
	}
	return nil
}
