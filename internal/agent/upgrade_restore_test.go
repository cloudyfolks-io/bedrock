package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
)

func restoringStep(t *testing.T, err error) Step {
	return func(ctx context.Context, env StepEnv) (Outcome, error) {
		writeRestoreFixture(t, env.Deps.Root, env.Deps.Now())
		return Outcome{Message: "restored"}, err
	}
}

func TestRunUpgradesKeepsTheRestoredStatusAfterARestore(t *testing.T) {
	for name, stepErr := range map[string]error{"restore done": nil, "restart failed after the restore": errors.New("restart DaemonSet kube-system/fabric-cni: timeout")} {
		t.Run(name, func(t *testing.T) {
			createReleases(t)
			createUpgrade(t, "node-a", v1alpha1.StepK0sUpdate, v1alpha1.StepRestore)
			recordStatus(t, "node-a", v1alpha1.NodeUpgradeStatus{Steps: []v1alpha1.NodeUpgradeStepStatus{{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepFailed, Attempt: 1, Message: "k0s is not ready"}}})
			steps := map[string]Step{v1alpha1.StepRestore: restoringStep(t, stepErr)}
			err := RunUpgrades(context.Background(), k8sClient, upgradeDeps(t, &host.FakeExec{}, "boot-1"), steps)
			if stepErr != nil && (err == nil || err.Error() == "") {
				t.Fatalf("the restart failure must reach the agent log, got %v", err)
			}
			if status := stepStatus(getUpgrade(t, "node-a"), v1alpha1.StepRestore); status.State != v1alpha1.StepRunning {
				t.Fatalf("after a restore the agent must not write its pre-restore status into the restored object: restore status %+v", status)
			}
		})
	}
}

func TestRunUpgradesReportsARestoreThatFailedBeforeEtcdChanged(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepRestore)
	failed := func(ctx context.Context, env StepEnv) (Outcome, error) {
		return Outcome{}, errors.New("no backup to restore")
	}
	if err := RunUpgrades(context.Background(), k8sClient, upgradeDeps(t, &host.FakeExec{}, "boot-1"), map[string]Step{v1alpha1.StepRestore: failed}); err != nil {
		t.Fatal(err)
	}
	if status := stepStatus(getUpgrade(t, "node-a"), v1alpha1.StepRestore); status.State != v1alpha1.StepFailed || status.Message != "no backup to restore" {
		t.Fatalf("a restore that failed before etcd changed must report Failed for the operator: %+v", status)
	}
}

func TestRunUpgradeStopsThePassAfterARestore(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepRestore)
	deps := upgradeDeps(t, &host.FakeExec{}, "boot-1")
	stopped, err := runUpgrade(context.Background(), k8sClient, deps, map[string]Step{v1alpha1.StepRestore: restoringStep(t, nil)}, getUpgrade(t, "node-a"), "boot-1", nil)
	if err != nil || !stopped {
		t.Fatalf("after a restore the pass must stop, so the next loop reads the restored objects: stopped %v err %v", stopped, err)
	}
}
