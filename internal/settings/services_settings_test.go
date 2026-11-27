package settings

import "testing"

func TestServicesSettingsDefaultsAndValidation(t *testing.T) {
	for _, key := range []string{"network.services.provider-network", "network.services.identity-range", "network.services.resolver"} {
		def, ok := Lookup(key)
		if !ok || def.Default != "" || def.Description == "" {
			t.Fatalf("setting %q: %+v", key, def)
		}
	}
	cases := map[string]map[string]bool{
		"network.services.provider-network": {"": true, "services": true, "Not A Label": false, "a_b": false},
		"network.services.identity-range":   {"": true, "100.67.0.0/16": true, "100.67.0.0/30": true, "100.67.0.0/31": false, "100.67.0.1/16": false, "fd00::/64": false},
		"network.services.resolver":         {"": true, "100.66.0.53": true, "fd00::1": false, "resolver": false},
	}
	for key, values := range cases {
		for value, valid := range values {
			if (validates(t, key, value) == nil) != valid {
				t.Errorf("%s=%q: valid %v", key, value, valid)
			}
		}
	}
}
