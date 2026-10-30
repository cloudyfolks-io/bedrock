package settings

import "testing"

func TestCatalogNodeKeys(t *testing.T) {
	defaults := map[string]string{
		KubeletSystemReservedMemory: "2Gi", KubeletKubeReservedMemory: "4Gi",
		KubeletSystemReservedCPU: "500m", KubeletKubeReservedCPU: "1",
		KubeletEvictionMemory: "1Gi", KubeletEvictionNodefs: "10%", KubeletEvictionImagefs: "15%",
	}
	for key, want := range defaults {
		def, ok := Lookup(key)
		if !ok || def.Default != want {
			t.Fatalf("%s: definition %+v %v", key, def, ok)
		}
		if err := def.Validate(want); err != nil {
			t.Errorf("%s: default must be valid: %v", key, err)
		}
		if err := def.Validate("abc"); err == nil {
			t.Errorf("%s: abc must be invalid", key)
		}
		if err := def.Validate("0"); err == nil {
			t.Errorf("%s: 0 must be invalid", key)
		}
	}
}

func TestCatalogEvictionThreshold(t *testing.T) {
	def, _ := Lookup(KubeletEvictionNodefs)
	for _, valid := range []string{"", "5%", "12.5%", "20Gi", "500Mi"} {
		if err := def.Validate(valid); err != nil {
			t.Errorf("%q must be valid: %v", valid, err)
		}
	}
	for _, invalid := range []string{"0%", "100%", "150%", "-1%", "x%"} {
		if err := def.Validate(invalid); err == nil {
			t.Errorf("%q must be invalid", invalid)
		}
	}
}
