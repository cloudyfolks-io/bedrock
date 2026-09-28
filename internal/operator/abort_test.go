package operator

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const backupLocationText = "host:node-a:/var/lib/bedrock/backups/bedrock-v1-20261001T100200Z.tar.gz"

func TestDecideAborts(t *testing.T) {
	aborting := func(phase, failedPhase string) v1alpha1.Cluster {
		cluster := upgradeCluster("v2", "v1", nil)
		cluster.Spec.Upgrade.Action = v1alpha1.UpgradeActionAbort
		cluster.Status.Phase = phase
		cluster.Status.Upgrade = &v1alpha1.UpgradeStatus{From: "v1", To: "v2", Attempt: 1, FailedPhase: failedPhase}
		return cluster
	}
	blocked := aborting(v1alpha1.PhaseIdle, "")
	blocked.Status.Upgrade = nil
	cases := map[string]struct {
		cluster v1alpha1.Cluster
		want    string
	}{
		"blocked before a start":  {blocked, decisionAbort},
		"preflight":               {aborting(v1alpha1.PhasePreflight, ""), decisionAbort},
		"backup failed":           {aborting(v1alpha1.PhaseFailed, v1alpha1.PhaseBackup), decisionAbort},
		"preload":                 {aborting(v1alpha1.PhasePreload, ""), decisionAbort},
		"controlplane":            {aborting(v1alpha1.PhaseControlPlane, ""), decisionRestore},
		"controlplane failed":     {aborting(v1alpha1.PhaseFailed, v1alpha1.PhaseControlPlane), decisionRestore},
		"components still refuse": {aborting(v1alpha1.PhaseComponents, ""), decisionRefuseAbort},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := decide(tc.cluster); got.Kind != tc.want {
				t.Fatalf("decision %+v, want %s", got, tc.want)
			}
		})
	}
}

func TestParseBackupLocation(t *testing.T) {
	node, path, ok := parseBackupLocation(backupLocationText)
	if !ok || node != "node-a" || path != "/var/lib/bedrock/backups/bedrock-v1-20261001T100200Z.tar.gz" {
		t.Fatalf("node %q path %q ok %v", node, path, ok)
	}
	for _, bad := range []string{"", "node-a:/backup.tar.gz", "host::/backup.tar.gz", "host:node-a:backup.tar.gz"} {
		if _, _, ok := parseBackupLocation(bad); ok {
			t.Fatalf("%q must not parse", bad)
		}
	}
}

func TestRestoreFinished(t *testing.T) {
	started := metav1.NewTime(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	upgrade := &v1alpha1.UpgradeStatus{From: "v1", To: "v2", StartedAt: started, Attempt: 1}
	restoredAt := func(at time.Time) v1alpha1.Host {
		host := hostWithRoles("node-a", v1alpha1.RoleControlPlane)
		host.Status.Restore = &v1alpha1.RestoreStatus{Backup: "/b.tar.gz", CompletedAt: metav1.NewTime(at)}
		return host
	}
	if restoreFinished([]v1alpha1.Host{restoredAt(started.Add(time.Hour))}, nil) {
		t.Fatal("no upgrade, no restore to finish")
	}
	if restoreFinished([]v1alpha1.Host{restoredAt(started.Add(-time.Hour)), hostWithRoles("node-b")}, upgrade) {
		t.Fatal("a restore from before this upgrade does not count")
	}
	if !restoreFinished([]v1alpha1.Host{hostWithRoles("node-b"), restoredAt(started.Add(time.Hour))}, upgrade) {
		t.Fatal("a restore after the start counts")
	}
}

func TestAbortTransforms(t *testing.T) {
	status := inPhase(v1alpha1.PhasePreload)
	status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionUpgradeBlocked, Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonBlocked}}
	aborted := abortUpgrade(status, "cleanup failed on node-b: boom", 7)
	progressing := meta.FindStatusCondition(aborted.Conditions, v1alpha1.ConditionProgressing)
	blocked := meta.FindStatusCondition(aborted.Conditions, v1alpha1.ConditionUpgradeBlocked)
	if aborted.Phase != v1alpha1.PhaseIdle || aborted.Upgrade != nil || aborted.Version != "v1" || progressing.Reason != v1alpha1.ReasonAborted || progressing.Message != "cleanup failed on node-b: boom" || progressing.ObservedGeneration != 7 || blocked.Status != metav1.ConditionFalse {
		t.Fatalf("aborted %+v", aborted)
	}
	restoringStatus := restoring(inPhase(v1alpha1.PhaseControlPlane), "restoring "+backupLocationText, 7)
	if restoringStatus.Phase != v1alpha1.PhaseControlPlane || restoringStatus.Upgrade.Message != "restoring "+backupLocationText || meta.FindStatusCondition(restoringStatus.Conditions, v1alpha1.ConditionProgressing).Reason != v1alpha1.ReasonRestoring {
		t.Fatalf("restoring %+v", restoringStatus)
	}
	manual := restoreManual(inPhase(v1alpha1.PhaseControlPlane), "restore by hand", 7)
	if manual.Phase != v1alpha1.PhaseFailed || manual.Upgrade.FailedPhase != v1alpha1.PhaseControlPlane || manual.Upgrade.Message != "restore by hand" || meta.FindStatusCondition(manual.Conditions, v1alpha1.ConditionProgressing).Reason != v1alpha1.ReasonRestoreManual {
		t.Fatalf("manual %+v", manual)
	}
}

func TestAbortUpgradeKeepsAnEarlierReport(t *testing.T) {
	status := v1alpha1.ClusterStatus{Phase: v1alpha1.PhaseIdle, Version: "v1"}
	setCondition(&status, v1alpha1.ConditionProgressing, metav1.ConditionFalse, v1alpha1.ReasonAborted, "cleanup failed on node-b: x", 1)
	got := abortUpgrade(status, "", 2)
	if !reflect.DeepEqual(got, status) {
		t.Fatalf("abortUpgrade must keep an already-aborted status unchanged: %+v", got)
	}
}

func createReleases(t *testing.T, ctx context.Context, c client.Client) {
	t.Helper()
	for _, version := range []string{"v1", "v2"} {
		rel := &v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: version}, Spec: v1alpha1.ReleaseSpec{Version: version, Image: "ghcr.io/cloudyfolks-labs/bedrock:" + version, K0sVersion: targetK0s, K0sChecksums: map[string]string{"amd64": amd64Checksum}}}
		if err := c.Create(ctx, rel); err != nil {
			t.Fatal(err)
		}
	}
}

func abortWorld(t *testing.T, phase string) (client.Client, context.Context) {
	t.Helper()
	c, _ := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	createOperatorDeployment(t, ctx, c, "ghcr.io/cloudyfolks-labs/bedrock:v2")
	createReleases(t, ctx, c)
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	createDepotHost(t, ctx, c, "node-b", v1alpha1.RoleWorkload)
	preloadedNodeUpgrade(t, ctx, c, "node-a")
	preloadedNodeUpgrade(t, ctx, c, "node-b")
	status := upgradeStatusIn(phase)
	status.Upgrade.Backup = backupLocationText
	createClusterWithStatus(t, ctx, c, "v2", status)
	return c, ctx
}

func removeNode(t *testing.T, ctx context.Context, c client.Client, name string) {
	t.Helper()
	for _, obj := range []client.Object{
		&v1alpha1.NodeUpgrade{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.NodeUpgradeName("v2", name)}},
		&v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}},
	} {
		if err := client.IgnoreNotFound(c.Delete(ctx, obj)); err != nil {
			t.Fatal(err)
		}
	}
}

func runRole(t *testing.T, ctx context.Context, c client.Client, role upgradeRole) v1alpha1.Cluster {
	t.Helper()
	if _, err := runUpgrade(ctx, upgradeEnv{Client: c}, getCluster(t, ctx, c), role); err != nil {
		t.Fatal(err)
	}
	return getCluster(t, ctx, c)
}

func TestAbortBeforeControlPlane(t *testing.T) {
	c, ctx := abortWorld(t, v1alpha1.PhasePreload)
	setAction(t, ctx, c, v1alpha1.UpgradeActionAbort)

	waiting := runRole(t, ctx, c, oldRole())
	if waiting.Status.Upgrade.Message != "abort: cleanup 0/2 nodes, waiting for node-a, node-b" || waiting.Spec.Upgrade.Action != v1alpha1.UpgradeActionAbort {
		t.Fatalf("status %+v action %q", waiting.Status.Upgrade, waiting.Spec.Upgrade.Action)
	}
	if image := operatorImage(t, ctx, c).Spec.Template.Spec.Containers[1].Image; image != "ghcr.io/cloudyfolks-labs/bedrock:v1" {
		t.Fatalf("the old operator must take its image back: %s", image)
	}
	for _, node := range []string{"node-a", "node-b"} {
		if steps := getNodeUpgrade(t, ctx, c, node).Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload, v1alpha1.StepCleanup}) {
			t.Fatalf("%s steps %v", node, steps)
		}
	}
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepCleanup, State: v1alpha1.StepSucceeded, Attempt: 1})
	reportStep(t, ctx, c, "node-b", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepCleanup, State: v1alpha1.StepFailed, Attempt: 1, Message: "k0s ctr images rm: exit status 1"})

	done := runRole(t, ctx, c, oldRole())
	progressing := meta.FindStatusCondition(done.Status.Conditions, v1alpha1.ConditionProgressing)
	if done.Status.Phase != v1alpha1.PhaseIdle || done.Status.Upgrade != nil || done.Status.Version != "v1" || done.Spec.DesiredVersion != "v1" || done.Spec.Upgrade.Action != "" {
		t.Fatalf("spec %+v status %+v", done.Spec, done.Status)
	}
	if progressing.Reason != v1alpha1.ReasonAborted || progressing.Message != "cleanup failed on node-b: k0s ctr images rm: exit status 1" {
		t.Fatalf("progressing %+v", progressing)
	}
	if upgrades, err := listNodeUpgrades(ctx, c, "v2"); err != nil || len(upgrades) != 0 {
		t.Fatalf("node upgrades %d %v", len(upgrades), err)
	}
}

func TestAbortRerunKeepsTheCleanupReport(t *testing.T) {
	c, ctx := abortWorld(t, v1alpha1.PhasePreload)
	setAction(t, ctx, c, v1alpha1.UpgradeActionAbort)
	runRole(t, ctx, c, oldRole())
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepCleanup, State: v1alpha1.StepSucceeded, Attempt: 1})
	reportStep(t, ctx, c, "node-b", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepCleanup, State: v1alpha1.StepFailed, Attempt: 1, Message: "k0s ctr images rm: exit status 1"})
	done := runRole(t, ctx, c, oldRole())
	wantMessage := "cleanup failed on node-b: k0s ctr images rm: exit status 1"
	if got := meta.FindStatusCondition(done.Status.Conditions, v1alpha1.ConditionProgressing).Message; got != wantMessage {
		t.Fatalf("setup: progressing message %q", got)
	}

	stale := getCluster(t, ctx, c)
	stale.Spec.DesiredVersion = "v2"
	stale.Spec.Upgrade.Action = v1alpha1.UpgradeActionAbort
	if err := c.Update(ctx, &stale); err != nil {
		t.Fatal(err)
	}

	rerun := runRole(t, ctx, c, oldRole())
	progressing := meta.FindStatusCondition(rerun.Status.Conditions, v1alpha1.ConditionProgressing)
	if progressing.Message != wantMessage {
		t.Fatalf("a rerun must keep the cleanup report: %q", progressing.Message)
	}
	if rerun.Spec.DesiredVersion != rerun.Status.Version || rerun.Spec.Upgrade.Action != "" {
		t.Fatalf("spec %+v status.version %s", rerun.Spec, rerun.Status.Version)
	}
}

func TestAbortEndsWhenItsSpecPatchIsLost(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhasePreload))
	cluster := getCluster(t, ctx, c)
	cluster.Status = abortUpgrade(cluster.Status, "", cluster.Generation)
	if err := c.Status().Update(ctx, &cluster); err != nil {
		t.Fatal(err)
	}
	got := runRole(t, ctx, c, oldRole())
	if got.Spec.DesiredVersion != got.Status.Version || got.Spec.Upgrade.Action != "" || got.Status.Upgrade != nil || got.Status.Phase != v1alpha1.PhaseIdle {
		t.Fatalf("an abort that lost its spec patch must end, not start again: spec %+v status %+v", got.Spec, got.Status)
	}
}

func TestAbortWhenBlocked(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createClusterWithStatus(t, ctx, c, "v2", v1alpha1.ClusterStatus{Version: "v1", Phase: v1alpha1.PhaseIdle})
	setAction(t, ctx, c, v1alpha1.UpgradeActionAbort)
	done := runRole(t, ctx, c, oldRole())
	if done.Spec.DesiredVersion != "v1" || done.Spec.Upgrade.Action != "" || meta.FindStatusCondition(done.Status.Conditions, v1alpha1.ConditionProgressing).Reason != v1alpha1.ReasonAborted {
		t.Fatalf("spec %+v status %+v", done.Spec, done.Status)
	}
}

func TestAbortOnTheNewOperatorHandsBack(t *testing.T) {
	c, ctx := abortWorld(t, v1alpha1.PhasePreload)
	setAction(t, ctx, c, v1alpha1.UpgradeActionAbort)
	got := runRole(t, ctx, c, newRole())
	if got.Status.Upgrade.Message != "abort: operator switching back to ghcr.io/cloudyfolks-labs/bedrock:v1" || got.Spec.Upgrade.Action != v1alpha1.UpgradeActionAbort {
		t.Fatalf("status %+v action %q", got.Status.Upgrade, got.Spec.Upgrade.Action)
	}
	if image := operatorImage(t, ctx, c).Spec.Template.Spec.Containers[1].Image; image != "ghcr.io/cloudyfolks-labs/bedrock:v1" {
		t.Fatalf("image %s", image)
	}
	if steps := getNodeUpgrade(t, ctx, c, "node-a").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload}) {
		t.Fatalf("the new operator leaves the cleanup to the old one: %v", steps)
	}
}

func appendK0sUpdate(t *testing.T, ctx context.Context, c client.Client, node string) {
	t.Helper()
	upgrade := getNodeUpgrade(t, ctx, c, node)
	upgrade.Spec.Steps = append(upgrade.Spec.Steps, v1alpha1.StepK0sUpdate)
	if err := c.Update(ctx, &upgrade); err != nil {
		t.Fatal(err)
	}
}

func TestAbortInControlPlaneRestoresASingleController(t *testing.T) {
	cases := map[string]struct {
		k0sUpdate *v1alpha1.NodeUpgradeStepStatus
		steps     []string
	}{
		"before K0sUpdate":    {nil, []string{v1alpha1.StepPreload, v1alpha1.StepRestore}},
		"K0sUpdate pending":   {&v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepPending}, []string{v1alpha1.StepPreload, v1alpha1.StepK0sUpdate, v1alpha1.StepRestore}},
		"K0sUpdate succeeded": {&v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepSucceeded, Attempt: 1, Message: "k0s v1.36.3+k0s.0 installed, restarted k0scontroller.service"}, []string{v1alpha1.StepPreload, v1alpha1.StepK0sUpdate, v1alpha1.StepRestore}},
		"K0sUpdate failed":    {&v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepFailed, Attempt: 1, Message: "k0s v1.36.3+k0s.0 is not ready after 10m0s: /readyz: exit status 1"}, []string{v1alpha1.StepPreload, v1alpha1.StepK0sUpdate, v1alpha1.StepRestore}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, ctx := abortWorld(t, v1alpha1.PhaseControlPlane)
			removeNode(t, ctx, c, "node-b")
			if tc.k0sUpdate != nil {
				appendK0sUpdate(t, ctx, c, "node-a")
				reportStep(t, ctx, c, "node-a", *tc.k0sUpdate)
			}
			setAction(t, ctx, c, v1alpha1.UpgradeActionAbort)

			got := runRole(t, ctx, c, newRole())
			progressing := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionProgressing)
			if got.Status.Phase != v1alpha1.PhaseControlPlane || progressing.Reason != v1alpha1.ReasonRestoring || got.Status.Upgrade.Message != "restoring "+backupLocationText || got.Spec.Upgrade.Action != v1alpha1.UpgradeActionAbort {
				t.Fatalf("status %+v action %q", got.Status, got.Spec.Upgrade.Action)
			}
			restore := getNodeUpgrade(t, ctx, c, "node-a")
			if !slices.Equal(restore.Spec.Steps, tc.steps) || restore.Spec.Backup != "/var/lib/bedrock/backups/bedrock-v1-20261001T100200Z.tar.gz" {
				t.Fatalf("restore spec %+v", restore.Spec)
			}
			if tc.k0sUpdate != nil && currentStep(restore, v1alpha1.StepK0sUpdate).State != tc.k0sUpdate.State {
				t.Fatalf("the abort must leave K0sUpdate %s: %+v", tc.k0sUpdate.State, restore.Status.Steps)
			}
			if image := operatorImage(t, ctx, c).Spec.Template.Spec.Containers[1].Image; image != "ghcr.io/cloudyfolks-labs/bedrock:v1" {
				t.Fatalf("image %s", image)
			}

			waiting := runRole(t, ctx, c, oldRole())
			if waiting.Status.Upgrade.Message != "abort: waiting for the restore of "+backupLocationText+" (Pending)" {
				t.Fatalf("message %q", waiting.Status.Upgrade.Message)
			}
			reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepRestore, State: v1alpha1.StepFailed, Attempt: 1, Message: "k0s restore: exit status 1"})
			failed := runRole(t, ctx, c, oldRole())
			manual := meta.FindStatusCondition(failed.Status.Conditions, v1alpha1.ConditionProgressing)
			if failed.Status.Phase != v1alpha1.PhaseFailed || manual.Reason != v1alpha1.ReasonRestoreManual || failed.Spec.Upgrade.Action != "" || failed.Status.Upgrade.Message != "restore on node-a failed: k0s restore: exit status 1: follow "+restoreRunbook {
				t.Fatalf("status %+v action %q", failed.Status.Upgrade, failed.Spec.Upgrade.Action)
			}
		})
	}
}

func TestResumeAfterAManualRestoreIsRefused(t *testing.T) {
	c, ctx := abortWorld(t, v1alpha1.PhaseControlPlane)
	cluster := getCluster(t, ctx, c)
	manual := "restore on node-a failed: k0s restore: exit status 1: follow " + restoreRunbook
	cluster.Status = restoreManual(cluster.Status, manual, cluster.Generation)
	if err := c.Status().Update(ctx, &cluster); err != nil {
		t.Fatal(err)
	}
	setAction(t, ctx, c, v1alpha1.UpgradeActionResume)
	got := runRole(t, ctx, c, oldRole())
	progressing := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionProgressing)
	if got.Status.Phase != v1alpha1.PhaseFailed || got.Spec.Upgrade.Action != "" || progressing.Reason != v1alpha1.ReasonRestoreManual || progressing.Message != manual {
		t.Fatalf("resume must be refused after a manual restore: action %q status %+v", got.Spec.Upgrade.Action, got.Status)
	}
	if got.Status.Upgrade.Message != "resume is not possible after a manual restore: follow "+restoreRunbook || got.Status.Upgrade.FailedPhase != v1alpha1.PhaseControlPlane || got.Status.Upgrade.Attempt != 1 {
		t.Fatalf("upgrade %+v", got.Status.Upgrade)
	}
}

func TestAbortInControlPlaneWithManyControllers(t *testing.T) {
	c, ctx := abortWorld(t, v1alpha1.PhaseControlPlane)
	createDepotHost(t, ctx, c, "node-c", v1alpha1.RoleControlPlane)
	setAction(t, ctx, c, v1alpha1.UpgradeActionAbort)
	got := runRole(t, ctx, c, newRole())
	progressing := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionProgressing)
	want := "abort in ControlPlane needs a manual restore of " + backupLocationText + " on 2 controllers: follow " + restoreRunbook
	if got.Status.Phase != v1alpha1.PhaseFailed || progressing.Reason != v1alpha1.ReasonRestoreManual || got.Status.Upgrade.Message != want || got.Spec.Upgrade.Action != "" {
		t.Fatalf("status %+v action %q", got.Status.Upgrade, got.Spec.Upgrade.Action)
	}
	if image := operatorImage(t, ctx, c).Spec.Template.Spec.Containers[1].Image; image != "ghcr.io/cloudyfolks-labs/bedrock:v2" {
		t.Fatalf("a manual restore leaves the operator alone: %s", image)
	}
}

func TestAbortInControlPlaneWithWorkersGoesManual(t *testing.T) {
	c, ctx := abortWorld(t, v1alpha1.PhaseControlPlane)
	setAction(t, ctx, c, v1alpha1.UpgradeActionAbort)
	got := runRole(t, ctx, c, newRole())
	progressing := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionProgressing)
	want := "abort in ControlPlane needs a manual restore of " + backupLocationText + ": the cluster has 2 nodes, and the automatic restore runs only on a single node: follow " + restoreRunbook
	if got.Status.Phase != v1alpha1.PhaseFailed || progressing.Reason != v1alpha1.ReasonRestoreManual || got.Status.Upgrade.Message != want || got.Spec.Upgrade.Action != "" {
		t.Fatalf("status %+v action %q", got.Status.Upgrade, got.Spec.Upgrade.Action)
	}
	if image := operatorImage(t, ctx, c).Spec.Template.Spec.Containers[1].Image; image != "ghcr.io/cloudyfolks-labs/bedrock:v2" {
		t.Fatalf("a manual restore leaves the operator alone: %s", image)
	}
	if restore := getNodeUpgrade(t, ctx, c, "node-a"); slices.Contains(restore.Spec.Steps, v1alpha1.StepRestore) {
		t.Fatalf("no automatic restore on a cluster with workers: %v", restore.Spec.Steps)
	}
}

func TestAbortInControlPlaneWithNoDepotGoesManual(t *testing.T) {
	c, _ := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	createOperatorDeployment(t, ctx, c, "ghcr.io/cloudyfolks-labs/bedrock:v2")
	createReleases(t, ctx, c)
	createHostWithStatus(t, ctx, c, healthyHost("node-a"))
	createNodeWithStatus(t, ctx, c, readyNode("node-a", "amd64"))
	status := upgradeStatusIn(v1alpha1.PhaseControlPlane)
	status.Upgrade.Backup = backupLocationText
	createClusterWithStatus(t, ctx, c, "v2", status)
	setAction(t, ctx, c, v1alpha1.UpgradeActionAbort)

	got := runRole(t, ctx, c, newRole())
	progressing := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionProgressing)
	want := "abort in ControlPlane needs a manual restore of " + backupLocationText + ": no host serves v2 for amd64: follow " + restoreRunbook
	if got.Status.Phase != v1alpha1.PhaseFailed || progressing.Reason != v1alpha1.ReasonRestoreManual || got.Status.Upgrade.Message != want || got.Spec.Upgrade.Action != "" {
		t.Fatalf("status %+v action %q", got.Status.Upgrade, got.Spec.Upgrade.Action)
	}
}

func TestFinishedRestoreEndsTheUpgrade(t *testing.T) {
	c, ctx := abortWorld(t, v1alpha1.PhaseBackup)
	cluster := getCluster(t, ctx, c)
	var host v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: "node-a"}, &host); err != nil {
		t.Fatal(err)
	}
	host.Status.Restore = &v1alpha1.RestoreStatus{Backup: "/var/lib/bedrock/backups/bedrock-v1-20261001T100200Z.tar.gz", CompletedAt: metav1.NewTime(cluster.Status.Upgrade.StartedAt.Add(time.Hour))}
	if err := c.Status().Update(ctx, &host); err != nil {
		t.Fatal(err)
	}
	var calls int
	role := oldRole()
	role.Phases = map[string]phaseFunc{v1alpha1.PhaseBackup: func(context.Context, upgradeEnv, v1alpha1.Cluster) (phaseResult, error) {
		calls++
		return phaseResult{Done: true}, nil
	}}

	cleaning := runRole(t, ctx, c, role)
	if calls != 0 || cleaning.Status.Upgrade.Message != "abort: cleanup 0/2 nodes, waiting for node-a, node-b" {
		t.Fatalf("a finished restore must win over the phase: calls %d status %+v", calls, cleaning.Status.Upgrade)
	}
	finishSteps(t, ctx, c, "node-a", v1alpha1.StepCleanup)
	finishSteps(t, ctx, c, "node-b", v1alpha1.StepCleanup)
	done := runRole(t, ctx, c, role)
	if done.Status.Phase != v1alpha1.PhaseIdle || done.Spec.DesiredVersion != "v1" || meta.FindStatusCondition(done.Status.Conditions, v1alpha1.ConditionProgressing).Reason != v1alpha1.ReasonAborted {
		t.Fatalf("spec %+v status %+v", done.Spec, done.Status)
	}
}

func TestRoles(t *testing.T) {
	old, next := oldRole(), newRole()
	if old.Abort == nil || old.Restore == nil || next.Abort == nil || next.Restore == nil {
		t.Fatal("both roles need abort and restore actions")
	}
	if !reflect.DeepEqual(mapKeys(old.Phases), []string{v1alpha1.PhaseBackup, v1alpha1.PhasePreflight, v1alpha1.PhasePreload}) {
		t.Fatalf("old phases %v", mapKeys(old.Phases))
	}
	if len(next.Phases) != len(upgradePhases()) {
		t.Fatalf("new phases %v", mapKeys(next.Phases))
	}
}

func mapKeys(phases map[string]phaseFunc) []string {
	keys := make([]string, 0, len(phases))
	for key := range phases {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
