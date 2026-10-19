package k0sversion

import (
	"slices"
	"testing"
)

func TestValid(t *testing.T) {
	for version, want := range map[string]bool{
		"v1.36.3+k0s.1": true, "v1.36.3+k0s.0": true, "v1.36.3": false, "1.36.3+k0s.1": false,
		"v1.36.3-k0s.1": false, "v1.36+k0s.1": false, "": false, "v1.36.3+k0s.1 ": false,
	} {
		if got := Valid(version); got != want {
			t.Errorf("%q: got %v want %v", version, got, want)
		}
	}
}

func TestParseAllowed(t *testing.T) {
	list, err := ParseAllowed("v1.36.3+k0s.1  v1.35.2+k0s.0\nv1.34.1+k0s.2")
	if err != nil || !slices.Equal(list, []string{"v1.36.3+k0s.1", "v1.35.2+k0s.0", "v1.34.1+k0s.2"}) {
		t.Fatalf("list %v err %v", list, err)
	}
	list, err = ParseAllowed("any")
	if err != nil || len(list) != 0 {
		t.Fatalf("any: %v %v", list, err)
	}
	for _, bad := range []string{"v1.36.3", "any v1.36.3+k0s.1", "latest"} {
		if _, err := ParseAllowed(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestAllowed(t *testing.T) {
	listed := []string{"v1.36.3+k0s.1"}
	if !Allowed(listed, "v1.36.3+k0s.1") || Allowed(listed, "v1.36.2+k0s.1") {
		t.Error("list rule")
	}
	if !Allowed(nil, "v1.30.0+k0s.0") || Allowed(nil, "bad") {
		t.Error("any rule")
	}
}
