package operator

import (
	"context"
	"slices"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
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
	return walkNodes(ctx, env, cluster, workersFlow())
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
	return slices.Concat([]string{v1alpha1.StepPreload}, k0sSteps(host), []string{v1alpha1.StepAgentUpdate}, osSteps(host), []string{v1alpha1.StepReboot})
}

func k0sSteps(host v1alpha1.Host) []string {
	if v1alpha1.HostHasRole(host, v1alpha1.RoleControlPlane) {
		return nil
	}
	return []string{v1alpha1.StepK0sUpdate}
}

func osSteps(host v1alpha1.Host) []string {
	if host.Spec.MaintenanceWindow == "" {
		return nil
	}
	return []string{v1alpha1.StepOSUpdate}
}
