package settings

import (
	"fmt"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/api/resource"
)

const maxSMBIOSLength = 255

func smbiosString(v string) error {
	if len(v) > maxSMBIOSLength || strings.ContainsFunc(v, unicode.IsControl) {
		return fmt.Errorf("value must have at most %d characters and no control characters", maxSMBIOSLength)
	}
	return nil
}

func positiveQuantity(v string) error {
	if v == "" {
		return nil
	}
	quantity, err := resource.ParseQuantity(v)
	if err != nil {
		return err
	}
	if quantity.Sign() <= 0 {
		return fmt.Errorf("value must be greater than zero")
	}
	return nil
}

func virtualizationDefinitions() []Definition {
	return []Definition{
		{"virt.smbios.manufacturer", "", "SMBIOS manufacturer of every guest. Empty keeps the KubeVirt value. A running VM keeps its value until its next start.", smbiosString},
		{"virt.smbios.product", "", "SMBIOS product name of every guest. Empty keeps the KubeVirt value. A running VM keeps its value until its next start.", smbiosString},
		{"virt.smbios.family", "", "SMBIOS family of every guest. Empty keeps the KubeVirt value. A running VM keeps its value until its next start.", smbiosString},
		{"virt.smbios.version", "", "SMBIOS version of every guest. Empty keeps the KubeVirt value. A running VM keeps its value until its next start.", smbiosString},
		{"virt.smbios.sku", "", "SMBIOS SKU of every guest. Empty keeps the KubeVirt value. A running VM keeps its value until its next start.", smbiosString},
		{"migration.parallel-outbound", "4", "Parallel outbound live migrations per node.", intMin(1)},
		{"migration.bandwidth", "1Gi", "Bandwidth limit of one live migration, in bytes per second, for example 1Gi.", positiveQuantity},
		{"migration.completion-timeout-per-gib", "800", "Seconds for each GiB of guest memory before a live migration is aborted.", intMin(1)},
		{"migration.progress-timeout", "150", "Seconds without progress before a live migration is aborted.", intMin(1)},
		{"virt.eviction-strategy", "LiveMigrateIfPossible", "What a node drain does to a VM that has no evictionStrategy of its own. LiveMigrate makes the drain wait for a VM that cannot migrate. LiveMigrateIfPossible shuts such a VM down. None shuts every VM down.", oneOf("LiveMigrate", "LiveMigrateIfPossible", "None")},
		{"migration.compression", "", "Compression of live migration traffic. Empty means none. zstd compresses.", oneOf("zstd")},
	}
}
