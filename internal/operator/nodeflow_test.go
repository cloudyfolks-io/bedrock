package operator

import (
	"reflect"
	"slices"
	"testing"

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

func TestNextNodeMove(t *testing.T) {
	base := nodeFacts{Name: "w-a", HasNode: true, NodeReady: true, K0sCurrent: true, TargetK0s: targetK0s}
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
			f.HasNode = false
			return f
		}), nodeMove{Progress: nodeDraining, Message: "w-a cordoned"}},
		"pods left": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.PodsLeft = nodeDraining, []string{"tenant-a/web", "tenant-a/db"}
			return f
		}), nodeMove{Message: "draining w-a: tenant-a/web, tenant-a/db"}},
		"drained": {with(func(f nodeFacts) nodeFacts {
			f.Progress = nodeDraining
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
			f.Progress, f.Steps, f.HasNode, f.NodeReady = nodeUpdating, done, false, false
			return f
		}), nodeMove{Progress: nodeUncordoned, Message: "w-a uncordoned"}},
		"ceph recovering": {with(func(f nodeFacts) nodeFacts {
			f.Progress, f.Ceph = nodeUncordoned, "ceph: 3 of 33 PGs are not active+clean"
			return f
		}), nodeMove{Message: "w-a: ceph: 3 of 33 PGs are not active+clean"}},
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
