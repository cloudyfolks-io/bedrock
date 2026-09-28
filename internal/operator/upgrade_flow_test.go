package operator

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	oldImage = "ghcr.io/cloudyfolks-io/bedrock:v1"
	newImage = "ghcr.io/cloudyfolks-io/bedrock:v2"
)

type upgradeWorld struct {
	ctx                      context.Context
	client                   client.Client
	oldOperator, newOperator *ClusterReconciler
}

func flowBundle(t *testing.T, version, image string, upgradeFrom []string) release.Bundle {
	t.Helper()
	bundle := rbacBundle(t)
	bundle.Spec.Version = version
	bundle.Spec.Image = image
	bundle.Spec.K0sVersion = targetK0s
	bundle.Spec.K0sChecksums = map[string]string{"amd64": amd64Checksum}
	bundle.Spec.UpgradeFrom = upgradeFrom
	return bundle
}

func newUpgradeWorld(t *testing.T) upgradeWorld {
	t.Helper()
	return newUpgradeWorldAs(t, func(_ *testing.T, _ context.Context, admin client.Client, _ *rest.Config) client.Client { return admin })
}

func newSingleNodeUpgradeWorld(t *testing.T) upgradeWorld {
	t.Helper()
	w := newUpgradeWorld(t)
	removeNode(t, w.ctx, w.client, "node-b")
	return w
}

func newUpgradeWorldAs(t *testing.T, operatorClient func(*testing.T, context.Context, client.Client, *rest.Config) client.Client) upgradeWorld {
	t.Helper()
	c, cfg := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	createOperatorDeployment(t, ctx, c, oldImage)
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	createDepotHost(t, ctx, c, "node-b", v1alpha1.RoleWorkload)
	setEtcd(t, ctx, c, "node-a", 1, true)
	oldBundle := flowBundle(t, "v1", oldImage, nil)
	newBundle := flowBundle(t, "v2", newImage, []string{"v1"})
	target := &v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v2", Labels: map[string]string{v1alpha1.LabelKind: "Release", v1alpha1.LabelName: "v2"}}, Spec: newBundle.Spec}
	if err := c.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	createClusterWithStatus(t, ctx, c, "v2", v1alpha1.ClusterStatus{Version: "v1", Phase: v1alpha1.PhaseIdle})
	operatorView := operatorClient(t, ctx, c, cfg)
	operator := func(bundle release.Bundle) *ClusterReconciler {
		return &ClusterReconciler{
			Client: operatorView, APIReader: operatorView, Bundle: bundle, Gates: release.Gates{}, Interval: 50 * time.Millisecond, GroupTimeout: 10 * time.Second,
			Exec: func(context.Context, string, string, string, []string) ([]byte, error) { return nil, nil },
			Dial: func(context.Context, string) error { return nil },
		}
	}
	return upgradeWorld{ctx: ctx, client: c, oldOperator: operator(oldBundle), newOperator: operator(newBundle)}
}

func runningOperator(t *testing.T, w upgradeWorld) (string, *ClusterReconciler) {
	t.Helper()
	if operatorImage(t, w.ctx, w.client).Spec.Template.Spec.Containers[1].Image == newImage {
		return "new", w.newOperator
	}
	return "old", w.oldOperator
}

func reconcileWorld(t *testing.T, w upgradeWorld, restarts int) (string, []string) {
	t.Helper()
	name, operator := runningOperator(t, w)
	phases := make([]string, 0, restarts+1)
	for range restarts + 1 {
		if _, err := operator.Reconcile(w.ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: v1alpha1.ClusterName}}); err != nil {
			t.Fatalf("%s operator: %v", name, err)
		}
		phases = append(phases, getCluster(t, w.ctx, w.client).Status.Phase)
	}
	fakeAgents(t, w.ctx, w.client)
	fakeKubelets(t, w.ctx, w.client)
	fakeKubeVirt(t, w.ctx, w.client)
	return name, phases
}

func fakeStepMessage(upgrade v1alpha1.NodeUpgrade, step string) string {
	if step == v1alpha1.StepBackup {
		return "/var/lib/bedrock/backups/bedrock-" + upgrade.Spec.From + "-20261001T100200Z.tar.gz sha256:" + strings.Repeat("ab", 32)
	}
	return step + " done"
}

func fakeAgents(t *testing.T, ctx context.Context, c client.Client) {
	t.Helper()
	var list v1alpha1.NodeUpgradeList
	if err := c.List(ctx, &list); err != nil {
		t.Fatal(err)
	}
	for _, upgrade := range list.Items {
		steps := slices.Clone(upgrade.Status.Steps)
		for _, name := range upgrade.Spec.Steps {
			if currentStep(upgrade, name).State == v1alpha1.StepSucceeded {
				continue
			}
			finished := metav1.Now()
			steps = append(slices.DeleteFunc(steps, func(step v1alpha1.NodeUpgradeStepStatus) bool { return step.Name == name }), v1alpha1.NodeUpgradeStepStatus{Name: name, State: v1alpha1.StepSucceeded, Attempt: upgrade.Spec.Attempt, Message: fakeStepMessage(upgrade, name), FinishedAt: &finished})
			fakeStepEffect(t, ctx, c, upgrade, name)
		}
		if len(steps) == len(upgrade.Status.Steps) && slices.EqualFunc(steps, upgrade.Status.Steps, func(a, b v1alpha1.NodeUpgradeStepStatus) bool {
			return a.Name == b.Name && a.State == b.State && a.Attempt == b.Attempt
		}) {
			continue
		}
		upgrade.Status.Steps = steps
		if err := c.Status().Update(ctx, &upgrade); err != nil {
			t.Fatal(err)
		}
	}
}

func fakeStepEffect(t *testing.T, ctx context.Context, c client.Client, upgrade v1alpha1.NodeUpgrade, step string) {
	t.Helper()
	var host v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: upgrade.Spec.Node}, &host); err != nil {
		t.Fatal(err)
	}
	switch step {
	case v1alpha1.StepK0sUpdate:
		var target v1alpha1.Release
		if err := c.Get(ctx, client.ObjectKey{Name: upgrade.Spec.Version}, &target); err != nil {
			t.Fatal(err)
		}
		host.Status.K0sVersion = target.Spec.K0sVersion
	case v1alpha1.StepAgentUpdate:
		host.Status.AgentVersion = upgrade.Spec.Version
	case v1alpha1.StepRestore:
		host.Status.Restore = &v1alpha1.RestoreStatus{Backup: upgrade.Spec.Backup, CompletedAt: metav1.NewTime(time.Now().Add(time.Second))}
	default:
		return
	}
	if err := c.Status().Update(ctx, &host); err != nil {
		t.Fatal(err)
	}
}

func fakeKubelets(t *testing.T, ctx context.Context, c client.Client) {
	t.Helper()
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes); err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes.Items {
		renewLease(t, ctx, c, node.Name, time.Now().Add(time.Second))
	}
}

func fakeKubeVirt(t *testing.T, ctx context.Context, c client.Client) {
	t.Helper()
	if _, ok := smokeVMExists(t, ctx, c); ok {
		setVMIStatus(t, ctx, c, map[string]any{"phase": "Running", "interfaces": []any{map[string]any{"ipAddress": "10.244.1.7"}}})
	}
}

type flowEnd struct {
	phases   []string
	owners   map[string][]string
	cluster  v1alpha1.Cluster
	attempts int
}

func runFlow(t *testing.T, w upgradeWorld, restarts int, until func(v1alpha1.Cluster) bool, act func(v1alpha1.Cluster)) flowEnd {
	t.Helper()
	end := flowEnd{owners: map[string][]string{}}
	for end.attempts = 0; end.attempts < 200; end.attempts++ {
		owner, phases := reconcileWorld(t, w, restarts)
		for _, phase := range phases {
			if len(end.phases) == 0 || end.phases[len(end.phases)-1] != phase {
				end.phases = append(end.phases, phase)
			}
			if !slices.Contains(end.owners[phase], owner) {
				end.owners[phase] = append(end.owners[phase], owner)
			}
		}
		cluster := getCluster(t, w.ctx, w.client)
		end.cluster = cluster
		if until(cluster) {
			return end
		}
		act(cluster)
	}
	t.Fatalf("the upgrade did not end: phases %v status %+v", end.phases, end.cluster.Status.Upgrade)
	return end
}

func upgraded(cluster v1alpha1.Cluster) bool {
	return cluster.Status.Phase == v1alpha1.PhaseIdle && cluster.Status.Version == "v2"
}

func fullSequence() []string {
	return append(upgradePhases(), v1alpha1.PhaseIdle)
}

func assertUpgraded(t *testing.T, w upgradeWorld, end flowEnd) {
	t.Helper()
	if !slices.Equal(end.phases, fullSequence()) {
		t.Fatalf("phases %v, want %v", end.phases, fullSequence())
	}
	for _, phase := range []string{v1alpha1.PhaseControlPlane, v1alpha1.PhaseComponents, v1alpha1.PhaseWorkers, v1alpha1.PhaseVerify} {
		if slices.Contains(end.owners[phase], "old") {
			t.Fatalf("the old operator ran %s", phase)
		}
	}
	available := meta.FindStatusCondition(end.cluster.Status.Conditions, v1alpha1.ConditionAvailable)
	progressing := meta.FindStatusCondition(end.cluster.Status.Conditions, v1alpha1.ConditionProgressing)
	if available.Reason != v1alpha1.ReasonUpgraded || progressing.Reason != v1alpha1.ReasonUpgraded || end.cluster.Status.Upgrade != nil {
		t.Fatalf("status %+v", end.cluster.Status)
	}
	if upgrades, err := listNodeUpgrades(w.ctx, w.client, "v2"); err != nil || len(upgrades) != 0 {
		t.Fatalf("node upgrades left: %d %v", len(upgrades), err)
	}
	for _, name := range []string{"node-a", "node-b"} {
		var host v1alpha1.Host
		if err := w.client.Get(w.ctx, client.ObjectKey{Name: name}, &host); err != nil {
			t.Fatal(err)
		}
		if host.Status.K0sVersion != targetK0s || host.Status.AgentVersion != "v2" {
			t.Fatalf("%s k0s %q agent %q", name, host.Status.K0sVersion, host.Status.AgentVersion)
		}
		if getNode(t, w.ctx, w.client, name).Spec.Unschedulable {
			t.Fatalf("%s is still cordoned", name)
		}
	}
	if image, _ := runningOperator(t, w); image != "new" {
		t.Fatal("operator v2 must run at the end")
	}
	if len(end.cluster.Status.Backups) != 1 || !strings.HasPrefix(end.cluster.Status.Backups[0].Location, "host:node-a:/var/lib/bedrock/backups/bedrock-v1-") {
		t.Fatalf("backups %+v", end.cluster.Status.Backups)
	}
}

func TestUpgradeFlow(t *testing.T) {
	w := newUpgradeWorld(t)
	end := runFlow(t, w, 0, upgraded, func(v1alpha1.Cluster) {})
	assertUpgraded(t, w, end)
}

func TestUpgradeFlowSurvivesARestartAfterEveryReconcile(t *testing.T) {
	w := newUpgradeWorld(t)
	end := runFlow(t, w, 1, upgraded, func(v1alpha1.Cluster) {})
	assertUpgraded(t, w, end)
}

func aborted(cluster v1alpha1.Cluster) bool {
	progressing := meta.FindStatusCondition(cluster.Status.Conditions, v1alpha1.ConditionProgressing)
	return cluster.Status.Phase == v1alpha1.PhaseIdle && progressing != nil && progressing.Reason == v1alpha1.ReasonAborted
}

func abortIn(t *testing.T, w upgradeWorld, phase string, afterSwitch bool) func(v1alpha1.Cluster) {
	sent := false
	return func(cluster v1alpha1.Cluster) {
		switched := operatorImage(t, w.ctx, w.client).Spec.Template.Spec.Containers[1].Image == newImage
		if sent || cluster.Status.Phase != phase || switched != afterSwitch {
			return
		}
		setAction(t, w.ctx, w.client, v1alpha1.UpgradeActionAbort)
		sent = true
	}
}

func TestUpgradeFlowAbortsBeforeComponents(t *testing.T) {
	cases := map[string]struct {
		world       func(*testing.T) upgradeWorld
		phase       string
		afterSwitch bool
	}{
		"preflight":                              {newUpgradeWorld, v1alpha1.PhasePreflight, false},
		"backup":                                 {newUpgradeWorld, v1alpha1.PhaseBackup, false},
		"preload":                                {newUpgradeWorld, v1alpha1.PhasePreload, false},
		"preload after switch":                   {newUpgradeWorld, v1alpha1.PhasePreload, true},
		"controlplane restores on a single node": {newSingleNodeUpgradeWorld, v1alpha1.PhaseControlPlane, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := tc.world(t)
			end := runFlow(t, w, 0, func(cluster v1alpha1.Cluster) bool {
				return aborted(cluster) || cluster.Status.Phase == v1alpha1.PhaseFailed || upgraded(cluster)
			}, abortIn(t, w, tc.phase, tc.afterSwitch))
			if !aborted(end.cluster) {
				t.Fatalf("phases %v status %+v upgrade %+v", end.phases, end.cluster.Status, end.cluster.Status.Upgrade)
			}
			if end.cluster.Status.Version != "v1" || end.cluster.Spec.DesiredVersion != "v1" || end.cluster.Spec.Upgrade.Action != "" {
				t.Fatalf("spec %+v status %+v", end.cluster.Spec, end.cluster.Status)
			}
			if image, _ := runningOperator(t, w); image != "old" {
				t.Fatal("operator v1 must run after an abort")
			}
			if upgrades, err := listNodeUpgrades(w.ctx, w.client, "v2"); err != nil || len(upgrades) != 0 {
				t.Fatalf("node upgrades left: %d %v", len(upgrades), err)
			}
		})
	}
}

func TestUpgradeFlowRefusesAbortAfterComponentsAndResumes(t *testing.T) {
	for _, phase := range []string{v1alpha1.PhaseComponents, v1alpha1.PhaseWorkers, v1alpha1.PhaseVerify} {
		t.Run(phase, func(t *testing.T) {
			w := newUpgradeWorld(t)
			refused := runFlow(t, w, 0, func(cluster v1alpha1.Cluster) bool {
				return cluster.Status.Phase == v1alpha1.PhaseFailed || upgraded(cluster)
			}, abortIn(t, w, phase, true))
			if refused.cluster.Status.Phase != v1alpha1.PhaseFailed || refused.cluster.Status.Upgrade.FailedPhase != phase {
				t.Fatalf("phases %v upgrade %+v", refused.phases, refused.cluster.Status.Upgrade)
			}
			setAction(t, w.ctx, w.client, v1alpha1.UpgradeActionResume)
			end := runFlow(t, w, 0, upgraded, func(v1alpha1.Cluster) {})
			if end.cluster.Status.Version != "v2" {
				t.Fatalf("status %+v", end.cluster.Status)
			}
		})
	}
}
