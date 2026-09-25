package v1alpha1

import "testing"

func TestClusterPhasesAreDistinct(t *testing.T) {
	phases := []string{PhaseIdle, PhasePreflight, PhaseBackup, PhasePreload, PhaseControlPlane, PhaseComponents, PhaseWorkers, PhaseVerify, PhaseFailed}
	seen := map[string]bool{}
	for _, phase := range phases {
		if seen[phase] {
			t.Fatalf("duplicate phase %q", phase)
		}
		seen[phase] = true
	}
	if ClusterName != "cluster" {
		t.Fatalf("cluster singleton name %q", ClusterName)
	}
}

func TestReleaseAllowsUpgrade(t *testing.T) {
	release := Release{Spec: ReleaseSpec{Version: "v0.2.0", UpgradeFrom: []string{"v0.1.0", "v0.1.1"}}}
	if !ReleaseAllowsUpgrade(release, "v0.1.1") {
		t.Fatal("v0.1.1 must be allowed")
	}
	if ReleaseAllowsUpgrade(release, "v0.0.9") {
		t.Fatal("v0.0.9 must not be allowed")
	}
}
