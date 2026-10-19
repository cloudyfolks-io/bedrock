package settings

import (
	"strings"
	"testing"
)

func TestCatalogSMBIOSKeys(t *testing.T) {
	for _, key := range []string{"virt.smbios.manufacturer", "virt.smbios.product", "virt.smbios.family", "virt.smbios.version", "virt.smbios.sku"} {
		def, ok := Lookup(key)
		if !ok || def.Default != "" {
			t.Fatalf("%s: definition %+v %v", key, def, ok)
		}
		for _, valid := range []string{"", "Cloudyfolks", "1.0", "ir-central-1"} {
			if err := def.Validate(valid); err != nil {
				t.Errorf("%s: %q must be valid: %v", key, valid, err)
			}
		}
		for _, invalid := range []string{"a\nb", "a\x00b", strings.Repeat("a", 256)} {
			if err := def.Validate(invalid); err == nil {
				t.Errorf("%s: %q must be invalid", key, invalid)
			}
		}
	}
}
