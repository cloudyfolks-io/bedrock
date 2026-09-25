package v1alpha1

import "testing"

func TestNodeUpgradeName(t *testing.T) {
	if got := NodeUpgradeName("v0.3.0", "node-a"); got != "v0.3.0-node-a" {
		t.Fatalf("name %q", got)
	}
}

func TestNodeUpgradeStepState(t *testing.T) {
	upgrade := NodeUpgrade{Status: NodeUpgradeStatus{Steps: []NodeUpgradeStepStatus{{Name: StepPreload, State: StepSucceeded}}}}
	if got := upgrade.StepState(StepPreload); got != StepSucceeded {
		t.Fatalf("preload state %q", got)
	}
	if got := upgrade.StepState(StepBackup); got != "" {
		t.Fatalf("absent step must report an empty state, got %q", got)
	}
}
