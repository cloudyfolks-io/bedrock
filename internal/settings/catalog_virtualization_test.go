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

func TestCatalogMigrationKeys(t *testing.T) {
	cases := []struct {
		key     string
		def     string
		valid   []string
		invalid []string
	}{
		{"migration.parallel-outbound", "4", []string{"1", "8"}, []string{"0", "x"}},
		{"migration.bandwidth", "1Gi", []string{"512Mi", "2G", "1000000"}, []string{"fast", "0", "-1Gi"}},
		{"migration.completion-timeout-per-gib", "800", []string{"150"}, []string{"0", "x"}},
		{"migration.progress-timeout", "150", []string{"60"}, []string{"0", "x"}},
		{"migration.compression", "", []string{"", "zstd"}, []string{"lz4", "none"}},
	}
	for _, c := range cases {
		def, ok := Lookup(c.key)
		if !ok || def.Default != c.def {
			t.Fatalf("%s: definition %+v %v", c.key, def, ok)
		}
		for _, value := range c.valid {
			if err := def.Validate(value); err != nil {
				t.Errorf("%s: %q must be valid: %v", c.key, value, err)
			}
		}
		for _, value := range c.invalid {
			if err := def.Validate(value); err == nil {
				t.Errorf("%s: %q must be invalid", c.key, value)
			}
		}
	}
}
