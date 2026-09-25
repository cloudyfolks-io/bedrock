package operator

import (
	"context"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-labs/bedrock/internal/release"
)

func TestPreloadPhase(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	createDepotHost(t, ctx, c, "node-b", v1alpha1.RoleWorkload)
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhasePreload))
	backupStep := v1alpha1.NodeUpgrade{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.NodeUpgradeName("v2", "node-a")}, Spec: v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v2", From: "v1", Depot: "http://10.0.0.11:9480/v2/amd64", Attempt: 1, Steps: []string{v1alpha1.StepBackup}}}
	if err := c.Create(ctx, &backupStep); err != nil {
		t.Fatal(err)
	}
	env := upgradeEnv{Client: c}
	run := func() phaseResult {
		t.Helper()
		result, err := preload(ctx, env, getCluster(t, ctx, c))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	if got := run(); got != (phaseResult{Message: "preload 0/2 nodes, waiting for node-a, node-b"}) {
		t.Fatalf("result %+v", got)
	}
	if steps := getNodeUpgrade(t, ctx, c, "node-a").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepBackup, v1alpha1.StepPreload}) {
		t.Fatalf("node-a steps %v", steps)
	}
	if steps := getNodeUpgrade(t, ctx, c, "node-b").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload}) {
		t.Fatalf("node-b steps %v", steps)
	}

	createDepotHost(t, ctx, c, "node-c", v1alpha1.RoleWorkload)
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepPreload, State: v1alpha1.StepSucceeded, Attempt: 1})
	if got := run(); got != (phaseResult{Message: "preload 1/3 nodes, waiting for node-b, node-c"}) {
		t.Fatalf("a Host that joins must get its own NodeUpgrade: %+v", got)
	}
	if joined := getNodeUpgrade(t, ctx, c, "node-c"); !slices.Equal(joined.Spec.Steps, []string{v1alpha1.StepPreload}) || joined.Spec.Depot != "http://10.0.0.11:9480/v2/amd64" {
		t.Fatalf("node-c spec %+v", joined.Spec)
	}

	reportStep(t, ctx, c, "node-b", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepPreload, State: v1alpha1.StepFailed, Attempt: 1, Message: "k0s/k0s: want sha256:aa, got sha256:bb"})
	if got := run(); got != (phaseResult{Failure: "preload failed on node-b: k0s/k0s: want sha256:aa, got sha256:bb"}) {
		t.Fatalf("result %+v", got)
	}
	reportStep(t, ctx, c, "node-b", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepPreload, State: v1alpha1.StepSucceeded, Attempt: 1})
	reportStep(t, ctx, c, "node-c", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepPreload, State: v1alpha1.StepSucceeded, Attempt: 1})
	if got := run(); got != (phaseResult{Done: true}) {
		t.Fatalf("result %+v", got)
	}
}

func createOperatorDeployment(t *testing.T, ctx context.Context, c client.Client, image string) {
	t.Helper()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release.SystemNamespace}}); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"app": operatorDeployment}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: operatorDeployment, Namespace: release.SystemNamespace},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "sidecar", Image: "registry.example/sidecar:1"},
					{Name: operatorContainer, Image: image, Args: []string{"operator"}, Env: []corev1.EnvVar{{Name: "BEDROCK_RELEASE_DIR", Value: "/release"}}},
				}},
			},
		},
	}
	if err := c.Create(ctx, deployment); err != nil {
		t.Fatal(err)
	}
}

func operatorImage(t *testing.T, ctx context.Context, c client.Client) appsv1.Deployment {
	t.Helper()
	var deployment appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: operatorDeployment}, &deployment); err != nil {
		t.Fatal(err)
	}
	return deployment
}

func TestPreloadThenSwitch(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createOperatorDeployment(t, ctx, c, "ghcr.io/cloudyfolks-labs/bedrock:v1")
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhasePreload))
	target := &v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v2"}, Spec: v1alpha1.ReleaseSpec{Version: "v2", Image: "ghcr.io/cloudyfolks-labs/bedrock:v2", UpgradeFrom: []string{"v1"}}}
	if err := c.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	env := upgradeEnv{Client: c}
	if got, err := preloadThenSwitch(ctx, env, getCluster(t, ctx, c)); err != nil || got != (phaseResult{Message: "preload 0/1 nodes, waiting for node-a"}) {
		t.Fatalf("result %+v err %v", got, err)
	}
	if image := operatorImage(t, ctx, c).Spec.Template.Spec.Containers[1].Image; image != "ghcr.io/cloudyfolks-labs/bedrock:v1" {
		t.Fatalf("the switch must wait for Preload: %s", image)
	}
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepPreload, State: v1alpha1.StepSucceeded, Attempt: 1})
	for range 2 {
		got, err := preloadThenSwitch(ctx, env, getCluster(t, ctx, c))
		if err != nil || got != (phaseResult{Message: "preload done, operator switching to ghcr.io/cloudyfolks-labs/bedrock:v2"}) {
			t.Fatalf("result %+v err %v", got, err)
		}
	}
	containers := operatorImage(t, ctx, c).Spec.Template.Spec.Containers
	if containers[1].Image != "ghcr.io/cloudyfolks-labs/bedrock:v2" || containers[0].Image != "registry.example/sidecar:1" || containers[1].Env[0].Value != "/release" || containers[1].Args[0] != "operator" {
		t.Fatalf("containers %+v", containers)
	}
}

func TestSetOperatorImageNeedsTheContainer(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	createOperatorDeployment(t, ctx, c, "ghcr.io/cloudyfolks-labs/bedrock:v1")
	deployment := operatorImage(t, ctx, c)
	deployment.Spec.Template.Spec.Containers[1].Name = "renamed"
	if err := c.Update(ctx, &deployment); err != nil {
		t.Fatal(err)
	}
	if err := setOperatorImage(ctx, c, "ghcr.io/cloudyfolks-labs/bedrock:v2"); err == nil || err.Error() != "deployment bedrock-system/bedrock-operator has no container operator" {
		t.Fatalf("error %v", err)
	}
}

func TestBackupAndPreloadPhaseTables(t *testing.T) {
	for name, phases := range map[string]map[string]phaseFunc{"old": oldPhases(), "new": newPhases()} {
		for _, phase := range []string{v1alpha1.PhasePreflight, v1alpha1.PhaseBackup, v1alpha1.PhasePreload} {
			if phases[phase] == nil {
				t.Fatalf("the %s operator must run %s", name, phase)
			}
		}
	}
}
