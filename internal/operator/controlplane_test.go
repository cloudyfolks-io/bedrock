package operator

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	sigyaml "sigs.k8s.io/yaml"

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

func TestAutopilotPlan(t *testing.T) {
	platforms := map[string]any{"linux-amd64": map[string]any{"url": "http://10.0.0.11:9480/v2/amd64/k0s/k0s", "sha256": "11"}}
	got := autopilotPlan("v2-workers-1", targetK0s, platforms, nil, []string{"node-b", "node-c"}, 2, "2026-10-01T10:00:00Z")
	body, err := sigyaml.YAMLToJSON([]byte(`
apiVersion: autopilot.k0sproject.io/v1beta2
kind: Plan
metadata:
  name: autopilot
spec:
  id: v2-workers-1
  timestamp: "2026-10-01T10:00:00Z"
  commands:
    - k0supdate:
        version: v1.36.3+k0s.0
        platforms:
          linux-amd64:
            url: http://10.0.0.11:9480/v2/amd64/k0s/k0s
            sha256: "11"
        targets:
          controllers:
            discovery:
              static:
                nodes: []
            limits:
              concurrent: 1
          workers:
            discovery:
              static:
                nodes: [node-b, node-c]
            limits:
              concurrent: 2
`))
	if err != nil {
		t.Fatal(err)
	}
	var want unstructured.Unstructured
	if err := want.UnmarshalJSON(body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Object, want.Object) {
		gotYAML, _ := sigyaml.Marshal(got.Object)
		t.Fatalf("plan:\n%s", gotYAML)
	}
}

func TestK0sPlatforms(t *testing.T) {
	hosts := []v1alpha1.Host{healthyPreflight().Hosts[0]}
	got, problem := k0sPlatforms(hosts, targetRelease(), []string{"amd64", "arm64"})
	want := map[string]any{
		"linux-amd64": map[string]any{"url": "http://10.0.0.11:9480/v2/amd64/k0s/k0s", "sha256": "1111111111111111111111111111111111111111111111111111111111111111"},
		"linux-arm64": map[string]any{"url": "http://10.0.0.11:9480/v2/arm64/k0s/k0s", "sha256": "2222222222222222222222222222222222222222222222222222222222222222"},
	}
	if problem != "" || !reflect.DeepEqual(got, want) {
		t.Fatalf("platforms %+v problem %q", got, problem)
	}
	if _, problem := k0sPlatforms(hosts, targetRelease(), []string{"riscv64"}); problem != "no host serves v2 for riscv64" {
		t.Fatalf("problem %q", problem)
	}
	noChecksum := targetRelease()
	noChecksum.Spec.K0sChecksums = map[string]string{"amd64": amd64Checksum}
	if _, problem := k0sPlatforms(hosts, noChecksum, []string{"arm64"}); problem != "Release/v2 has no k0s checksum for arm64" {
		t.Fatalf("problem %q", problem)
	}
}

func planWithStatus(state string, controllers ...map[string]any) *unstructured.Unstructured {
	plan := autopilotPlan("v2-controlplane-1", targetK0s, map[string]any{}, []string{"node-a"}, nil, 1, "2026-10-01T10:00:00Z")
	targets := make([]any, 0, len(controllers))
	for _, controller := range controllers {
		targets = append(targets, controller)
	}
	plan.Object["status"] = map[string]any{"state": state, "commands": []any{map[string]any{"id": int64(0), "state": state, "k0supdate": map[string]any{"controllers": targets}}}}
	return plan
}

func TestPlanProgress(t *testing.T) {
	sent := map[string]any{"name": "node-a", "state": "SignalSent"}
	failed := map[string]any{"name": "node-a", "state": "SignalApplyFailed"}
	cases := map[string]struct {
		plan *unstructured.Unstructured
		want phaseResult
	}{
		"new":       {autopilotPlan("v2-controlplane-1", targetK0s, map[string]any{}, []string{"node-a"}, nil, 1, "now"), phaseResult{Message: "controlplane: autopilot plan v2-controlplane-1 is new"}},
		"running":   {planWithStatus("SchedulableWait", sent), phaseResult{Message: "controlplane: autopilot plan v2-controlplane-1 is SchedulableWait (node-a SignalSent)"}},
		"completed": {planWithStatus("Completed", map[string]any{"name": "node-a", "state": "SignalCompleted"}), phaseResult{Done: true}},
		"failed":    {planWithStatus("ApplyFailed", failed), phaseResult{Failure: "controlplane: autopilot plan v2-controlplane-1 is ApplyFailed (node-a SignalApplyFailed)"}},
		"targets":   {planWithStatus("IncompleteTargets"), phaseResult{Failure: "controlplane: autopilot plan v2-controlplane-1 is IncompleteTargets"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := planProgress("controlplane", tc.plan); got != tc.want {
				t.Fatalf("result %+v, want %+v", got, tc.want)
			}
		})
	}
}

func getPlan(t *testing.T, ctx context.Context, c client.Client) *unstructured.Unstructured {
	t.Helper()
	plan := &unstructured.Unstructured{}
	plan.SetGroupVersionKind(planGVK)
	if err := c.Get(ctx, client.ObjectKey{Name: autopilotPlanName}, plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func setPlanState(t *testing.T, ctx context.Context, c client.Client, state string) {
	t.Helper()
	plan := getPlan(t, ctx, c)
	plan.Object["status"] = map[string]any{"state": state, "commands": []any{map[string]any{"id": int64(0), "state": state}}}
	if err := c.Status().Update(ctx, plan); err != nil {
		t.Fatal(err)
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
	finishSteps(t, ctx, c, "node-a", v1alpha1.StepK0sUpdate)
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
