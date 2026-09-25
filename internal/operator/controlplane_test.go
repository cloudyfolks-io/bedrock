package operator

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"
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

func TestControlPlanePhase(t *testing.T) {
	c, _ := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	createDepotHost(t, ctx, c, "node-b", v1alpha1.RoleWorkload)
	target := targetRelease()
	if err := c.Create(ctx, &target); err != nil {
		t.Fatal(err)
	}
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseControlPlane))
	env := upgradeEnv{Client: c}
	run := func() phaseResult {
		t.Helper()
		result, err := controlPlane(ctx, env, getCluster(t, ctx, c))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	if got := run(); got != (phaseResult{Message: "controlplane: creating autopilot plan v2-controlplane-1"}) {
		t.Fatalf("result %+v", got)
	}
	plan := getPlan(t, ctx, c)
	commands, _, _ := unstructured.NestedSlice(plan.Object, "spec", "commands")
	update := commands[0].(map[string]any)["k0supdate"].(map[string]any)
	controllers, _, _ := unstructured.NestedStringSlice(update, "targets", "controllers", "discovery", "static", "nodes")
	workers, _, _ := unstructured.NestedStringSlice(update, "targets", "workers", "discovery", "static", "nodes")
	url, _, _ := unstructured.NestedString(update, "platforms", "linux-amd64", "url")
	if !reflect.DeepEqual(controllers, []string{"node-a"}) || len(workers) != 0 || update["version"] != targetK0s || url != "http://10.0.0.11:9480/v2/amd64/k0s/k0s" {
		t.Fatalf("plan spec %+v", plan.Object["spec"])
	}
	if got := run(); got != (phaseResult{Message: "controlplane: autopilot plan v2-controlplane-1 is new"}) {
		t.Fatalf("result %+v", got)
	}
	setPlanState(t, ctx, c, "Completed")
	setK0sVersion(t, ctx, c, "node-a", "v1.36.2+k0s.0")
	if got := run(); got != (phaseResult{Message: "controlplane: waiting for k0s v1.36.3+k0s.0 on node-a"}) {
		t.Fatalf("result %+v", got)
	}
	setK0sVersion(t, ctx, c, "node-a", targetK0s)
	if got := run(); got != (phaseResult{Done: true}) {
		t.Fatalf("result %+v", got)
	}
	setPlanState(t, ctx, c, "IncompleteTargets")
	if got := run(); got != (phaseResult{Failure: "controlplane: autopilot plan v2-controlplane-1 is IncompleteTargets"}) {
		t.Fatalf("result %+v", got)
	}

	resumed := getCluster(t, ctx, c)
	resumed.Status.Upgrade.Attempt = 2
	if err := c.Status().Update(ctx, &resumed); err != nil {
		t.Fatal(err)
	}
	if got := run(); got != (phaseResult{Message: "controlplane: replacing autopilot plan v2-controlplane-1"}) {
		t.Fatalf("result %+v", got)
	}
	gone := &unstructured.Unstructured{}
	gone.SetGroupVersionKind(planGVK)
	if err := c.Get(ctx, client.ObjectKey{Name: autopilotPlanName}, gone); !errors.IsNotFound(err) {
		t.Fatalf("the old plan must be deleted: %v", err)
	}
	if got := run(); got != (phaseResult{Message: "controlplane: creating autopilot plan v2-controlplane-2"}) {
		t.Fatalf("result %+v", got)
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
