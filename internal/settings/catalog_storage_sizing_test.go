package settings

import "testing"

func TestCatalogStorageSizingKeys(t *testing.T) {
	defaults := map[string]string{
		StorageOSDMemory: "5Gi", StorageOSDMemoryTarget: "4Gi", StorageOSDCPU: "2",
		StorageMonMemory: "2Gi", StorageMgrMemory: "1Gi", StorageMDSMemory: "2Gi", StorageRGWMemory: "2Gi",
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
