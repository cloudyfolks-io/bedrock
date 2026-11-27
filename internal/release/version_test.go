package release

import "testing"

func TestIsDowngrade(t *testing.T) {
	cases := []struct {
		running, to string
		want        bool
	}{
		{"v0.5.0", "v0.4.0", true},
		{"v0.5.0", "v0.4.9", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.10.0", "v0.9.0", true},
		{"v0.5.0", "v0.5.0-rc.1", true},
		{"v0.5.0", "v0.5.0", false},
		{"v0.5.0", "v0.5.1", false},
		{"v0.5.0", "v0.10.0", false},
		{"v0.5.0-rc.1", "v0.5.0", false},
		{"v0.0.0-e2e.1", "v0.0.0-e2e.2", false},
		{"v0.0.0-e2e.2", "v0.0.0-e2e.1", true},
		{"v0.5.0", "dev", false},
		{"dev", "v0.5.0", false},
		{"dev", "dev", false},
		{"v0.5.0", "v0.0.0-e2e.1", true},
		{"v1", "v2", false},
		{"v2", "v1", false},
		{"", "v0.5.0", false},
	}
	for _, c := range cases {
		if got := IsDowngrade(c.running, c.to); got != c.want {
			t.Errorf("IsDowngrade(%q, %q) = %v, want %v", c.running, c.to, got, c.want)
		}
	}
}
