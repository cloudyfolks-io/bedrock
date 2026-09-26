package operator

import (
	"context"
	"slices"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

const controlPlaneProgressAnnotation = "bedrock.cloudyfolks.io/controlplane"

func controlPlaneFlow() nodeFlow {
	return nodeFlow{
		Label:      "controlplane",
		Annotation: controlPlaneProgressAnnotation,
		Members:    func(hosts []v1alpha1.Host) []v1alpha1.Host { return hostsWithRole(hosts, v1alpha1.RoleControlPlane) },
		Steps:      func(v1alpha1.Host) []string { return []string{v1alpha1.StepK0sUpdate} },
	}
}

func controlPlane(ctx context.Context, env upgradeEnv, cluster v1alpha1.Cluster) (phaseResult, error) {
	return walkNodes(ctx, env, cluster, controlPlaneFlow())
}

func hostsWithRole(hosts []v1alpha1.Host, role string) []v1alpha1.Host {
	var matching []v1alpha1.Host
	for _, host := range hosts {
		if slices.Contains(host.Spec.Roles, role) {
			matching = append(matching, host)
		}
	}
	return matching
}

func hostsNotAt(hosts []v1alpha1.Host, k0sVersion string) []string {
	var waiting []string
	for _, host := range hosts {
		if host.Status.K0sVersion != k0sVersion {
			waiting = append(waiting, host.Name)
		}
	}
	return waiting
}
