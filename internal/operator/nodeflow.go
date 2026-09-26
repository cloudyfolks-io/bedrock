package operator

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const (
	nodeDraining   = "draining"
	nodeUpdating   = "updating"
	nodeUncordoned = "uncordoned"
	nodeDone       = "done"
)

type nodeFlow struct {
	Label      string
	Annotation string
	Members    func([]v1alpha1.Host) []v1alpha1.Host
	Steps      func(v1alpha1.Host) []string
}

type nodeFacts struct {
	Name       string
	Progress   string
	Controller bool
	HasNode    bool
	SingleNode bool
	Spare      bool
	NodeReady  bool
	PodsLeft   []string
	Steps      []v1alpha1.NodeUpgradeStepStatus
	K0sCurrent bool
	TargetK0s  string
	Ceph       string
	Etcd       string
}

type nodeMove struct {
	Cordon   bool
	Uncordon bool
	Append   bool
	Progress string
	Message  string
	Failure  string
}

type flowInput struct {
	Flow     nodeFlow
	Upgrade  v1alpha1.UpgradeStatus
	Target   v1alpha1.Release
	Hosts    []v1alpha1.Host
	Nodes    []corev1.Node
	Leases   []coordinationv1.Lease
	Upgrades []v1alpha1.NodeUpgrade
	Progress map[string]string
	Ceph     string
}

func walkNodes(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster, flow nodeFlow) (phaseResult, error) {
	upgrade := *cluster.Status.Upgrade
	target, err := optionalRelease(ctx, env.Client, upgrade.To)
	if err != nil {
		return phaseResult{}, err
	}
	if target == nil {
		return phaseResult{Failure: fmt.Sprintf("%s: Release/%s does not exist", flow.Label, upgrade.To)}, nil
	}
	hosts, nodes, err := hostsAndNodes(ctx, env.Client)
	if err != nil {
		return phaseResult{}, err
	}
	members := flow.Members(hosts)
	for _, host := range members {
		want, problem := nodeUpgradeFor(upgrade, host, hosts, nodes, []string{v1alpha1.StepPreload})
		if problem != "" {
			return phaseResult{Message: flow.Label + ": " + problem}, nil
		}
		if err := applyNodeUpgrade(ctx, env.Client, want); err != nil {
			return phaseResult{}, err
		}
	}
	upgrades, err := listNodeUpgrades(ctx, env.Client, upgrade.To)
	if err != nil {
		return phaseResult{}, err
	}
	progress := flowProgress(upgrades, flow.Annotation)
	active := activeNodes(workerOrder(members), hostNames(hostsWithRole(members, v1alpha1.RoleControlPlane)), progress, int(cluster.Spec.NodeConcurrency))
	if len(active) == 0 {
		return k0sReached(flow.Label, members, target.Spec.K0sVersion), nil
	}
	leases, err := nodeLeases(ctx, env.Client)
	if err != nil {
		return phaseResult{}, err
	}
	in := flowInput{Flow: flow, Upgrade: upgrade, Target: *target, Hosts: hosts, Nodes: nodes, Leases: leases, Upgrades: upgrades, Progress: progress, Ceph: uncordonedCeph(ctx, env, active, progress)}
	messages := make([]string, 0, len(active))
	for _, name := range active {
		move, err := advanceNode(ctx, env.Client, in, name)
		if err != nil {
			return phaseResult{}, err
		}
		if move.Failure != "" {
			return phaseResult{Failure: flow.Label + ": " + move.Failure}, nil
		}
		messages = append(messages, move.Message)
		in = afterMove(in, name, move)
	}
	return phaseResult{Message: flow.Label + ": " + strings.Join(messages, "; ")}, nil
}

func afterMove(in flowInput, name string, move nodeMove) flowInput {
	if !move.Cordon {
		return in
	}
	next := in
	next.Nodes = make([]corev1.Node, 0, len(in.Nodes))
	for _, node := range in.Nodes {
		copied := *node.DeepCopy()
		copied.Spec.Unschedulable = copied.Spec.Unschedulable || copied.Name == name
		next.Nodes = append(next.Nodes, copied)
	}
	return next
}

func k0sReached(label string, hosts []v1alpha1.Host, version string) phaseResult {
	if waiting := hostsNotAt(hosts, version); len(waiting) > 0 {
		return phaseResult{Message: fmt.Sprintf("%s: waiting for k0s %s on %s", label, version, strings.Join(waiting, ", "))}
	}
	return phaseResult{Done: true}
}

func flowProgress(upgrades []v1alpha1.NodeUpgrade, annotation string) map[string]string {
	progress := map[string]string{}
	for _, upgrade := range upgrades {
		progress[upgrade.Spec.Node] = upgrade.Annotations[annotation]
	}
	return progress
}

func activeNodes(order, controllers []string, progress map[string]string, concurrency int) []string {
	started := slices.DeleteFunc(slices.Clone(order), func(name string) bool {
		return progress[name] == "" || progress[name] == nodeDone
	})
	waiting := slices.DeleteFunc(slices.Clone(order), func(name string) bool {
		return progress[name] != ""
	})
	isController := func(name string) bool { return slices.Contains(controllers, name) }
	switch {
	case slices.ContainsFunc(waiting, isController) && len(started) > 0:
		return started
	case slices.ContainsFunc(waiting, isController):
		return []string{waiting[slices.IndexFunc(waiting, isController)]}
	case slices.ContainsFunc(started, isController):
		return started
	}
	free := max(max(concurrency, 1)-len(started), 0)
	return append(started, waiting[:min(free, len(waiting))]...)
}

func etcdProblem(hosts []v1alpha1.Host) string {
	controllers := hostsWithRole(hosts, v1alpha1.RoleControlPlane)
	for _, host := range controllers {
		checks := hostChecks(host)
		switch {
		case checks.EtcdMembers != int32(len(controllers)):
			return fmt.Sprintf("etcd: %s reports %d of %d members", host.Name, checks.EtcdMembers, len(controllers))
		case !checks.EtcdHealthy:
			return fmt.Sprintf("etcd: %s is not healthy", host.Name)
		}
	}
	return ""
}

func hostChecks(host v1alpha1.Host) v1alpha1.HostChecks {
	if host.Status.Checks == nil {
		return v1alpha1.HostChecks{}
	}
	return *host.Status.Checks
}

func uncordonedCeph(ctx context.Context, env upgradeEnv, active []string, progress map[string]string) string {
	if !slices.ContainsFunc(active, func(name string) bool { return progress[name] == nodeUncordoned }) {
		return ""
	}
	report, err := readCeph(ctx, env.Client, env.Exec)
	if err != nil {
		return "ceph: " + err.Error()
	}
	return cephPGProblem(report)
}

func hostNamed(hosts []v1alpha1.Host, name string) v1alpha1.Host {
	for _, host := range hosts {
		if host.Name == name {
			return host
		}
	}
	return v1alpha1.Host{}
}

func nodeFactsFor(in flowInput, name string) nodeFacts {
	host := hostNamed(in.Hosts, name)
	node, hasNode := findNode(in.Nodes, name)
	index := slices.IndexFunc(in.Upgrades, func(upgrade v1alpha1.NodeUpgrade) bool { return upgrade.Spec.Node == name })
	var upgrade v1alpha1.NodeUpgrade
	if index >= 0 {
		upgrade = in.Upgrades[index]
	}
	names := in.Flow.Steps(host)
	steps := make([]v1alpha1.NodeUpgradeStepStatus, 0, len(names))
	for _, step := range names {
		steps = append(steps, currentStep(upgrade, step))
	}
	return nodeFacts{
		Name:       name,
		Progress:   in.Progress[name],
		Controller: slices.Contains(host.Spec.Roles, v1alpha1.RoleControlPlane),
		HasNode:    hasNode,
		SingleNode: len(in.Nodes) == 1,
		Spare:      spareNode(in.Nodes, name),
		NodeReady:  hasNode && readySince(node, leaseNamed(in.Leases, name), lastFinished(steps)),
		Steps:      steps,
		K0sCurrent: host.Status.K0sVersion == in.Target.Spec.K0sVersion,
		TargetK0s:  in.Target.Spec.K0sVersion,
		Ceph:       in.Ceph,
		Etcd:       etcdProblem(in.Hosts),
	}
}

func spareNode(nodes []corev1.Node, name string) bool {
	return slices.ContainsFunc(nodes, func(node corev1.Node) bool { return node.Name != name && schedulable(node) })
}

func schedulable(node corev1.Node) bool {
	return !node.Spec.Unschedulable && !slices.ContainsFunc(node.Spec.Taints, func(taint corev1.Taint) bool {
		return taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute
	})
}

func nextNodeMove(f nodeFacts) nodeMove {
	switch f.Progress {
	case "":
		return startMove(f)
	case nodeDraining:
		return drainingMove(f)
	case nodeUpdating:
		return updatingMove(f)
	case nodeUncordoned:
		return uncordonedMove(f)
	}
	return nodeMove{}
}

func startMove(f nodeFacts) nodeMove {
	switch {
	case f.Controller && f.Etcd != "":
		return etcdWait(f)
	case !f.HasNode || f.SingleNode:
		return nodeMove{Append: true, Progress: nodeUpdating, Message: f.Name + " drain skipped"}
	case !f.Spare:
		return nodeMove{Message: "waiting for another schedulable node before draining " + f.Name}
	}
	return nodeMove{Cordon: true, Progress: nodeDraining, Message: f.Name + " cordoned"}
}

func drainingMove(f nodeFacts) nodeMove {
	switch {
	case len(f.PodsLeft) > 0:
		return nodeMove{Message: fmt.Sprintf("draining %s: %s", f.Name, strings.Join(f.PodsLeft, ", "))}
	case f.Controller && f.Etcd != "":
		return etcdWait(f)
	}
	return nodeMove{Append: true, Progress: nodeUpdating, Message: f.Name + " drained"}
}

func etcdWait(f nodeFacts) nodeMove {
	return nodeMove{Message: fmt.Sprintf("waiting before updating %s: %s", f.Name, f.Etcd)}
}

func updatingMove(f nodeFacts) nodeMove {
	states := make([]string, 0, len(f.Steps))
	succeeded := 0
	for _, step := range f.Steps {
		if step.State == v1alpha1.StepFailed {
			return nodeMove{Failure: fmt.Sprintf("%s %s failed: %s", f.Name, step.Name, step.Message)}
		}
		if step.State == v1alpha1.StepSucceeded {
			succeeded++
		}
		states = append(states, step.Name+" "+step.State)
	}
	switch {
	case succeeded < len(f.Steps):
		return nodeMove{Message: fmt.Sprintf("updating %s: %s", f.Name, strings.Join(states, ", "))}
	case f.HasNode && !f.NodeReady:
		return nodeMove{Message: fmt.Sprintf("waiting for %s to be Ready", f.Name)}
	case !f.K0sCurrent:
		return nodeMove{Message: fmt.Sprintf("waiting for k0s %s on %s", f.TargetK0s, f.Name)}
	case !f.HasNode:
		return nodeMove{Progress: nodeUncordoned, Message: f.Name + " updated"}
	}
	return nodeMove{Uncordon: true, Progress: nodeUncordoned, Message: f.Name + " uncordoned"}
}

func uncordonedMove(f nodeFacts) nodeMove {
	switch {
	case f.HasNode && f.Ceph != "":
		return nodeMove{Message: f.Name + ": " + f.Ceph}
	case f.Controller && f.Etcd != "":
		return nodeMove{Message: f.Name + ": " + f.Etcd}
	}
	return nodeMove{Progress: nodeDone, Message: f.Name + " done"}
}

func advanceNode(ctx context.Context, c client.Client, in flowInput, name string) (nodeMove, error) {
	facts := nodeFactsFor(in, name)
	if facts.Progress == nodeDraining {
		left, err := drainNode(ctx, c, name)
		if err != nil {
			return nodeMove{}, err
		}
		facts.PodsLeft = left
	}
	move := nextNodeMove(facts)
	return move, applyNodeMove(ctx, c, in, name, move)
}

func applyNodeMove(ctx context.Context, c client.Client, in flowInput, name string, move nodeMove) error {
	if move.Cordon {
		if err := setUnschedulable(ctx, c, name, true); err != nil {
			return err
		}
	}
	if move.Append {
		host := hostNamed(in.Hosts, name)
		want, problem := nodeUpgradeFor(in.Upgrade, host, in.Hosts, in.Nodes, in.Flow.Steps(host))
		if problem != "" {
			return errors.New(problem)
		}
		if err := applyNodeUpgrade(ctx, c, want); err != nil {
			return err
		}
	}
	if move.Uncordon {
		if err := setUnschedulable(ctx, c, name, false); err != nil {
			return err
		}
	}
	if move.Progress == "" {
		return nil
	}
	return setNodeProgress(ctx, c, v1alpha1.NodeUpgradeName(in.Upgrade.To, name), in.Flow.Annotation, move.Progress)
}

func setNodeProgress(ctx context.Context, c client.Client, name, annotation, progress string) error {
	var upgrade v1alpha1.NodeUpgrade
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &upgrade); err != nil {
		return err
	}
	annotations := map[string]string{}
	maps.Copy(annotations, upgrade.Annotations)
	annotations[annotation] = progress
	patched := upgrade.DeepCopy()
	patched.Annotations = annotations
	return c.Patch(ctx, patched, client.MergeFrom(&upgrade))
}

func readySince(node corev1.Node, lease coordinationv1.Lease, since time.Time) bool {
	return nodeReady(node) && lease.Spec.RenewTime != nil && lease.Spec.RenewTime.Time.After(since)
}

func nodeLeases(ctx context.Context, c client.Client) ([]coordinationv1.Lease, error) {
	var leases coordinationv1.LeaseList
	if err := c.List(ctx, &leases, client.InNamespace(corev1.NamespaceNodeLease)); err != nil {
		return nil, err
	}
	return leases.Items, nil
}

func leaseNamed(leases []coordinationv1.Lease, name string) coordinationv1.Lease {
	index := slices.IndexFunc(leases, func(lease coordinationv1.Lease) bool { return lease.Name == name })
	if index < 0 {
		return coordinationv1.Lease{}
	}
	return leases[index]
}

func lastFinished(steps []v1alpha1.NodeUpgradeStepStatus) time.Time {
	var last time.Time
	for _, step := range steps {
		if step.FinishedAt != nil && step.FinishedAt.After(last) {
			last = step.FinishedAt.Time
		}
	}
	return last
}
