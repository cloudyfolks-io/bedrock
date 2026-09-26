package operator

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const (
	amd64Checksum = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	arm64Checksum = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	targetK0s     = "v1.36.3+k0s.0"
)

func targetRelease() v1alpha1.Release {
	return v1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "v2"},
		Spec:       v1alpha1.ReleaseSpec{Version: "v2", Image: "ghcr.io/cloudyfolks-labs/bedrock:v2", K0sVersion: targetK0s, UpgradeFrom: []string{"v1"}, K0sChecksums: map[string]string{"amd64": amd64Checksum, "arm64": arm64Checksum}},
	}
}

func setK0sVersion(t *testing.T, ctx context.Context, c client.Client, name, version string) {
	t.Helper()
	var host v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &host); err != nil {
		t.Fatal(err)
	}
	host.Status.K0sVersion = version
	if err := c.Status().Update(ctx, &host); err != nil {
		t.Fatal(err)
	}
}

func controlPlaneWorld(t *testing.T) (client.Client, context.Context, func(phaseResult)) {
	t.Helper()
	c, _ := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-a"}}); err != nil {
		t.Fatal(err)
	}
	target := targetRelease()
	if err := c.Create(ctx, &target); err != nil {
		t.Fatal(err)
	}
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseControlPlane))
	env := upgradeEnv{Client: c}
	run := func(want phaseResult) {
		t.Helper()
		got, err := controlPlane(ctx, env, getCluster(t, ctx, c))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("result %+v, want %+v", got, want)
		}
	}
	return c, ctx, run
}

func runningPod(t *testing.T, ctx context.Context, c client.Client, name, node string) {
	t.Helper()
	pod := podOn(name, node)
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	createPod(t, ctx, c, pod)
}

func TestControlPlanePhaseOnASingleNode(t *testing.T) {
	c, ctx, run := controlPlaneWorld(t)
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	setEtcdMembers(t, ctx, c, "node-a", 1)
	preloadedNodeUpgrade(t, ctx, c, "node-a")
	runningPod(t, ctx, c, "web", "node-a")

	run(phaseResult{Message: "controlplane: node-a cordoned"})
	cordoned := getNodeUpgrade(t, ctx, c, "node-a")
	if !getNode(t, ctx, c, "node-a").Spec.Unschedulable || cordoned.Annotations[controlPlaneProgressAnnotation] != nodeDraining || cordoned.Annotations[workerProgressAnnotation] != "" {
		t.Fatalf("node-a must be cordoned and marked draining for ControlPlane only: %v", cordoned.Annotations)
	}
	run(phaseResult{Message: "controlplane: node-a drained"})
	var kept corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-a", Name: "web"}, &kept); err != nil || kept.DeletionTimestamp != nil {
		t.Fatalf("the only schedulable node is not drained: %v", err)
	}
	if steps := getNodeUpgrade(t, ctx, c, "node-a").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload, v1alpha1.StepK0sUpdate}) {
		t.Fatalf("node-a steps %v", steps)
	}
	run(phaseResult{Message: "controlplane: updating node-a: K0sUpdate Pending"})
	finishSteps(t, ctx, c, "node-a", v1alpha1.StepK0sUpdate)
	run(phaseResult{Message: "controlplane: waiting for k0s v1.36.3+k0s.0 on node-a"})
	setK0sVersion(t, ctx, c, "node-a", targetK0s)
	run(phaseResult{Message: "controlplane: node-a uncordoned"})
	if getNode(t, ctx, c, "node-a").Spec.Unschedulable {
		t.Fatal("node-a must be uncordoned")
	}
	run(phaseResult{Message: "controlplane: node-a done"})
	run(phaseResult{Done: true})
	setK0sVersion(t, ctx, c, "node-a", "v1.36.2+k0s.0")
	run(phaseResult{Message: "controlplane: waiting for k0s v1.36.3+k0s.0 on node-a"})
}

func TestControlPlanePhaseWalksControllersOneAtATime(t *testing.T) {
	c, ctx, run := controlPlaneWorld(t)
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	createDepotHost(t, ctx, c, "node-b", v1alpha1.RoleWorkload)
	controllerOnly := hostWithRoles("node-c", v1alpha1.RoleControlPlane)
	controllerOnly.Status = v1alpha1.HostStatus{Hostname: "node-c", Checks: &v1alpha1.HostChecks{TimeSynced: true}}
	createHostWithStatus(t, ctx, c, controllerOnly)
	for _, node := range []string{"node-a", "node-b", "node-c"} {
		preloadedNodeUpgrade(t, ctx, c, node)
	}
	runningPod(t, ctx, c, "web", "node-a")

	run(phaseResult{Message: "controlplane: node-a cordoned"})
	run(phaseResult{Message: "controlplane: draining node-a: tenant-a/web"})
	var evicted corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-a", Name: "web"}, &evicted); err != nil || evicted.DeletionTimestamp == nil {
		t.Fatalf("web must be evicted: %v", err)
	}
	if err := c.Delete(ctx, &evicted, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	run(phaseResult{Message: "controlplane: node-a drained"})
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepFailed, Attempt: 1, Message: "staged k0s: checksum mismatch"})
	run(phaseResult{Failure: "controlplane: node-a K0sUpdate failed: staged k0s: checksum mismatch"})
	if err := raiseAttempts(ctx, c, "v2", 2); err != nil {
		t.Fatal(err)
	}
	run(phaseResult{Message: "controlplane: updating node-a: K0sUpdate Pending"})
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepSucceeded, Attempt: 2})
	setK0sVersion(t, ctx, c, "node-a", targetK0s)
	run(phaseResult{Message: "controlplane: node-a uncordoned"})
	run(phaseResult{Message: "controlplane: node-a: etcd: node-a reports 0 of 2 members"})
	if steps := getNodeUpgrade(t, ctx, c, "node-c").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload}) {
		t.Fatalf("node-c must wait for node-a: %v", steps)
	}
	setEtcdMembers(t, ctx, c, "node-a", 2)
	setEtcdMembers(t, ctx, c, "node-c", 2)
	run(phaseResult{Message: "controlplane: node-a done"})

	run(phaseResult{Message: "controlplane: node-c cordoned"})
	run(phaseResult{Message: "controlplane: node-c drained"})
	if steps := getNodeUpgrade(t, ctx, c, "node-c").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload, v1alpha1.StepK0sUpdate}) {
		t.Fatalf("node-c steps %v", steps)
	}
	finishSteps(t, ctx, c, "node-c", v1alpha1.StepK0sUpdate)
	run(phaseResult{Message: "controlplane: waiting for k0s v1.36.3+k0s.0 on node-c"})
	setK0sVersion(t, ctx, c, "node-c", targetK0s)
	run(phaseResult{Message: "controlplane: node-c uncordoned"})
	run(phaseResult{Message: "controlplane: node-c done"})
	run(phaseResult{Done: true})

	worker := getNodeUpgrade(t, ctx, c, "node-b")
	if !slices.Equal(worker.Spec.Steps, []string{v1alpha1.StepPreload}) || worker.Annotations[controlPlaneProgressAnnotation] != "" || getNode(t, ctx, c, "node-b").Spec.Unschedulable {
		t.Fatalf("ControlPlane must leave the worker alone: steps %v annotations %v", worker.Spec.Steps, worker.Annotations)
	}
}

func TestControlPlanePhaseTable(t *testing.T) {
	if newPhases()[v1alpha1.PhaseControlPlane] == nil {
		t.Fatal("the new operator runs ControlPlane")
	}
	if oldPhases()[v1alpha1.PhaseControlPlane] != nil {
		t.Fatal("the old operator never runs ControlPlane")
	}
}
