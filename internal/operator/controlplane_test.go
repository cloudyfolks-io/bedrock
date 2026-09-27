package operator

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

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

func renewLease(t *testing.T, ctx context.Context, c client.Client, name string, at time.Time) {
	t.Helper()
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: corev1.NamespaceNodeLease}}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, lease, func() error {
		lease.Spec.HolderIdentity = &name
		lease.Spec.RenewTime = &metav1.MicroTime{Time: at}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestControlPlanePhaseOnASingleNode(t *testing.T) {
	c, ctx, run := controlPlaneWorld(t)
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	setEtcd(t, ctx, c, "node-a", 1, true)
	preloadedNodeUpgrade(t, ctx, c, "node-a")
	runningPod(t, ctx, c, "web", "node-a")

	run(phaseResult{Message: "controlplane: node-a drain skipped"})
	started := getNodeUpgrade(t, ctx, c, "node-a")
	if getNode(t, ctx, c, "node-a").Spec.Unschedulable || started.Annotations[controlPlaneProgressAnnotation] != nodeUpdating || started.Annotations[workerProgressAnnotation] != "" {
		t.Fatalf("node-a must stay schedulable and be marked for ControlPlane only: %v", started.Annotations)
	}
	var kept corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-a", Name: "web"}, &kept); err != nil || kept.DeletionTimestamp != nil {
		t.Fatalf("the only node is not drained: %v", err)
	}
	if !slices.Equal(started.Spec.Steps, []string{v1alpha1.StepPreload, v1alpha1.StepK0sUpdate}) {
		t.Fatalf("node-a steps %v", started.Spec.Steps)
	}
	run(phaseResult{Message: "controlplane: updating node-a: K0sUpdate Pending"})
	finished := metav1.NewTime(time.Now().Add(time.Minute))
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepSucceeded, Attempt: 1, FinishedAt: &finished})
	setK0sVersion(t, ctx, c, "node-a", targetK0s)
	run(phaseResult{Message: "controlplane: waiting for node-a to be Ready"})
	renewLease(t, ctx, c, "node-a", finished.Add(time.Second))
	run(phaseResult{Message: "controlplane: node-a uncordoned"})
	if getNode(t, ctx, c, "node-a").Spec.Unschedulable {
		t.Fatal("node-a must be schedulable")
	}
	run(phaseResult{Message: "controlplane: node-a done"})
	run(phaseResult{Done: true})
	setK0sVersion(t, ctx, c, "node-a", "v1.36.2+k0s.0")
	run(phaseResult{Message: "controlplane: waiting for k0s v1.36.3+k0s.0 on node-a"})
}

func TestControlPlanePhaseWaitsForASpareNode(t *testing.T) {
	cases := map[string]func(*corev1.Node){
		"other node cordoned": func(n *corev1.Node) { n.Spec.Unschedulable = true },
		"other node tainted": func(n *corev1.Node) {
			n.Spec.Taints = []corev1.Taint{{Key: "example.com/maintenance", Effect: corev1.TaintEffectNoSchedule}}
		},
	}
	for name, other := range cases {
		t.Run(name, func(t *testing.T) {
			c, ctx, run := controlPlaneWorld(t)
			createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
			setEtcd(t, ctx, c, "node-a", 1, true)
			preloadedNodeUpgrade(t, ctx, c, "node-a")
			node := readyNode("node-b", "amd64")
			other(&node)
			createNodeWithStatus(t, ctx, c, node)

			run(phaseResult{Message: "controlplane: waiting for another schedulable node before draining node-a"})
			run(phaseResult{Message: "controlplane: waiting for another schedulable node before draining node-a"})
			waiting := getNodeUpgrade(t, ctx, c, "node-a")
			if getNode(t, ctx, c, "node-a").Spec.Unschedulable || waiting.Annotations[controlPlaneProgressAnnotation] != "" || !slices.Equal(waiting.Spec.Steps, []string{v1alpha1.StepPreload}) {
				t.Fatalf("node-a must not start: annotations %v steps %v", waiting.Annotations, waiting.Spec.Steps)
			}
			spare := getNode(t, ctx, c, "node-b")
			spare.Spec.Unschedulable, spare.Spec.Taints = false, nil
			if err := c.Update(ctx, &spare); err != nil {
				t.Fatal(err)
			}
			run(phaseResult{Message: "controlplane: node-a cordoned"})
		})
	}
}

func TestControlPlanePhaseKeepsTheDrainDecisionAfterARestart(t *testing.T) {
	cases := map[string]struct {
		progress      string
		cordoned      bool
		appended      bool
		spareLost     bool
		want          string
		unschedulable bool
		annotation    string
	}{
		"cordoned before the progress was written": {"", true, false, false, "controlplane: node-a cordoned", true, nodeDraining},
		"draining when the spare is lost":          {nodeDraining, true, false, true, "controlplane: draining node-a: tenant-a/web", true, nodeDraining},
		"updating after a drain, spare lost":       {nodeUpdating, true, true, true, "controlplane: updating node-a: K0sUpdate Pending", true, nodeUpdating},
		"updating after a skip, a Node joined":     {nodeUpdating, false, true, false, "controlplane: updating node-a: K0sUpdate Pending", false, nodeUpdating},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, ctx, run := controlPlaneWorld(t)
			createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
			setEtcd(t, ctx, c, "node-a", 1, true)
			preloadedNodeUpgrade(t, ctx, c, "node-a")
			createNodeWithStatus(t, ctx, c, readyNode("node-b", "amd64"))
			runningPod(t, ctx, c, "web", "node-a")
			if tc.cordoned {
				if err := setUnschedulable(ctx, c, "node-a", true); err != nil {
					t.Fatal(err)
				}
			}
			if tc.appended {
				appendK0sUpdate(t, ctx, c, "node-a")
			}
			if tc.progress != "" {
				if err := setNodeProgress(ctx, c, v1alpha1.NodeUpgradeName("v2", "node-a"), controlPlaneProgressAnnotation, tc.progress); err != nil {
					t.Fatal(err)
				}
			}
			if tc.spareLost {
				if err := setUnschedulable(ctx, c, "node-b", true); err != nil {
					t.Fatal(err)
				}
			}

			run(phaseResult{Message: tc.want})
			if got := getNode(t, ctx, c, "node-a").Spec.Unschedulable; got != tc.unschedulable {
				t.Fatalf("node-a unschedulable %v, want %v", got, tc.unschedulable)
			}
			if got := getNodeUpgrade(t, ctx, c, "node-a").Annotations[controlPlaneProgressAnnotation]; got != tc.annotation {
				t.Fatalf("progress %q, want %q", got, tc.annotation)
			}
		})
	}
}

func TestControlPlanePhaseAfterARestartWithTheStepAppended(t *testing.T) {
	failed := v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepFailed, Attempt: 1, Message: "k0s v1.36.3+k0s.0 is not ready after 10m0s: k0s does not run"}
	failure := phaseResult{Failure: "controlplane: node-a K0sUpdate failed: k0s v1.36.3+k0s.0 is not ready after 10m0s: k0s does not run"}
	cases := map[string]struct {
		progress   string
		drained    bool
		step       v1alpha1.NodeUpgradeStepStatus
		members    int32
		want       phaseResult
		annotation string
	}{
		"draining, the step failed and etcd is down":      {nodeDraining, true, failed, 0, failure, nodeDraining},
		"drain skipped, the step failed and etcd is down": {"", false, failed, 0, failure, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, ctx, run := controlPlaneWorld(t)
			createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
			setEtcd(t, ctx, c, "node-a", tc.members, tc.members == 1)
			preloadedNodeUpgrade(t, ctx, c, "node-a")
			if tc.drained {
				createNodeWithStatus(t, ctx, c, readyNode("node-b", "amd64"))
				if err := setUnschedulable(ctx, c, "node-a", true); err != nil {
					t.Fatal(err)
				}
			}
			appendK0sUpdate(t, ctx, c, "node-a")
			if tc.step.Name != "" {
				reportStep(t, ctx, c, "node-a", tc.step)
			}
			if tc.progress != "" {
				if err := setNodeProgress(ctx, c, v1alpha1.NodeUpgradeName("v2", "node-a"), controlPlaneProgressAnnotation, tc.progress); err != nil {
					t.Fatal(err)
				}
			}

			run(tc.want)
			upgrade := getNodeUpgrade(t, ctx, c, "node-a")
			if !slices.Equal(upgrade.Spec.Steps, []string{v1alpha1.StepPreload, v1alpha1.StepK0sUpdate}) {
				t.Fatalf("node-a steps %v", upgrade.Spec.Steps)
			}
			if got := upgrade.Annotations[controlPlaneProgressAnnotation]; got != tc.annotation {
				t.Fatalf("progress %q, want %q", got, tc.annotation)
			}
		})
	}
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
	setEtcd(t, ctx, c, "node-a", 2, true)
	setEtcd(t, ctx, c, "node-c", 2, false)
	runningPod(t, ctx, c, "web", "node-a")

	run(phaseResult{Message: "controlplane: waiting before updating node-a: etcd: node-c is not healthy"})
	if getNode(t, ctx, c, "node-a").Spec.Unschedulable || getNodeUpgrade(t, ctx, c, "node-a").Annotations[controlPlaneProgressAnnotation] != "" {
		t.Fatal("the first controller must stay in service while etcd is unhealthy")
	}
	setEtcd(t, ctx, c, "node-c", 2, true)
	run(phaseResult{Message: "controlplane: node-a cordoned"})
	if err := setUnschedulable(ctx, c, "node-b", true); err != nil {
		t.Fatal(err)
	}
	run(phaseResult{Message: "controlplane: draining node-a: tenant-a/web"})
	var evicted corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-a", Name: "web"}, &evicted); err != nil || evicted.DeletionTimestamp == nil {
		t.Fatalf("web must be evicted: %v", err)
	}
	if err := c.Delete(ctx, &evicted, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	setEtcd(t, ctx, c, "node-c", 2, false)
	run(phaseResult{Message: "controlplane: waiting before updating node-a: etcd: node-c is not healthy"})
	if steps := getNodeUpgrade(t, ctx, c, "node-a").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload}) {
		t.Fatalf("a drained controller must not get its steps while a peer is unhealthy: %v", steps)
	}
	setEtcd(t, ctx, c, "node-c", 2, true)
	run(phaseResult{Message: "controlplane: node-a drained"})
	if err := setUnschedulable(ctx, c, "node-b", false); err != nil {
		t.Fatal(err)
	}
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepFailed, Attempt: 1, Message: "staged k0s: checksum mismatch"})
	run(phaseResult{Failure: "controlplane: node-a K0sUpdate failed: staged k0s: checksum mismatch"})
	if err := raiseAttempts(ctx, c, "v2", 2); err != nil {
		t.Fatal(err)
	}
	run(phaseResult{Message: "controlplane: updating node-a: K0sUpdate Pending"})
	reportStep(t, ctx, c, "node-a", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepSucceeded, Attempt: 2})
	setK0sVersion(t, ctx, c, "node-a", targetK0s)
	setEtcd(t, ctx, c, "node-a", 1, false)
	run(phaseResult{Message: "controlplane: node-a uncordoned"})
	run(phaseResult{Message: "controlplane: node-a: etcd: node-a reports 1 of 2 members"})
	if steps := getNodeUpgrade(t, ctx, c, "node-c").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload}) {
		t.Fatalf("node-c must wait for node-a: %v", steps)
	}
	setEtcd(t, ctx, c, "node-a", 2, false)
	run(phaseResult{Message: "controlplane: node-a: etcd: node-a is not healthy"})
	setEtcd(t, ctx, c, "node-a", 2, true)
	run(phaseResult{Message: "controlplane: node-a done"})

	run(phaseResult{Message: "controlplane: node-c drain skipped"})
	if steps := getNodeUpgrade(t, ctx, c, "node-c").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload, v1alpha1.StepK0sUpdate}) {
		t.Fatalf("node-c steps %v", steps)
	}
	finishSteps(t, ctx, c, "node-c", v1alpha1.StepK0sUpdate)
	run(phaseResult{Message: "controlplane: waiting for k0s v1.36.3+k0s.0 on node-c"})
	setK0sVersion(t, ctx, c, "node-c", targetK0s)
	run(phaseResult{Message: "controlplane: node-c updated"})
	run(phaseResult{Message: "controlplane: node-c done"})
	run(phaseResult{Done: true})

	worker := getNodeUpgrade(t, ctx, c, "node-b")
	if !slices.Equal(worker.Spec.Steps, []string{v1alpha1.StepPreload}) || worker.Annotations[controlPlaneProgressAnnotation] != "" || getNode(t, ctx, c, "node-b").Spec.Unschedulable {
		t.Fatalf("ControlPlane must leave the worker alone: steps %v annotations %v", worker.Spec.Steps, worker.Annotations)
	}
}

func setNodeReady(t *testing.T, ctx context.Context, c client.Client, name string, status corev1.ConditionStatus) {
	t.Helper()
	node := getNode(t, ctx, c, name)
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}
	if err := c.Status().Update(ctx, &node); err != nil {
		t.Fatal(err)
	}
}

func TestControlPlanePhaseWaitsForADeadPeerController(t *testing.T) {
	c, ctx, run := controlPlaneWorld(t)
	for _, name := range []string{"node-a", "node-b", "node-c"} {
		createDepotHost(t, ctx, c, name, v1alpha1.RoleControlPlane, v1alpha1.RoleWorkload)
		setEtcd(t, ctx, c, name, 3, true)
		preloadedNodeUpgrade(t, ctx, c, name)
	}
	if err := setNodeProgress(ctx, c, v1alpha1.NodeUpgradeName("v2", "node-a"), controlPlaneProgressAnnotation, nodeDone); err != nil {
		t.Fatal(err)
	}
	setNodeReady(t, ctx, c, "node-c", corev1.ConditionUnknown)
	waiting := "controlplane: waiting before updating node-b: etcd: the Node of controller node-c is not Ready"

	run(phaseResult{Message: waiting})
	if getNode(t, ctx, c, "node-b").Spec.Unschedulable || getNodeUpgrade(t, ctx, c, "node-b").Annotations[controlPlaneProgressAnnotation] != "" {
		t.Fatal("the next controller must not start while a peer Node is not Ready")
	}
	setNodeReady(t, ctx, c, "node-c", corev1.ConditionTrue)
	run(phaseResult{Message: "controlplane: node-b cordoned"})
	setNodeReady(t, ctx, c, "node-c", corev1.ConditionFalse)
	run(phaseResult{Message: waiting})
	if steps := getNodeUpgrade(t, ctx, c, "node-b").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload}) {
		t.Fatalf("a drained controller must not get its steps while a peer Node is not Ready: %v", steps)
	}
	setNodeReady(t, ctx, c, "node-c", corev1.ConditionTrue)
	run(phaseResult{Message: "controlplane: node-b drained"})
	if steps := getNodeUpgrade(t, ctx, c, "node-b").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload, v1alpha1.StepK0sUpdate}) {
		t.Fatalf("node-b steps %v", steps)
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
