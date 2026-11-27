package settings

import "testing"

func TestPeeringRangeSettingValidation(t *testing.T) {
	cases := map[string]bool{
		"": true, "169.254.0.0/16": true, "169.254.0.0/30": true, "10.9.0.0/24": true,
		"169.254.0.0/31": false, "169.254.0.0/32": false, "169.254.0.1/16": false, "fd00::/64": false, "nonsense": false, "169.254.0.0/16,10.0.0.0/8": false,
	}
	for value, valid := range cases {
		if (validates(t, "network.peering.range", value) == nil) != valid {
			t.Errorf("value %q: valid %v", value, valid)
		}
	}
}
