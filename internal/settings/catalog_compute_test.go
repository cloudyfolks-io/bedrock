package settings

import "testing"

func TestComputeLimitSettings(t *testing.T) {
	cases := []struct {
		key, want string
		bad       []string
	}{
		{"compute.max-cores", "32", []string{"0", "x"}},
		{"compute.max-memory-mib", "131072", []string{"127", "x"}},
	}
	for _, tc := range cases {
		def, ok := Lookup(tc.key)
		if !ok || def.Default != tc.want {
			t.Fatalf("%s: %+v", tc.key, def)
		}
		for _, bad := range tc.bad {
			if def.Validate(bad) == nil {
				t.Errorf("%s accepts %q", tc.key, bad)
			}
		}
		if err := def.Validate("256"); err != nil {
			t.Errorf("%s: %v", tc.key, err)
		}
	}
}
