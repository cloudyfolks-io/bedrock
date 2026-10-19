package operator

import (
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const reportFreshness = 3 * v1alpha1.AgentReportInterval

func rotationGate(hosts []v1alpha1.Host, nodes []corev1.Node, leases []coordinationv1.Lease, now time.Time) string {
	controllers := controlPlaneHosts(hosts)
	for _, host := range controllers {
		if problem := freshnessProblem(host, nodes, leases, now); problem != "" {
			return problem
		}
	}
	return etcdGate(controllers, nodes)
}

func freshnessProblem(host v1alpha1.Host, nodes []corev1.Node, leases []coordinationv1.Lease, now time.Time) string {
	if reported := host.Status.ReportedAt; reported == nil || now.Sub(reported.Time) > reportFreshness {
		return fmt.Sprintf("a fresh report of host %s", host.Name)
	}
	node, found := findNode(nodes, host.Name)
	if !found {
		return fmt.Sprintf("host %s: it has no Node", host.Name)
	}
	if now.Sub(lastHeartbeat(node, leases)) > reportFreshness {
		return fmt.Sprintf("a fresh heartbeat of the Node of host %s", host.Name)
	}
	return ""
}

func lastHeartbeat(node corev1.Node, leases []coordinationv1.Lease) time.Time {
	var latest time.Time
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.LastHeartbeatTime.After(latest) {
			latest = condition.LastHeartbeatTime.Time
		}
	}
	for _, lease := range leases {
		if lease.Name == node.Name && lease.Spec.RenewTime != nil && lease.Spec.RenewTime.After(latest) {
			latest = lease.Spec.RenewTime.Time
		}
	}
	return latest
}
