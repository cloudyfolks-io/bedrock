package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

type stepRecorder struct {
	calls []string
}

func (r *stepRecorder) step(name string, outcome Outcome, err error) Step {
	return func(_ context.Context, env StepEnv) (Outcome, error) {
		r.calls = append(r.calls, name+"@"+env.Target.Name+"<-"+env.From.Name)
		return outcome, err
	}
}

func createReleases(t *testing.T) {
	t.Helper()
	for _, version := range []string{"v0.2.0", "v0.3.0"} {
		release := &v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: version}, Spec: v1alpha1.ReleaseSpec{Version: version, Image: "ghcr.io/cloudyfolks-io/bedrock:" + version, K0sVersion: "v1.36.3+k0s.0"}}
		if err := k8sClient.Create(context.Background(), release); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { k8sClient.Delete(context.Background(), release) })
	}
}

func createUpgrade(t *testing.T, node string, steps ...string) {
	t.Helper()
	upgrade := &v1alpha1.NodeUpgrade{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.NodeUpgradeName("v0.3.0", node)},
		Spec:       v1alpha1.NodeUpgradeSpec{Node: node, Version: "v0.3.0", From: "v0.2.0", Attempt: 1, Steps: steps},
	}
	if err := k8sClient.Create(context.Background(), upgrade); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k8sClient.Delete(context.Background(), upgrade) })
}

func getUpgrade(t *testing.T, node string) v1alpha1.NodeUpgrade {
	t.Helper()
	var upgrade v1alpha1.NodeUpgrade
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: v1alpha1.NodeUpgradeName("v0.3.0", node)}, &upgrade); err != nil {
		t.Fatal(err)
	}
	return upgrade
}

func stepStatus(upgrade v1alpha1.NodeUpgrade, name string) v1alpha1.NodeUpgradeStepStatus {
	for _, step := range upgrade.Status.Steps {
		if step.Name == name {
			return step
		}
	}
	return v1alpha1.NodeUpgradeStepStatus{}
}

func upgradeDeps(t *testing.T, exec *host.FakeExec, bootID string) Deps {
	t.Helper()
	root := t.TempDir()
	writeBootID(t, root, bootID)
	deps := newDeps(exec, time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	deps.Root = root
	return deps
}

func writeBootID(t *testing.T, root, bootID string) {
	t.Helper()
	path := filepath.Join(root, "proc", "sys", "kernel", "random", "boot_id")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(bootID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func raiseAttempt(t *testing.T, node string) {
	t.Helper()
	upgrade := getUpgrade(t, node)
	upgrade.Spec.Attempt++
	if err := k8sClient.Update(context.Background(), &upgrade); err != nil {
		t.Fatal(err)
	}
}

func TestRunUpgradesRunsStepsInOrder(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepPreload, v1alpha1.StepBackup)
	recorder := &stepRecorder{}
	steps := map[string]Step{v1alpha1.StepPreload: recorder.step(v1alpha1.StepPreload, Outcome{Message: "staged"}, nil), v1alpha1.StepBackup: recorder.step(v1alpha1.StepBackup, Outcome{Message: "/b.tar.gz sha256:x"}, nil)}
	deps := upgradeDeps(t, &host.FakeExec{}, "boot-1")
	if err := RunUpgrades(context.Background(), k8sClient, deps, steps); err != nil {
		t.Fatal(err)
	}
	want := []string{"Preload@v0.3.0<-v0.2.0", "Backup@v0.3.0<-v0.2.0"}
	if !slices.Equal(recorder.calls, want) {
		t.Fatalf("calls %v, want %v", recorder.calls, want)
	}
	upgrade := getUpgrade(t, "node-a")
	for _, name := range []string{v1alpha1.StepPreload, v1alpha1.StepBackup} {
		status := stepStatus(upgrade, name)
		if status.State != v1alpha1.StepSucceeded || status.Attempt != 1 || status.StartedAt == nil || status.FinishedAt == nil {
			t.Fatalf("%s status %+v", name, status)
		}
	}
	if stepStatus(upgrade, v1alpha1.StepBackup).Message != "/b.tar.gz sha256:x" || upgrade.Status.ObservedGeneration != upgrade.Generation {
		t.Fatalf("status %+v", upgrade.Status)
	}
	if err := RunUpgrades(context.Background(), k8sClient, deps, steps); err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != 2 {
		t.Fatalf("succeeded steps must not run again: %v", recorder.calls)
	}
}

func TestRunUpgradesStopsAtFailureAndRetriesAtHigherAttempt(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepPreload, v1alpha1.StepBackup)
	recorder := &stepRecorder{}
	steps := map[string]Step{v1alpha1.StepPreload: recorder.step(v1alpha1.StepPreload, Outcome{}, errors.New("depot unreachable")), v1alpha1.StepBackup: recorder.step(v1alpha1.StepBackup, Outcome{}, nil)}
	deps := upgradeDeps(t, &host.FakeExec{}, "boot-1")
	for range 2 {
		if err := RunUpgrades(context.Background(), k8sClient, deps, steps); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorder.calls) != 1 {
		t.Fatalf("a failed step must block later steps and not rerun at the same attempt: %v", recorder.calls)
	}
	failed := stepStatus(getUpgrade(t, "node-a"), v1alpha1.StepPreload)
	if failed.State != v1alpha1.StepFailed || failed.Message != "depot unreachable" || failed.Attempt != 1 {
		t.Fatalf("failed status %+v", failed)
	}
	raiseAttempt(t, "node-a")
	steps[v1alpha1.StepPreload] = recorder.step(v1alpha1.StepPreload, Outcome{}, nil)
	if err := RunUpgrades(context.Background(), k8sClient, deps, steps); err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != 3 {
		t.Fatalf("a higher attempt must rerun the failed step and continue: %v", recorder.calls)
	}
	if status := stepStatus(getUpgrade(t, "node-a"), v1alpha1.StepPreload); status.State != v1alpha1.StepSucceeded || status.Attempt != 2 {
		t.Fatalf("retried status %+v", status)
	}
}

func TestRunUpgradesRunsCleanupAfterFailure(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepPreload, v1alpha1.StepBackup, v1alpha1.StepCleanup)
	recorder := &stepRecorder{}
	steps := map[string]Step{
		v1alpha1.StepPreload: recorder.step(v1alpha1.StepPreload, Outcome{}, errors.New("disk full")),
		v1alpha1.StepBackup:  recorder.step(v1alpha1.StepBackup, Outcome{}, nil),
		v1alpha1.StepCleanup: recorder.step(v1alpha1.StepCleanup, Outcome{}, nil),
	}
	if err := RunUpgrades(context.Background(), k8sClient, upgradeDeps(t, &host.FakeExec{}, "boot-1"), steps); err != nil {
		t.Fatal(err)
	}
	want := []string{"Preload@v0.3.0<-v0.2.0", "Cleanup@v0.3.0<-v0.2.0"}
	if !slices.Equal(recorder.calls, want) {
		t.Fatalf("calls %v, want %v", recorder.calls, want)
	}
}

func TestRunUpgradesRestoresAfterK0sUpdate(t *testing.T) {
	cases := map[string]struct {
		k0sUpdate *v1alpha1.NodeUpgradeStepStatus
		want      []string
	}{
		"K0sUpdate pending":   {nil, []string{"K0sUpdate@v0.3.0<-v0.2.0", "Restore@v0.3.0<-v0.2.0"}},
		"K0sUpdate succeeded": {&v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepSucceeded, Attempt: 1}, []string{"Restore@v0.3.0<-v0.2.0"}},
		"K0sUpdate failed":    {&v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepFailed, Attempt: 1, Message: "k0s is not ready"}, []string{"Restore@v0.3.0<-v0.2.0"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			createReleases(t)
			createUpgrade(t, "node-a", v1alpha1.StepK0sUpdate, v1alpha1.StepRestore)
			if tc.k0sUpdate != nil {
				recordStatus(t, "node-a", v1alpha1.NodeUpgradeStatus{Steps: []v1alpha1.NodeUpgradeStepStatus{*tc.k0sUpdate}})
			}
			recorder := &stepRecorder{}
			steps := map[string]Step{
				v1alpha1.StepK0sUpdate: recorder.step(v1alpha1.StepK0sUpdate, Outcome{}, nil),
				v1alpha1.StepRestore:   recorder.step(v1alpha1.StepRestore, Outcome{Message: "restored"}, nil),
			}
			if err := RunUpgrades(context.Background(), k8sClient, upgradeDeps(t, &host.FakeExec{}, "boot-1"), steps); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(recorder.calls, tc.want) {
				t.Fatalf("calls %v, want %v", recorder.calls, tc.want)
			}
			if status := stepStatus(getUpgrade(t, "node-a"), v1alpha1.StepRestore); status.State != v1alpha1.StepSucceeded {
				t.Fatalf("restore status %+v", status)
			}
		})
	}
}

func TestRunUpgradesRestartsAfterAgentUpdate(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepAgentUpdate, v1alpha1.StepReboot)
	recorder := &stepRecorder{}
	steps := map[string]Step{v1alpha1.StepAgentUpdate: recorder.step(v1alpha1.StepAgentUpdate, Outcome{Restart: true}, nil), v1alpha1.StepReboot: recorder.step(v1alpha1.StepReboot, Outcome{}, nil)}
	exec := &host.FakeExec{Responses: map[string]string{"systemctl restart --no-block bedrock-agent.service": ""}}
	if err := RunUpgrades(context.Background(), k8sClient, upgradeDeps(t, exec, "boot-1"), steps); err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != 1 || stepStatus(getUpgrade(t, "node-a"), v1alpha1.StepAgentUpdate).State != v1alpha1.StepSucceeded {
		t.Fatalf("the restart must follow the Succeeded write and stop the walk: calls %v", recorder.calls)
	}
	if !slices.Equal(hostChanges(exec.Calls), []string{"systemctl restart --no-block bedrock-agent.service"}) {
		t.Fatalf("calls %v", exec.Calls)
	}
}

func TestRunUpgradesRebootsAndFinishesAfterBoot(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepReboot, v1alpha1.StepPrune)
	recorder := &stepRecorder{}
	steps := map[string]Step{v1alpha1.StepReboot: recorder.step(v1alpha1.StepReboot, Outcome{Reboot: true}, nil), v1alpha1.StepPrune: recorder.step(v1alpha1.StepPrune, Outcome{}, nil)}
	exec := &host.FakeExec{Responses: map[string]string{"systemctl reboot": ""}}
	deps := upgradeDeps(t, exec, "boot-1")
	if err := RunUpgrades(context.Background(), k8sClient, deps, steps); err != nil {
		t.Fatal(err)
	}
	upgrade := getUpgrade(t, "node-a")
	if stepStatus(upgrade, v1alpha1.StepReboot).State != v1alpha1.StepRunning || upgrade.Status.BootID != "boot-1" {
		t.Fatalf("before the reboot: %+v", upgrade.Status)
	}
	if !slices.Equal(hostChanges(exec.Calls), []string{"systemctl reboot"}) {
		t.Fatalf("calls %v", exec.Calls)
	}
	writeBootID(t, deps.Root, "boot-2")
	if err := RunUpgrades(context.Background(), k8sClient, deps, steps); err != nil {
		t.Fatal(err)
	}
	upgrade = getUpgrade(t, "node-a")
	if status := stepStatus(upgrade, v1alpha1.StepReboot); status.State != v1alpha1.StepSucceeded || status.Message != "rebooted" {
		t.Fatalf("after the reboot: %+v", status)
	}
	if !slices.Equal(recorder.calls, []string{"Reboot@v0.3.0<-v0.2.0", "Prune@v0.3.0<-v0.2.0"}) {
		t.Fatalf("calls %v", recorder.calls)
	}
}

func recordStatus(t *testing.T, node string, status v1alpha1.NodeUpgradeStatus) {
	t.Helper()
	upgrade := getUpgrade(t, node)
	upgrade.Status = status
	if err := k8sClient.Status().Update(context.Background(), &upgrade); err != nil {
		t.Fatal(err)
	}
}

func runningSince(name string, started time.Time) v1alpha1.NodeUpgradeStepStatus {
	startedAt := metav1.NewTime(started)
	return v1alpha1.NodeUpgradeStepStatus{Name: name, State: v1alpha1.StepRunning, Attempt: 1, StartedAt: &startedAt}
}

func writeRestoreFixture(t *testing.T, root string, completed time.Time) {
	t.Helper()
	marker := `{"backup":"/var/lib/bedrock/backups/bedrock-v0.2.0-20261001T090000Z.tar.gz","completedAt":"` + completed.UTC().Format(time.RFC3339) + `"}`
	writeFixtureFile(t, filepath.Join(root, "var/lib/bedrock/restore.json"), marker)
}

func TestRunUpgradesDoesNotRerunAStepFromBeforeTheRestore(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepBackup, v1alpha1.StepCleanup)
	started := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	recordStatus(t, "node-a", v1alpha1.NodeUpgradeStatus{Steps: []v1alpha1.NodeUpgradeStepStatus{runningSince(v1alpha1.StepBackup, started)}})
	deps := upgradeDeps(t, &host.FakeExec{}, "boot-1")
	writeRestoreFixture(t, deps.Root, time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC))
	recorder := &stepRecorder{}
	steps := map[string]Step{v1alpha1.StepBackup: recorder.step(v1alpha1.StepBackup, Outcome{Message: "/b.tar.gz sha256:x"}, nil), v1alpha1.StepCleanup: recorder.step(v1alpha1.StepCleanup, Outcome{}, nil)}
	if err := RunUpgrades(context.Background(), k8sClient, deps, steps); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(recorder.calls, []string{"Cleanup@v0.3.0<-v0.2.0"}) {
		t.Fatalf("a step that was Running in the restored snapshot must not run again, Cleanup runs alone: calls %v", recorder.calls)
	}
	upgrade := getUpgrade(t, "node-a")
	if status := stepStatus(upgrade, v1alpha1.StepCleanup); status.State != v1alpha1.StepSucceeded {
		t.Fatalf("cleanup status %+v", status)
	}
	if status := stepStatus(upgrade, v1alpha1.StepBackup); status.State != v1alpha1.StepRunning || status.StartedAt == nil || !status.StartedAt.Time.Equal(started) {
		t.Fatalf("backup status %+v", status)
	}
}

func TestRunUpgradesRerunsAStepStartedAfterTheRestore(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepCleanup)
	recordStatus(t, "node-a", v1alpha1.NodeUpgradeStatus{Steps: []v1alpha1.NodeUpgradeStepStatus{runningSince(v1alpha1.StepCleanup, time.Date(2026, 10, 1, 9, 45, 0, 0, time.UTC))}})
	deps := upgradeDeps(t, &host.FakeExec{}, "boot-1")
	writeRestoreFixture(t, deps.Root, time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC))
	recorder := &stepRecorder{}
	steps := map[string]Step{v1alpha1.StepCleanup: recorder.step(v1alpha1.StepCleanup, Outcome{}, nil)}
	if err := RunUpgrades(context.Background(), k8sClient, deps, steps); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(recorder.calls, []string{"Cleanup@v0.3.0<-v0.2.0"}) {
		t.Fatalf("a step interrupted after the restore must run again: calls %v", recorder.calls)
	}
}

func TestRunUpgradesDoesNotFinishARebootFromBeforeTheRestore(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepReboot, v1alpha1.StepPrune)
	recordStatus(t, "node-a", v1alpha1.NodeUpgradeStatus{BootID: "boot-0", Steps: []v1alpha1.NodeUpgradeStepStatus{runningSince(v1alpha1.StepReboot, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))}})
	exec := &host.FakeExec{}
	deps := upgradeDeps(t, exec, "boot-1")
	writeRestoreFixture(t, deps.Root, time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC))
	recorder := &stepRecorder{}
	steps := map[string]Step{v1alpha1.StepReboot: recorder.step(v1alpha1.StepReboot, Outcome{Reboot: true}, nil), v1alpha1.StepPrune: recorder.step(v1alpha1.StepPrune, Outcome{}, nil)}
	if err := RunUpgrades(context.Background(), k8sClient, deps, steps); err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != 0 || len(exec.Calls) != 0 {
		t.Fatalf("a Reboot from before the restore must neither run nor finish: calls %v, host calls %v", recorder.calls, exec.Calls)
	}
	if status := stepStatus(getUpgrade(t, "node-a"), v1alpha1.StepReboot); status.State != v1alpha1.StepRunning {
		t.Fatalf("reboot status %+v", status)
	}
}

func TestRunUpgradesDoesNotRerunARestoreFromBeforeTheMarker(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepRestore, v1alpha1.StepCleanup)
	recordStatus(t, "node-a", v1alpha1.NodeUpgradeStatus{Steps: []v1alpha1.NodeUpgradeStepStatus{runningSince(v1alpha1.StepRestore, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))}})
	deps := upgradeDeps(t, &host.FakeExec{}, "boot-1")
	writeRestoreFixture(t, deps.Root, time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC))
	recorder := &stepRecorder{}
	steps := map[string]Step{v1alpha1.StepRestore: recorder.step(v1alpha1.StepRestore, Outcome{}, nil), v1alpha1.StepCleanup: recorder.step(v1alpha1.StepCleanup, Outcome{}, nil)}
	if err := RunUpgrades(context.Background(), k8sClient, deps, steps); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(recorder.calls, []string{"Cleanup@v0.3.0<-v0.2.0"}) {
		t.Fatalf("the marker proves the restore completed, it must not run again: calls %v", recorder.calls)
	}
	if status := stepStatus(getUpgrade(t, "node-a"), v1alpha1.StepRestore); status.State != v1alpha1.StepRunning {
		t.Fatalf("restore status %+v", status)
	}
}

func TestRunUpgradesIgnoresARestoreMarkerFromTheFuture(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepReboot, v1alpha1.StepPrune)
	recordStatus(t, "node-a", v1alpha1.NodeUpgradeStatus{BootID: "boot-0", Steps: []v1alpha1.NodeUpgradeStepStatus{runningSince(v1alpha1.StepReboot, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))}})
	deps := upgradeDeps(t, &host.FakeExec{}, "boot-1")
	writeRestoreFixture(t, deps.Root, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	recorder := &stepRecorder{}
	steps := map[string]Step{v1alpha1.StepReboot: recorder.step(v1alpha1.StepReboot, Outcome{Reboot: true}, nil), v1alpha1.StepPrune: recorder.step(v1alpha1.StepPrune, Outcome{}, nil)}
	if err := RunUpgrades(context.Background(), k8sClient, deps, steps); err != nil {
		t.Fatal(err)
	}
	if status := stepStatus(getUpgrade(t, "node-a"), v1alpha1.StepReboot); status.State != v1alpha1.StepSucceeded || status.Message != "rebooted" {
		t.Fatalf("a marker after now must not block the Reboot: %+v", status)
	}
	if !slices.Equal(recorder.calls, []string{"Prune@v0.3.0<-v0.2.0"}) {
		t.Fatalf("calls %v", recorder.calls)
	}
}

func TestRunUpgradesOnlyTouchesItsOwnNode(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-b", v1alpha1.StepPreload)
	recorder := &stepRecorder{}
	steps := map[string]Step{v1alpha1.StepPreload: recorder.step(v1alpha1.StepPreload, Outcome{}, nil)}
	if err := RunUpgrades(context.Background(), k8sClient, upgradeDeps(t, &host.FakeExec{}, "boot-1"), steps); err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != 0 || len(getUpgrade(t, "node-b").Status.Steps) != 0 {
		t.Fatalf("node-a must not run node-b's steps: %v", recorder.calls)
	}
}

func TestRunUpgradesFailsAStepThisAgentLacks(t *testing.T) {
	createReleases(t)
	createUpgrade(t, "node-a", v1alpha1.StepOSUpdate)
	if err := RunUpgrades(context.Background(), k8sClient, upgradeDeps(t, &host.FakeExec{}, "boot-1"), map[string]Step{}); err != nil {
		t.Fatal(err)
	}
	if status := stepStatus(getUpgrade(t, "node-a"), v1alpha1.StepOSUpdate); status.State != v1alpha1.StepFailed || status.Message != "this agent has no step OSUpdate" {
		t.Fatalf("status %+v", status)
	}
}
