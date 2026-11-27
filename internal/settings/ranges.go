package settings

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
)

var bucketKeys = []string{"registry.bucket", "database.backup.bucket"}

var retentionKeys = []string{"database.backup.retention-days-min", "database.backup.retention-days-max"}

const tenantManagementEnabledKey = "network.tenant-management.enabled"

var tenantManagementRangeKeys = []string{"network.tenant-management.workload-cidr", "network.tenant-management.managed-cidr"}

var addressRangeKeys = append([]string{"database.vip-range", "network.services.identity-range", "network.load-balancer.cidr"}, tenantManagementRangeKeys...)

func Conflict(key string, values map[string]string) error {
	switch {
	case slices.Contains(addressRangeKeys, key) || key == tenantManagementEnabledKey:
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
	enabled := effectiveValue(tenantManagementEnabledKey, values) == "true"
	prefixes := make([]netip.Prefix, len(addressRangeKeys))
	for i, key := range addressRangeKeys {
		if !enabled && slices.Contains(tenantManagementRangeKeys, key) {
			continue
		}
		prefixes[i], _ = netip.ParsePrefix(effectiveValue(key, values))
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

func effectiveValue(key string, values map[string]string) string {
	if value := values[key]; value != "" {
		return value
	}
	def, _ := Lookup(key)
	return def.Default
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
