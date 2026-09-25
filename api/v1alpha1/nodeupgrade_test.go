package v1alpha1

import "testing"

func TestNodeUpgradeName(t *testing.T) {
	if got := NodeUpgradeName("v0.3.0", "node-a"); got != "v0.3.0-node-a" {
		t.Fatalf("name %q", got)
	}
}
