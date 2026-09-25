package operator

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

func TestActiveNodes(t *testing.T) {
	order := []string{"cp-a", "cp-b", "w-a", "w-b", "w-c"}
	controllers := []string{"cp-a", "cp-b"}
	cases := map[string]struct {
		progress    map[string]string
		concurrency int
		want        []string
	}{
		"first controller":                          {map[string]string{}, 2, []string{"cp-a"}},
		"controller in progress":                    {map[string]string{"cp-a": workerDone, "cp-b": workerUpdating}, 2, []string{"cp-b"}},
		"workers in a window":                       {map[string]string{"cp-a": workerDone, "cp-b": workerDone}, 2, []string{"w-a", "w-b"}},
		"started nodes stay":                        {map[string]string{"cp-a": workerDone, "cp-b": workerDone, "w-c": workerDraining}, 2, []string{"w-c", "w-a"}},
		"one finished frees a place":                {map[string]string{"cp-a": workerDone, "cp-b": workerDone, "w-a": workerDone, "w-b": workerUpdating}, 2, []string{"w-b", "w-c"}},
		"everything done":                           {map[string]string{"cp-a": workerDone, "cp-b": workerDone, "w-a": workerDone, "w-b": workerDone, "w-c": workerDone}, 2, nil},
		"concurrency below one":                     {map[string]string{"cp-a": workerDone, "cp-b": workerDone}, 0, []string{"w-a"}},
		"late controller waits for started workers": {map[string]string{"cp-a": workerDone, "w-a": workerUpdating}, 1, []string{"w-a"}},
		"late controller after the window empties":  {map[string]string{"cp-a": workerDone, "w-a": workerDone}, 1, []string{"cp-b"}},
		"started nodes stay when concurrency drops": {map[string]string{"cp-a": workerDone, "cp-b": workerDone, "w-a": workerUpdating, "w-b": workerDraining}, 1, []string{"w-a", "w-b"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := activeNodes(order, controllers, tc.progress, tc.concurrency); !slices.Equal(got, tc.want) {
				t.Fatalf("active %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorkerOrderAndSteps(t *testing.T) {
	hosts := []v1alpha1.Host{hostWithRoles("w-b", v1alpha1.RoleWorkload), hostWithRoles("cp-b", v1alpha1.RoleControlPlane), hostWithRoles("w-a", v1alpha1.RoleCephOSD), hostWithRoles("cp-a", v1alpha1.RoleControlPlane, v1alpha1.RoleCephOSD)}
	if got := workerOrder(hosts); !slices.Equal(got, []string{"cp-a", "cp-b", "w-a", "w-b"}) {
		t.Fatalf("order %v", got)
	}
	if got := updateSteps(hostWithRoles("w-a")); !slices.Equal(got, []string{v1alpha1.StepPreload, v1alpha1.StepAgentUpdate, v1alpha1.StepReboot}) {
		t.Fatalf("steps %v", got)
	}
	windowed := hostWithRoles("w-a")
	windowed.Spec.MaintenanceWindow = "Sun 02:00-04:00"
	if got := updateSteps(windowed); !slices.Equal(got, []string{v1alpha1.StepPreload, v1alpha1.StepAgentUpdate, v1alpha1.StepOSUpdate, v1alpha1.StepReboot}) {
		t.Fatalf("steps %v", got)
	}
}

func TestEtcdProblem(t *testing.T) {
	member := func(name string, members int32) v1alpha1.Host {
		host := hostWithRoles(name, v1alpha1.RoleControlPlane)
		host.Status.Checks = &v1alpha1.HostChecks{EtcdMembers: members}
		return host
	}
	worker := hostWithRoles("w-a", v1alpha1.RoleWorkload)
	if got := etcdProblem([]v1alpha1.Host{member("cp-a", 2), member("cp-b", 2), worker}); got != "" {
		t.Fatalf("healthy etcd: %q", got)
	}
	if got := etcdProblem([]v1alpha1.Host{member("cp-a", 2), member("cp-b", 1), worker}); got != "etcd: cp-b reports 1 of 2 members" {
		t.Fatalf("problem %q", got)
	}
	if got := etcdProblem([]v1alpha1.Host{hostWithRoles("cp-a", v1alpha1.RoleControlPlane)}); got != "etcd: cp-a reports 0 of 1 members" {
		t.Fatalf("a controller without checks: %q", got)
	}
}

func stepsIn(states ...string) []v1alpha1.NodeUpgradeStepStatus {
	names := []string{v1alpha1.StepPreload, v1alpha1.StepAgentUpdate, v1alpha1.StepReboot}
	steps := make([]v1alpha1.NodeUpgradeStepStatus, 0, len(states))
	for i, state := range states {
		steps = append(steps, v1alpha1.NodeUpgradeStepStatus{Name: names[i], State: state, Message: "exit status 1"})
	}
	return steps
}

func TestNextWorkerMove(t *testing.T) {
	base := workerFacts{Name: "w-a", HasNode: true, NodeReady: true, K0sCurrent: true, TargetK0s: targetK0s}
	with := func(change func(workerFacts) workerFacts) workerFacts {
		return change(base)
	}
	done := stepsIn(v1alpha1.StepSucceeded, v1alpha1.StepSucceeded, v1alpha1.StepSucceeded)
	cases := map[string]struct {
		facts workerFacts
		want  workerMove
	}{
		"start": {base, workerMove{Cordon: true, Progress: workerDraining, Message: "w-a cordoned"}},
		"start without a node": {with(func(f workerFacts) workerFacts {
			f.HasNode = false
			return f
		}), workerMove{Progress: workerDraining, Message: "w-a cordoned"}},
		"pods left": {with(func(f workerFacts) workerFacts {
			f.Progress, f.PodsLeft = workerDraining, []string{"tenant-a/web", "tenant-a/db"}
			return f
		}), workerMove{Message: "draining w-a: tenant-a/web, tenant-a/db"}},
		"drained": {with(func(f workerFacts) workerFacts {
			f.Progress = workerDraining
			return f
		}), workerMove{Append: true, Progress: workerUpdating, Message: "w-a drained"}},
		"step failed": {with(func(f workerFacts) workerFacts {
			f.Progress, f.Steps = workerUpdating, stepsIn(v1alpha1.StepSucceeded, v1alpha1.StepSucceeded, v1alpha1.StepFailed)
			return f
		}), workerMove{Failure: "w-a Reboot failed: exit status 1"}},
		"steps running": {with(func(f workerFacts) workerFacts {
			f.Progress, f.Steps = workerUpdating, stepsIn(v1alpha1.StepSucceeded, v1alpha1.StepRunning, v1alpha1.StepPending)
			return f
		}), workerMove{Message: "updating w-a: Preload Succeeded, AgentUpdate Running, Reboot Pending"}},
		"node not ready": {with(func(f workerFacts) workerFacts {
			f.Progress, f.Steps, f.NodeReady = workerUpdating, done, false
			return f
		}), workerMove{Message: "waiting for w-a to be Ready"}},
		"old k0s": {with(func(f workerFacts) workerFacts {
			f.Progress, f.Steps, f.K0sCurrent = workerUpdating, done, false
			return f
		}), workerMove{Message: "waiting for k0s v1.36.3+k0s.0 on w-a"}},
		"updated": {with(func(f workerFacts) workerFacts {
			f.Progress, f.Steps = workerUpdating, done
			return f
		}), workerMove{Uncordon: true, Progress: workerUncordoned, Message: "w-a uncordoned"}},
		"updated without a node": {with(func(f workerFacts) workerFacts {
			f.Progress, f.Steps, f.HasNode, f.NodeReady = workerUpdating, done, false, false
			return f
		}), workerMove{Progress: workerUncordoned, Message: "w-a uncordoned"}},
		"ceph recovering": {with(func(f workerFacts) workerFacts {
			f.Progress, f.Ceph = workerUncordoned, "ceph: 3 of 33 PGs are not active+clean"
			return f
		}), workerMove{Message: "w-a: ceph: 3 of 33 PGs are not active+clean"}},
		"controller waits for etcd": {with(func(f workerFacts) workerFacts {
			f.Progress, f.Controller, f.Etcd = workerUncordoned, true, "etcd: cp-b reports 2 of 3 members"
			return f
		}), workerMove{Message: "w-a: etcd: cp-b reports 2 of 3 members"}},
		"worker ignores etcd": {with(func(f workerFacts) workerFacts {
			f.Progress, f.Etcd = workerUncordoned, "etcd: cp-b reports 2 of 3 members"
			return f
		}), workerMove{Progress: workerDone, Message: "w-a done"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := nextWorkerMove(tc.facts); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("move %+v, want %+v", got, tc.want)
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

func setEtcdMembers(t *testing.T, ctx context.Context, c client.Client, name string, members int32) {
	t.Helper()
	var host v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &host); err != nil {
		t.Fatal(err)
	}
	host.Status.Checks.EtcdMembers = members
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

	run(phaseResult{Message: "workers: creating autopilot plan v2-workers-1"})
	setPlanState(t, ctx, c, "Completed")
	run(phaseResult{Message: "workers: node-a cordoned"})
	if !getNode(t, ctx, c, "node-a").Spec.Unschedulable || getNodeUpgrade(t, ctx, c, "node-a").Annotations[workerProgressAnnotation] != workerDraining {
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
	setEtcdMembers(t, ctx, c, "node-a", 1)
	run(phaseResult{Message: "workers: node-a done"})

	run(phaseResult{Message: "workers: node-b cordoned"})
	run(phaseResult{Message: "workers: node-b drained"})
	if steps := getNodeUpgrade(t, ctx, c, "node-b").Spec.Steps; !slices.Equal(steps, []string{v1alpha1.StepPreload, v1alpha1.StepAgentUpdate, v1alpha1.StepOSUpdate, v1alpha1.StepReboot}) {
		t.Fatalf("node-b steps %v", steps)
	}
	finishSteps(t, ctx, c, "node-b", v1alpha1.StepAgentUpdate, v1alpha1.StepOSUpdate)
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
	for _, want := range []string{"workers: node-a cordoned", "workers: node-a drained"} {
		got, err := workers(ctx, env, getCluster(t, ctx, c))
		if err != nil || got != (phaseResult{Message: want}) {
			t.Fatalf("result %+v err %v, want %q", got, err, want)
		}
	}
	var kept corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: "tenant-a", Name: "web"}, &kept); err != nil || kept.DeletionTimestamp != nil {
		t.Fatalf("the only node is not drained: %v", err)
	}
}

func TestWorkersPhaseTable(t *testing.T) {
	if newPhases()[v1alpha1.PhaseWorkers] == nil || oldPhases()[v1alpha1.PhaseWorkers] != nil {
		t.Fatal("only the new operator runs Workers")
	}
}
