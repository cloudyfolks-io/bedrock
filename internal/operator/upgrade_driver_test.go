package operator

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

func createClusterWithStatus(t *testing.T, ctx context.Context, c client.Client, desired string, status v1alpha1.ClusterStatus) {
	t.Helper()
	cluster := &v1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.ClusterName}, Spec: v1alpha1.ClusterSpec{DesiredVersion: desired, API: v1alpha1.APISpec{VIP: "10.0.0.10", VIPMode: "arp"}, NodeConcurrency: 1}}
	if err := c.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	cluster.Status = status
	if err := c.Status().Update(ctx, cluster); err != nil {
		t.Fatal(err)
	}
}

func getCluster(t *testing.T, ctx context.Context, c client.Client) v1alpha1.Cluster {
	t.Helper()
	var cluster v1alpha1.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		t.Fatal(err)
	}
	return cluster
}

func setAction(t *testing.T, ctx context.Context, c client.Client, action string) {
	t.Helper()
	cluster := getCluster(t, ctx, c)
	cluster.Spec.Upgrade.Action = action
	if err := c.Update(ctx, &cluster); err != nil {
		t.Fatal(err)
	}
}

func reconcileUpgrade(t *testing.T, ctx context.Context, c client.Client, phases map[string]phaseFunc) v1alpha1.Cluster {
	t.Helper()
	if _, err := runUpgrade(ctx, upgradeEnv{Client: c}, getCluster(t, ctx, c), phases); err != nil {
		t.Fatal(err)
	}
	return getCluster(t, ctx, c)
}

func upgradeStatusIn(phase string) v1alpha1.ClusterStatus {
	now := metav1.Now()
	return v1alpha1.ClusterStatus{Version: "v1", Phase: phase, Upgrade: &v1alpha1.UpgradeStatus{From: "v1", To: "v2", StartedAt: now, PhaseStartedAt: now, Attempt: 1}}
}

func createNodeUpgrade(t *testing.T, ctx context.Context, c client.Client, node string) {
	t.Helper()
	upgrade := &v1alpha1.NodeUpgrade{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.NodeUpgradeName("v2", node)}, Spec: v1alpha1.NodeUpgradeSpec{Node: node, Version: "v2", From: "v1", Attempt: 1, Steps: []string{v1alpha1.StepPreload}}}
	if err := c.Create(ctx, upgrade); err != nil {
		t.Fatal(err)
	}
}

func TestRunUpgradeWalksEveryPhase(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createClusterWithStatus(t, ctx, c, "v2", v1alpha1.ClusterStatus{Version: "v1", Phase: v1alpha1.PhaseIdle})
	var ran []string
	phases := map[string]phaseFunc{}
	for _, phase := range upgradePhases() {
		phases[phase] = func(context.Context, upgradeEnv, v1alpha1.Cluster) (phaseResult, error) {
			ran = append(ran, phase)
			return phaseResult{Done: true}, nil
		}
	}
	var cluster v1alpha1.Cluster
	for range 10 {
		cluster = reconcileUpgrade(t, ctx, c, phases)
		if cluster.Status.Phase == v1alpha1.PhaseIdle {
			break
		}
	}
	if !slices.Equal(ran, upgradePhases()) {
		t.Fatalf("phases ran %v", ran)
	}
	available := meta.FindStatusCondition(cluster.Status.Conditions, v1alpha1.ConditionAvailable)
	if cluster.Status.Version != "v2" || cluster.Status.Upgrade != nil || available == nil || available.Reason != v1alpha1.ReasonUpgraded || available.ObservedGeneration != cluster.Generation {
		t.Fatalf("status %+v", cluster.Status)
	}
}

func TestRunUpgradeFailsAndResumes(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseWorkers))
	createNodeUpgrade(t, ctx, c, "node-a")
	createNodeUpgrade(t, ctx, c, "node-b")
	var calls atomic.Int32
	phases := map[string]phaseFunc{v1alpha1.PhaseWorkers: func(context.Context, upgradeEnv, v1alpha1.Cluster) (phaseResult, error) {
		if calls.Add(1) == 1 {
			return phaseResult{Failure: "node-b: reboot failed"}, nil
		}
		return phaseResult{Done: true}, nil
	}}

	failed := reconcileUpgrade(t, ctx, c, phases)
	progressing := meta.FindStatusCondition(failed.Status.Conditions, v1alpha1.ConditionProgressing)
	if failed.Status.Phase != v1alpha1.PhaseFailed || failed.Status.Upgrade.FailedPhase != v1alpha1.PhaseWorkers || failed.Status.Upgrade.Message != "node-b: reboot failed" || progressing.Reason != v1alpha1.ReasonPhaseFailed {
		t.Fatalf("status %+v upgrade %+v", failed.Status, failed.Status.Upgrade)
	}
	if again := reconcileUpgrade(t, ctx, c, phases); again.Status.Phase != v1alpha1.PhaseFailed || calls.Load() != 1 {
		t.Fatalf("a failed phase must wait for an action: phase %s calls %d", again.Status.Phase, calls.Load())
	}

	setAction(t, ctx, c, v1alpha1.UpgradeActionResume)
	resumed := reconcileUpgrade(t, ctx, c, phases)
	if resumed.Status.Phase != v1alpha1.PhaseWorkers || resumed.Status.Upgrade.Attempt != 2 || resumed.Spec.Upgrade.Action != "" {
		t.Fatalf("resumed %+v upgrade %+v action %q", resumed.Status, resumed.Status.Upgrade, resumed.Spec.Upgrade.Action)
	}
	upgrades, err := listNodeUpgrades(ctx, c, "v2")
	if err != nil || len(upgrades) != 2 {
		t.Fatalf("node upgrades %v %v", upgrades, err)
	}
	for _, upgrade := range upgrades {
		if upgrade.Spec.Attempt != 2 || !slices.Equal(upgrade.Spec.Steps, []string{v1alpha1.StepPreload}) {
			t.Fatalf("%s spec %+v", upgrade.Name, upgrade.Spec)
		}
	}
	if done := reconcileUpgrade(t, ctx, c, phases); done.Status.Phase != v1alpha1.PhaseVerify {
		t.Fatalf("phase %s", done.Status.Phase)
	}
}

func TestRunUpgradeAnswersActionsWithoutMeaning(t *testing.T) {
	t.Run("resume while a phase runs", func(t *testing.T) {
		c, _ := StartTestEnv(t)
		ctx := context.Background()
		createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseBackup))
		setAction(t, ctx, c, v1alpha1.UpgradeActionResume)
		var calls atomic.Int32
		phases := map[string]phaseFunc{v1alpha1.PhaseBackup: func(context.Context, upgradeEnv, v1alpha1.Cluster) (phaseResult, error) {
			calls.Add(1)
			return phaseResult{Message: "backup running"}, nil
		}}
		got := reconcileUpgrade(t, ctx, c, phases)
		if got.Spec.Upgrade.Action != "" || got.Status.Phase != v1alpha1.PhaseBackup || got.Status.Upgrade.Message != "resume ignored: phase Backup has not failed" || calls.Load() != 0 {
			t.Fatalf("action %q status %+v upgrade %+v calls %d", got.Spec.Upgrade.Action, got.Status, got.Status.Upgrade, calls.Load())
		}
	})
	t.Run("abort after Components started", func(t *testing.T) {
		c, _ := StartTestEnv(t)
		ctx := context.Background()
		createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseComponents))
		setAction(t, ctx, c, v1alpha1.UpgradeActionAbort)
		got := reconcileUpgrade(t, ctx, c, map[string]phaseFunc{})
		if got.Spec.Upgrade.Action != "" || got.Status.Phase != v1alpha1.PhaseFailed || got.Status.Upgrade.FailedPhase != v1alpha1.PhaseComponents {
			t.Fatalf("action %q status %+v upgrade %+v", got.Spec.Upgrade.Action, got.Status, got.Status.Upgrade)
		}
		if got.Status.Upgrade.Message != "abort refused in Components: no rollback after Components started; resume continues the upgrade" {
			t.Fatalf("message %q", got.Status.Upgrade.Message)
		}
	})
}

func TestRunUpgradeBlocksAndStartsAgain(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createClusterWithStatus(t, ctx, c, "v2", v1alpha1.ClusterStatus{Version: "v1", Phase: v1alpha1.PhaseIdle})
	var calls atomic.Int32
	phases := map[string]phaseFunc{v1alpha1.PhasePreflight: func(context.Context, upgradeEnv, v1alpha1.Cluster) (phaseResult, error) {
		if calls.Add(1) == 1 {
			return phaseResult{Blocked: "timeSynced: node-a clock not synced"}, nil
		}
		return phaseResult{Done: true}, nil
	}}
	if started := reconcileUpgrade(t, ctx, c, phases); started.Status.Phase != v1alpha1.PhasePreflight || started.Status.Upgrade == nil {
		t.Fatalf("status %+v", started.Status)
	}
	blocked := reconcileUpgrade(t, ctx, c, phases)
	condition := meta.FindStatusCondition(blocked.Status.Conditions, v1alpha1.ConditionUpgradeBlocked)
	if blocked.Status.Phase != v1alpha1.PhaseIdle || blocked.Status.Upgrade != nil || condition == nil || condition.Status != metav1.ConditionTrue || condition.Message != "timeSynced: node-a clock not synced" {
		t.Fatalf("status %+v", blocked.Status)
	}
	reconcileUpgrade(t, ctx, c, phases)
	passed := reconcileUpgrade(t, ctx, c, phases)
	condition = meta.FindStatusCondition(passed.Status.Conditions, v1alpha1.ConditionUpgradeBlocked)
	if passed.Status.Phase != v1alpha1.PhaseBackup || condition.Status != metav1.ConditionFalse {
		t.Fatalf("status %+v", passed.Status)
	}
}

func TestClusterReconcilerStartsAnUpgradeOnTheOldOperator(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createClusterWithStatus(t, ctx, c, "v0.2.0", v1alpha1.ClusterStatus{Version: "v0.1.0-test", Phase: v1alpha1.PhaseIdle})
	r := &ClusterReconciler{Client: c, Bundle: testBundle(t), Gates: release.Gates{}, Interval: 100 * time.Millisecond, GroupTimeout: time.Second, UpgradeInterval: time.Second}
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: v1alpha1.ClusterName}})
	if err != nil {
		t.Fatal(err)
	}
	got := getCluster(t, ctx, c)
	progressing := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionProgressing)
	if got.Status.Phase != v1alpha1.PhasePreflight || got.Status.Upgrade == nil || got.Status.Upgrade.From != "v0.1.0-test" || got.Status.Upgrade.To != "v0.2.0" || got.Status.Upgrade.Attempt != 1 {
		t.Fatalf("status %+v", got.Status)
	}
	if progressing == nil || progressing.Reason != v1alpha1.ReasonUpgrading || progressing.ObservedGeneration != got.Generation || result.RequeueAfter != time.Second {
		t.Fatalf("progressing %+v result %+v", progressing, result)
	}
}

func TestClusterReconcilerIgnoresActionsWithoutAnUpgrade(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createClusterWithStatus(t, ctx, c, "v0.1.0-test", v1alpha1.ClusterStatus{Version: "v0.1.0-test", Phase: v1alpha1.PhaseIdle})
	setAction(t, ctx, c, v1alpha1.UpgradeActionAbort)
	r := &ClusterReconciler{Client: c, Bundle: testBundle(t), Gates: release.Gates{}, Interval: 100 * time.Millisecond, GroupTimeout: time.Second}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: v1alpha1.ClusterName}}); err != nil {
		t.Fatal(err)
	}
	got := getCluster(t, ctx, c)
	progressing := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionProgressing)
	if got.Spec.Upgrade.Action != "" || got.Status.Phase != v1alpha1.PhaseIdle || progressing == nil || progressing.Reason != "ActionIgnored" || progressing.Message != "abort ignored: no upgrade in progress" {
		t.Fatalf("action %q status %+v", got.Spec.Upgrade.Action, got.Status)
	}
}

func TestAddonReconcilerHoldsDuringAnUpgrade(t *testing.T) {
	c, cfg := StartTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhasePreload))
	var renders atomic.Int32
	count := func(AddonInput) (Rendered, error) {
		renders.Add(1)
		return Rendered{}, nil
	}
	mgr, err := ctrl.NewManager(cfg, testManagerOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&AddonReconciler{Client: mgr.GetClient(), Addons: []Addon{{Name: "count", Condition: "CountReady", Render: count}}, Interval: 200 * time.Millisecond, ReadyInterval: time.Second}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	time.Sleep(2 * time.Second)
	if renders.Load() != 0 || clusterCondition(t, c, "CountReady") != nil {
		t.Fatalf("addons must hold during an upgrade: %d renders", renders.Load())
	}
	cluster := getCluster(t, ctx, c)
	cluster.Status.Version = "v2"
	cluster.Status.Upgrade = nil
	cluster.Status.Phase = v1alpha1.PhaseIdle
	if err := c.Status().Update(ctx, &cluster); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		condition := clusterCondition(t, c, "CountReady")
		return condition != nil && condition.Status == metav1.ConditionTrue
	})
}
