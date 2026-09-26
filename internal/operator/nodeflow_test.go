package operator

import (
	"reflect"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
		"controller in progress":                    {map[string]string{"cp-a": nodeDone, "cp-b": nodeUpdating}, 2, []string{"cp-b"}},
		"workers in a window":                       {map[string]string{"cp-a": nodeDone, "cp-b": nodeDone}, 2, []string{"w-a", "w-b"}},
		"started nodes stay":                        {map[string]string{"cp-a": nodeDone, "cp-b": nodeDone, "w-c": nodeDraining}, 2, []string{"w-c", "w-a"}},
		"one finished frees a place":                {map[string]string{"cp-a": nodeDone, "cp-b": nodeDone, "w-a": nodeDone, "w-b": nodeUpdating}, 2, []string{"w-b", "w-c"}},
		"everything done":                           {map[string]string{"cp-a": nodeDone, "cp-b": nodeDone, "w-a": nodeDone, "w-b": nodeDone, "w-c": nodeDone}, 2, nil},
		"concurrency below one":                     {map[string]string{"cp-a": nodeDone, "cp-b": nodeDone}, 0, []string{"w-a"}},
		"late controller waits for started workers": {map[string]string{"cp-a": nodeDone, "w-a": nodeUpdating}, 1, []string{"w-a"}},
		"late controller after the window empties":  {map[string]string{"cp-a": nodeDone, "w-a": nodeDone}, 1, []string{"cp-b"}},
		"started nodes stay when concurrency drops": {map[string]string{"cp-a": nodeDone, "cp-b": nodeDone, "w-a": nodeUpdating, "w-b": nodeDraining}, 1, []string{"w-a", "w-b"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := activeNodes(order, controllers, tc.progress, tc.concurrency); !slices.Equal(got, tc.want) {
				t.Fatalf("active %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEtcdProblem(t *testing.T) {
	member := func(name string, members int32, healthy bool) v1alpha1.Host {
		host := hostWithRoles(name, v1alpha1.RoleControlPlane)
		host.Status.Checks = &v1alpha1.HostChecks{EtcdMembers: members, EtcdHealthy: healthy}
		return host
	}
	worker := hostWithRoles("w-a", v1alpha1.RoleWorkload)
	cases := map[string]struct {
		hosts []v1alpha1.Host
		want  string
	}{
		"healthy":          {[]v1alpha1.Host{member("cp-a", 2, true), member("cp-b", 2, true), worker}, ""},
		"missing member":   {[]v1alpha1.Host{member("cp-a", 2, true), member("cp-b", 1, true), worker}, "etcd: cp-b reports 1 of 2 members"},
		"unhealthy member": {[]v1alpha1.Host{member("cp-a", 2, true), member("cp-b", 2, false), worker}, "etcd: cp-b is not healthy"},
		"no checks":        {[]v1alpha1.Host{hostWithRoles("cp-a", v1alpha1.RoleControlPlane)}, "etcd: cp-a reports 0 of 1 members"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := etcdProblem(tc.hosts); got != tc.want {
				t.Fatalf("problem %q, want %q", got, tc.want)
			}
		})
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

func TestSpareNode(t *testing.T) {
	node := func(name string, change func(*corev1.Node)) corev1.Node {
		ready := readyNode(name, "amd64")
		change(&ready)
		return ready
	}
	keep := func(*corev1.Node) {}
	cordon := func(n *corev1.Node) { n.Spec.Unschedulable = true }
	taint := func(effect corev1.TaintEffect) func(*corev1.Node) {
		return func(n *corev1.Node) { n.Spec.Taints = []corev1.Taint{{Key: "example.com/busy", Effect: effect}} }
	}
	cases := map[string]struct {
		nodes []corev1.Node
		want  bool
	}{
		"only node":                  {[]corev1.Node{node("node-a", keep)}, false},
		"another schedulable node":   {[]corev1.Node{node("node-a", keep), node("node-b", keep)}, true},
		"the other node is cordoned": {[]corev1.Node{node("node-a", keep), node("node-b", cordon)}, false},
		"NoSchedule taint":           {[]corev1.Node{node("node-a", keep), node("node-b", taint(corev1.TaintEffectNoSchedule))}, false},
		"NoExecute taint":            {[]corev1.Node{node("node-a", keep), node("node-b", taint(corev1.TaintEffectNoExecute))}, false},
		"PreferNoSchedule taint":     {[]corev1.Node{node("node-a", keep), node("node-b", taint(corev1.TaintEffectPreferNoSchedule))}, true},
		"this node cordoned already": {[]corev1.Node{node("node-a", cordon), node("node-b", keep)}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := spareNode(tc.nodes, "node-a"); got != tc.want {
				t.Fatalf("spare node %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReadySince(t *testing.T) {
	finished := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	node := func(status corev1.ConditionStatus, heartbeat time.Time) corev1.Node {
		ready := readyNode("node-a", "amd64")
		ready.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status, LastHeartbeatTime: metav1.NewTime(heartbeat)}}
		return ready
	}
	cases := map[string]struct {
		node corev1.Node
		want bool
	}{
		"heartbeat after the step":  {node(corev1.ConditionTrue, finished.Add(time.Second)), true},
		"heartbeat before the step": {node(corev1.ConditionTrue, finished.Add(-time.Second)), false},
		"heartbeat at the step":     {node(corev1.ConditionTrue, finished), false},
		"not ready":                 {node(corev1.ConditionFalse, finished.Add(time.Second)), false},
		"no ready condition":        {corev1.Node{}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := readySince(tc.node, finished); got != tc.want {
				t.Fatalf("ready %v, want %v", got, tc.want)
			}
		})
	}
	steps := []v1alpha1.NodeUpgradeStepStatus{
		{Name: v1alpha1.StepPreload, FinishedAt: &metav1.Time{Time: finished.Add(-time.Hour)}},
		{Name: v1alpha1.StepK0sUpdate, FinishedAt: &metav1.Time{Time: finished}},
		{Name: v1alpha1.StepReboot},
	}
	if got := lastFinished(steps); !got.Equal(finished) {
		t.Fatalf("last finished %v, want %v", got, finished)
	}
}

func TestNextNodeMove(t *testing.T) {
	base := nodeFacts{Name: "w-a", HasNode: true, Spare: true, NodeReady: true, K0sCurrent: true, TargetK0s: targetK0s}
	with := func(change func(nodeFacts) nodeFacts) nodeFacts {
		return change(base)
	}
	done := stepsIn(v1alpha1.StepSucceeded, v1alpha1.StepSucceeded, v1alpha1.StepSucceeded)
	cases := map[string]struct {
		facts nodeFacts
		want  nodeMove
	}{
		"start": {base, nodeMove{Cordon: true, Progress: nodeDraining, Message: "w-a cordoned"}},
		"start without a node": {with(func(f nodeFacts) nodeFacts {
			f.HasNode, f.Spare = false, false
			return f
		}), nodeMove{Append: true, Progress: nodeUpdating, Message: "w-a drain skipped"}},
		"start on a one-node cluster": {with(func(f nodeFacts) nodeFacts {
			f.SingleNode, f.Spare = true, false
			return f
		}), nodeMove{Append: true, Progress: nodeUpdating, Message: "w-a drain skipped"}},
		"start without a spare node": {with(func(f nodeFacts) nodeFacts {
			f.Spare = false
			return f
		}), nodeMove{Message: "waiting for another schedulable node before draining w-a"}},
		"pods left": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.PodsLeft = nodeDraining, []string{"tenant-a/web", "tenant-a/db"}
			return f
		}), nodeMove{Message: "draining w-a: tenant-a/web, tenant-a/db"}},
		"drained": {with(func(f nodeFacts) nodeFacts {
			f.Progress = nodeDraining
			return f
		}), nodeMove{Append: true, Progress: nodeUpdating, Message: "w-a drained"}},
		"a started drain continues without a spare node": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Spare, f.PodsLeft = nodeDraining, false, []string{"tenant-a/web"}
			return f
		}), nodeMove{Message: "draining w-a: tenant-a/web"}},
		"a started drain finishes without a spare node": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Spare = nodeDraining, false
			return f
		}), nodeMove{Append: true, Progress: nodeUpdating, Message: "w-a drained"}},
		"controller waits for etcd before its steps": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Controller, f.Etcd = nodeDraining, true, "etcd: cp-b is not healthy"
			return f
		}), nodeMove{Message: "waiting before updating w-a: etcd: cp-b is not healthy"}},
		"controller on a one-node cluster waits for etcd": {with(func(f nodeFacts) nodeFacts {
			f.SingleNode, f.Controller, f.Etcd = true, true, "etcd: w-a is not healthy"
			return f
		}), nodeMove{Message: "waiting before updating w-a: etcd: w-a is not healthy"}},
		"worker gets its steps while etcd is unhealthy": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Etcd = nodeDraining, "etcd: cp-b is not healthy"
			return f
		}), nodeMove{Append: true, Progress: nodeUpdating, Message: "w-a drained"}},
		"step failed": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Steps = nodeUpdating, stepsIn(v1alpha1.StepSucceeded, v1alpha1.StepSucceeded, v1alpha1.StepFailed)
			return f
		}), nodeMove{Failure: "w-a Reboot failed: exit status 1"}},
		"steps running": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Steps = nodeUpdating, stepsIn(v1alpha1.StepSucceeded, v1alpha1.StepRunning, v1alpha1.StepPending)
			return f
		}), nodeMove{Message: "updating w-a: Preload Succeeded, AgentUpdate Running, Reboot Pending"}},
		"node not ready": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Steps, f.NodeReady = nodeUpdating, done, false
			return f
		}), nodeMove{Message: "waiting for w-a to be Ready"}},
		"old k0s": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Steps, f.K0sCurrent = nodeUpdating, done, false
			return f
		}), nodeMove{Message: "waiting for k0s v1.36.3+k0s.0 on w-a"}},
		"updated": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Steps = nodeUpdating, done
			return f
		}), nodeMove{Uncordon: true, Progress: nodeUncordoned, Message: "w-a uncordoned"}},
		"updated without a node": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Steps, f.HasNode, f.Spare, f.NodeReady = nodeUpdating, done, false, false, false
			return f
		}), nodeMove{Progress: nodeUncordoned, Message: "w-a updated"}},
		"ceph recovering": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Ceph = nodeUncordoned, "ceph: 3 of 33 PGs are not active+clean"
			return f
		}), nodeMove{Message: "w-a: ceph: 3 of 33 PGs are not active+clean"}},
		"ceph ignored without a node": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.HasNode, f.Spare, f.Ceph = nodeUncordoned, false, false, "ceph: 3 of 33 PGs are not active+clean"
			return f
		}), nodeMove{Progress: nodeDone, Message: "w-a done"}},
		"controller waits for etcd": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Controller, f.Etcd = nodeUncordoned, true, "etcd: cp-b reports 2 of 3 members"
			return f
		}), nodeMove{Message: "w-a: etcd: cp-b reports 2 of 3 members"}},
		"worker ignores etcd": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Etcd = nodeUncordoned, "etcd: cp-b reports 2 of 3 members"
			return f
		}), nodeMove{Progress: nodeDone, Message: "w-a done"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := nextNodeMove(tc.facts); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("move %+v, want %+v", got, tc.want)
			}
		})
	}
}
