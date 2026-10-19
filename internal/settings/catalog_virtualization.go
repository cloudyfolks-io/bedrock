package settings

import (
	"fmt"
	"strings"
	"unicode"
)

const maxSMBIOSLength = 255

func smbiosString(v string) error {
	if len(v) > maxSMBIOSLength || strings.ContainsFunc(v, unicode.IsControl) {
		return fmt.Errorf("value must have at most %d characters and no control characters", maxSMBIOSLength)
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
	}
}
