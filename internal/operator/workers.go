package operator

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const workerProgressAnnotation = "bedrock.cloudyfolks.io/workers"

func workersFlow() nodeFlow {
	return nodeFlow{
		Label:      "workers",
		Annotation: workerProgressAnnotation,
		Members:    func(hosts []v1alpha1.Host) []v1alpha1.Host { return hosts },
		Steps:      updateSteps,
	}
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
	return walkNodes(ctx, env, cluster, workersFlow())
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
