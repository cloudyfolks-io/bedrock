package operator

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const (
	workerProgressAnnotation = "bedrock.cloudyfolks.io/workers"
	workerDraining           = "draining"
	workerUpdating           = "updating"
	workerUncordoned         = "uncordoned"
	workerDone               = "done"
)

type workerFacts struct {
	Name       string
	Progress   string
	Controller bool
	HasNode    bool
	NodeReady  bool
	PodsLeft   []string
	Steps      []v1alpha1.NodeUpgradeStepStatus
	K0sCurrent bool
	TargetK0s  string
	Ceph       string
	Etcd       string
}

type workerMove struct {
	Cordon   bool
	Uncordon bool
	Append   bool
	Progress string
	Message  string
	Failure  string
}

type workersInput struct {
	Upgrade  v1alpha1.UpgradeStatus
	Target   v1alpha1.Release
	Hosts    []v1alpha1.Host
	Nodes    []corev1.Node
	Upgrades []v1alpha1.NodeUpgrade
	Progress map[string]string
	Ceph     string
}

func workers(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) (phaseResult, error) {
	upgrade := *cluster.Status.Upgrade
	target, err := optionalRelease(ctx, env.Client, upgrade.To)
	if err != nil {
		return phaseResult{}, err
	}
	if target == nil {
		return phaseResult{Failure: fmt.Sprintf("workers: Release/%s does not exist", upgrade.To)}, nil
	}
	hosts, nodes, err := hostsAndNodes(ctx, env.Client)
	if err != nil {
		return phaseResult{}, err
	}
	if result, done, err := workerPlan(ctx, env, upgrade, *target, hosts, nodes, cluster.Spec.NodeConcurrency); err != nil || !done {
		return result, err
	}
	for _, host := range hosts {
		want, problem := nodeUpgradeFor(upgrade, host, hosts, nodes, []string{v1alpha1.StepPreload})
		if problem != "" {
			return phaseResult{Message: "workers: " + problem}, nil
		}
		if err := applyNodeUpgrade(ctx, env.Client, want); err != nil {
			return phaseResult{}, err
		}
	}
	upgrades, err := listNodeUpgrades(ctx, env.Client, upgrade.To)
	if err != nil {
		return phaseResult{}, err
	}
	progress := workerProgress(upgrades)
	active := activeNodes(workerOrder(hosts), hostNames(hostsWithRole(hosts, v1alpha1.RoleControlPlane)), progress, int(cluster.Spec.NodeConcurrency))
	if len(active) == 0 {
		return phaseResult{Done: true}, nil
	}
	in := workersInput{Upgrade: upgrade, Target: *target, Hosts: hosts, Nodes: nodes, Upgrades: upgrades, Progress: progress, Ceph: workerCeph(ctx, env, active, progress)}
	messages := make([]string, 0, len(active))
	for _, name := range active {
		move, err := advanceWorker(ctx, env.Client, in, name)
		if err != nil {
			return phaseResult{}, err
		}
		if move.Failure != "" {
			return phaseResult{Failure: "workers: " + move.Failure}, nil
		}
		messages = append(messages, move.Message)
	}
	return phaseResult{Message: "workers: " + strings.Join(messages, "; ")}, nil
}

func workerPlan(ctx context.Context, env upgradeEnv, upgrade v1alpha1.UpgradeStatus, target v1alpha1.Release, hosts []v1alpha1.Host, nodes []corev1.Node, concurrency int32) (phaseResult, bool, error) {
	workerOnly := hostsWithoutRole(hosts, v1alpha1.RoleControlPlane)
	if len(workerOnly) == 0 {
		return phaseResult{}, true, nil
	}
	platforms, problem := k0sPlatforms(hosts, target, nodeArchitectures(nodes))
	if problem != "" {
		return phaseResult{Failure: "workers: " + problem}, false, nil
	}
	want := autopilotPlan(planID(upgrade.To, "workers", upgrade.Attempt), target.Spec.K0sVersion, platforms, nil, hostNames(workerOnly), int64(max(concurrency, 1)), time.Now().UTC().Format(time.RFC3339))
	plan, message, err := ensurePlan(ctx, env.Client, want)
	if err != nil {
		return phaseResult{}, false, err
	}
	if plan == nil {
		return phaseResult{Message: "workers: " + message}, false, nil
	}
	result := planProgress("workers", plan)
	return result, result.Done, nil
}

func hostsWithoutRole(hosts []v1alpha1.Host, role string) []v1alpha1.Host {
	var matching []v1alpha1.Host
	for _, host := range hosts {
		if !slices.Contains(host.Spec.Roles, role) {
			matching = append(matching, host)
		}
	}
	return matching
}

func workerOrder(hosts []v1alpha1.Host) []string {
	sorted := slices.SortedFunc(slices.Values(hosts), compareHosts)
	return append(hostNames(hostsWithRole(sorted, v1alpha1.RoleControlPlane)), hostNames(hostsWithoutRole(sorted, v1alpha1.RoleControlPlane))...)
}

func updateSteps(host v1alpha1.Host) []string {
	if host.Spec.MaintenanceWindow == "" {
		return []string{v1alpha1.StepPreload, v1alpha1.StepAgentUpdate, v1alpha1.StepReboot}
	}
	return []string{v1alpha1.StepPreload, v1alpha1.StepAgentUpdate, v1alpha1.StepOSUpdate, v1alpha1.StepReboot}
}

func workerProgress(upgrades []v1alpha1.NodeUpgrade) map[string]string {
	progress := map[string]string{}
	for _, upgrade := range upgrades {
		progress[upgrade.Spec.Node] = upgrade.Annotations[workerProgressAnnotation]
	}
	return progress
}

func activeNodes(order, controllers []string, progress map[string]string, concurrency int) []string {
	started := slices.DeleteFunc(slices.Clone(order), func(name string) bool {
		return progress[name] == "" || progress[name] == workerDone
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
		var members int32
		if host.Status.Checks != nil {
			members = host.Status.Checks.EtcdMembers
		}
		if members != int32(len(controllers)) {
			return fmt.Sprintf("etcd: %s reports %d of %d members", host.Name, members, len(controllers))
		}
	}
	return ""
}

func workerCeph(ctx context.Context, env upgradeEnv, active []string, progress map[string]string) string {
	if !slices.ContainsFunc(active, func(name string) bool { return progress[name] == workerUncordoned }) {
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

func workerFactsFor(in workersInput, name string) workerFacts {
	host := hostNamed(in.Hosts, name)
	node, hasNode := findNode(in.Nodes, name)
	index := slices.IndexFunc(in.Upgrades, func(upgrade v1alpha1.NodeUpgrade) bool { return upgrade.Spec.Node == name })
	var upgrade v1alpha1.NodeUpgrade
	if index >= 0 {
		upgrade = in.Upgrades[index]
	}
	steps := make([]v1alpha1.NodeUpgradeStepStatus, 0, 4)
	for _, step := range updateSteps(host) {
		steps = append(steps, currentStep(upgrade, step))
	}
	return workerFacts{
		Name:       name,
		Progress:   in.Progress[name],
		Controller: slices.Contains(host.Spec.Roles, v1alpha1.RoleControlPlane),
		HasNode:    hasNode,
		NodeReady:  hasNode && nodeReady(node),
		Steps:      steps,
		K0sCurrent: host.Status.K0sVersion == in.Target.Spec.K0sVersion,
		TargetK0s:  in.Target.Spec.K0sVersion,
		Ceph:       in.Ceph,
		Etcd:       etcdProblem(in.Hosts),
	}
}

func nextWorkerMove(f workerFacts) workerMove {
	switch f.Progress {
	case "":
		return workerMove{Cordon: f.HasNode, Progress: workerDraining, Message: f.Name + " cordoned"}
	case workerDraining:
		if len(f.PodsLeft) > 0 {
			return workerMove{Message: fmt.Sprintf("draining %s: %s", f.Name, strings.Join(f.PodsLeft, ", "))}
		}
		return workerMove{Append: true, Progress: workerUpdating, Message: f.Name + " drained"}
	case workerUpdating:
		return updatingMove(f)
	case workerUncordoned:
		return uncordonedMove(f)
	}
	return workerMove{}
}

func updatingMove(f workerFacts) workerMove {
	states := make([]string, 0, len(f.Steps))
	succeeded := 0
	for _, step := range f.Steps {
		if step.State == v1alpha1.StepFailed {
			return workerMove{Failure: fmt.Sprintf("%s %s failed: %s", f.Name, step.Name, step.Message)}
		}
		if step.State == v1alpha1.StepSucceeded {
			succeeded++
		}
		states = append(states, step.Name+" "+step.State)
	}
	switch {
	case succeeded < len(f.Steps):
		return workerMove{Message: fmt.Sprintf("updating %s: %s", f.Name, strings.Join(states, ", "))}
	case f.HasNode && !f.NodeReady:
		return workerMove{Message: fmt.Sprintf("waiting for %s to be Ready", f.Name)}
	case !f.K0sCurrent:
		return workerMove{Message: fmt.Sprintf("waiting for k0s %s on %s", f.TargetK0s, f.Name)}
	}
	return workerMove{Uncordon: f.HasNode, Progress: workerUncordoned, Message: f.Name + " uncordoned"}
}

func uncordonedMove(f workerFacts) workerMove {
	switch {
	case f.Ceph != "":
		return workerMove{Message: f.Name + ": " + f.Ceph}
	case f.Controller && f.Etcd != "":
		return workerMove{Message: f.Name + ": " + f.Etcd}
	}
	return workerMove{Progress: workerDone, Message: f.Name + " done"}
}

func advanceWorker(ctx context.Context, c client.Client, in workersInput, name string) (workerMove, error) {
	facts := workerFactsFor(in, name)
	if facts.Progress == workerDraining && facts.HasNode && len(in.Nodes) > 1 {
		left, err := drainNode(ctx, c, name)
		if err != nil {
			return workerMove{}, err
		}
		facts.PodsLeft = left
	}
	move := nextWorkerMove(facts)
	return move, applyWorkerMove(ctx, c, in, name, move)
}

func applyWorkerMove(ctx context.Context, c client.Client, in workersInput, name string, move workerMove) error {
	if move.Cordon {
		if err := setUnschedulable(ctx, c, name, true); err != nil {
			return err
		}
	}
	if move.Append {
		host := hostNamed(in.Hosts, name)
		want, problem := nodeUpgradeFor(in.Upgrade, host, in.Hosts, in.Nodes, updateSteps(host))
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
	return setWorkerProgress(ctx, c, v1alpha1.NodeUpgradeName(in.Upgrade.To, name), move.Progress)
}

func setWorkerProgress(ctx context.Context, c client.Client, name, progress string) error {
	var upgrade v1alpha1.NodeUpgrade
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &upgrade); err != nil {
		return err
	}
	annotations := map[string]string{}
	maps.Copy(annotations, upgrade.Annotations)
	annotations[workerProgressAnnotation] = progress
	patched := upgrade.DeepCopy()
	patched.Annotations = annotations
	return c.Patch(ctx, patched, client.MergeFrom(&upgrade))
}
