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

func TestWorkerOrderAndSteps(t *testing.T) {
	hosts := []v1alpha1.Host{hostWithRoles("w-b", v1alpha1.RoleWorkload), hostWithRoles("cp-b", v1alpha1.RoleControlPlane), hostWithRoles("w-a", v1alpha1.RoleCephOSD), hostWithRoles("cp-a", v1alpha1.RoleControlPlane, v1alpha1.RoleCephOSD)}
	if got := workerOrder(hosts); !slices.Equal(got, []string{"cp-a", "cp-b", "w-a", "w-b"}) {
		t.Fatalf("order %v", got)
	}
	windowed := func(host v1alpha1.Host) v1alpha1.Host {
		host.Spec.MaintenanceWindow = "Sun 02:00-04:00"
		return host
	}
	cases := map[string]struct {
		host v1alpha1.Host
		want []string
	}{
		"worker":                 {hostWithRoles("w-a", v1alpha1.RoleWorkload), []string{v1alpha1.StepPreload, v1alpha1.StepK0sUpdate, v1alpha1.StepAgentUpdate, v1alpha1.StepReboot}},
		"worker in a window":     {windowed(hostWithRoles("w-a", v1alpha1.RoleCephOSD)), []string{v1alpha1.StepPreload, v1alpha1.StepK0sUpdate, v1alpha1.StepAgentUpdate, v1alpha1.StepOSUpdate, v1alpha1.StepReboot}},
		"controller":             {hostWithRoles("cp-a", v1alpha1.RoleControlPlane, v1alpha1.RoleWorkload), []string{v1alpha1.StepPreload, v1alpha1.StepAgentUpdate, v1alpha1.StepReboot}},
		"controller in a window": {windowed(hostWithRoles("cp-a", v1alpha1.RoleControlPlane)), []string{v1alpha1.StepPreload, v1alpha1.StepAgentUpdate, v1alpha1.StepOSUpdate, v1alpha1.StepReboot}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := updateSteps(tc.host); !slices.Equal(got, tc.want) {
				t.Fatalf("steps %v, want %v", got, tc.want)
			}
		})
	}
}

func preloadedNodeUpgrade(t *testing.T, ctx context.Context, c client.Client, node string) {
	t.Helper()
	upgrade := &v1alpha1.NodeUpgrade{ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.NodeUpgradeName("v2", node)}, Spec: v1alpha1.NodeUpgradeSpec{Node: node, Version: "v2", From: "v1", Depot: "http://10.0.0.11:9480/v2/amd64", Attempt: 1, Steps: []string{v1alpha1.StepPreload}}}
	if err := c.Create(ctx, upgrade); err != nil {
		t.Fatal(err)
	}
	reportStep(t, ctx, c, node, v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepPreload, State: v1alpha1.StepSucceeded, Attempt: 1})
}

func setEtcd(t *testing.T, ctx context.Context, c client.Client, name string, members int32, healthy bool) {
	t.Helper()
	var host v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &host); err != nil {
		t.Fatal(err)
	}
	host.Status.Checks.EtcdMembers = members
	host.Status.Checks.EtcdHealthy = healthy
	if err := c.Status().Update(ctx, &host); err != nil {
		t.Fatal(err)
	}
}

func finishSteps(t *testing.T, ctx context.Context, c client.Client, node string, steps ...string) {
	t.Helper()
	for _, step := range steps {
		reportStep(t, ctx, c, node, v1alpha1.NodeUpgradeStepStatus{Name: step, State: v1alpha1.StepSucceeded, Attempt: 1})
	}
}

func TestWorkersPhase(t *testing.T) {
	c, _ := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-a"}}); err != nil {
		t.Fatal(err)
	}
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	createDepotHost(t, ctx, c, "node-b", v1alpha1.RoleWorkload)
	var nodeB v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: "node-b"}, &nodeB); err != nil {
		t.Fatal(err)
	}
	nodeB.Spec.MaintenanceWindow = "Sun 02:00-04:00"
	if err := c.Update(ctx, &nodeB); err != nil {
		t.Fatal(err)
	}
	preloadedNodeUpgrade(t, ctx, c, "node-a")
	preloadedNodeUpgrade(t, ctx, c, "node-b")
	target := targetRelease()
	if err := c.Create(ctx, &target); err != nil {
		t.Fatal(err)
	}
	web := podOn("web", "node-a")
	web.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	createPod(t, ctx, c, web)
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseWorkers))
	env := upgradeEnv{Client: c}
	run := func(want phaseResult) {
		t.Helper()
		got, err := workers(ctx, env, getCluster(t, ctx, c))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("result %+v, want %+v", got, want)
		}
	}

	run(phaseResult{Message: "workers: node-a cordoned"})
	if !getNode(t, ctx, c, "node-a").Spec.Unschedulable || getNodeUpgrade(t, ctx, c, "node-a").Annotations[workerProgressAnnotation] != nodeDraining {
		t.Fatal("node-a must be cordoned and marked draining")
	}
	run(phaseResult{Message: "workers: draining node-a: tenant-a/web"})
	var evicted corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-a", Name: "web"}, &evicted); err != nil || evicted.DeletionTimestamp == nil {
		t.Fatalf("web must be evicted: %v", err)
	}
	if err := c.Delete(ctx, &evicted, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	run(phaseResult{Message: "workers: node-a drained"})
	if steps := getNodeUpgrade(t, ctx, c, "node-a").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload, v1alpha1.StepAgentUpdate, v1alpha1.StepReboot}) {
		t.Fatalf("node-a steps %v", steps)
	}
	finishSteps(t, ctx, c, "node-a", v1alpha1.StepAgentUpdate)
	run(phaseResult{Message: "workers: updating node-a: Preload Succeeded, AgentUpdate Succeeded, Reboot Pending"})
	finishSteps(t, ctx, c, "node-a", v1alpha1.StepReboot)
	run(phaseResult{Message: "workers: waiting for k0s v1.36.3+k0s.0 on node-a"})
	setK0sVersion(t, ctx, c, "node-a", targetK0s)
	run(phaseResult{Message: "workers: node-a uncordoned"})
	if getNode(t, ctx, c, "node-a").Spec.Unschedulable {
		t.Fatal("node-a must be uncordoned")
	}
	run(phaseResult{Message: "workers: node-a: etcd: node-a reports 0 of 1 members"})
	setEtcd(t, ctx, c, "node-a", 1, false)
	run(phaseResult{Message: "workers: node-a: etcd: node-a is not healthy"})
	setEtcd(t, ctx, c, "node-a", 1, true)
	run(phaseResult{Message: "workers: node-a done"})

	run(phaseResult{Message: "workers: node-b cordoned"})
	run(phaseResult{Message: "workers: node-b drained"})
	if steps := getNodeUpgrade(t, ctx, c, "node-b").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload, v1alpha1.StepK0sUpdate, v1alpha1.StepAgentUpdate, v1alpha1.StepOSUpdate, v1alpha1.StepReboot}) {
		t.Fatalf("node-b steps %v", steps)
	}
	reportStep(t, ctx, c, "node-b", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepK0sUpdate, State: v1alpha1.StepRunning, Attempt: 1})
	run(phaseResult{Message: "workers: updating node-b: Preload Succeeded, K0sUpdate Running, AgentUpdate Pending, OSUpdate Pending, Reboot Pending"})
	finishSteps(t, ctx, c, "node-b", v1alpha1.StepK0sUpdate, v1alpha1.StepAgentUpdate, v1alpha1.StepOSUpdate)
	reportStep(t, ctx, c, "node-b", v1alpha1.NodeUpgradeStepStatus{Name: v1alpha1.StepReboot, State: v1alpha1.StepFailed, Attempt: 1, Message: "systemctl reboot: exit status 1"})
	run(phaseResult{Failure: "workers: node-b Reboot failed: systemctl reboot: exit status 1"})
	finishSteps(t, ctx, c, "node-b", v1alpha1.StepReboot)
	setK0sVersion(t, ctx, c, "node-b", targetK0s)
	run(phaseResult{Message: "workers: node-b uncordoned"})
	run(phaseResult{Message: "workers: node-b done"})
	run(phaseResult{Done: true})
}

func TestWorkersPhaseOnASingleNode(t *testing.T) {
	c, _ := startTestEnvWithCRDs(t, filepath.Join("testdata", "crds"))
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-a"}}); err != nil {
		t.Fatal(err)
	}
	createDepotHost(t, ctx, c, "node-a", v1alpha1.RoleControlPlane)
	preloadedNodeUpgrade(t, ctx, c, "node-a")
	target := targetRelease()
	if err := c.Create(ctx, &target); err != nil {
		t.Fatal(err)
	}
	web := podOn("web", "node-a")
	web.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	createPod(t, ctx, c, web)
	createClusterWithStatus(t, ctx, c, "v2", upgradeStatusIn(v1alpha1.PhaseWorkers))
	env := upgradeEnv{Client: c}
	for _, want := range []string{"workers: node-a started", "workers: node-a drain skipped"} {
		got, err := workers(ctx, env, getCluster(t, ctx, c))
		if err != nil || got != (phaseResult{Message: want}) {
			t.Fatalf("result %+v err %v, want %q", got, err, want)
		}
	}
	var kept corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-a", Name: "web"}, &kept); err != nil || kept.DeletionTimestamp != nil {
		t.Fatalf("the only node is not drained: %v", err)
	}
	if getNode(t, ctx, c, "node-a").Spec.Unschedulable {
		t.Fatal("the only node is not cordoned")
	}
}

func TestWorkersPhaseTable(t *testing.T) {
	if newPhases()[v1alpha1.PhaseWorkers] == nil || oldPhases()[v1alpha1.PhaseWorkers] != nil {
		t.Fatal("only the new operator runs Workers")
	}
}
