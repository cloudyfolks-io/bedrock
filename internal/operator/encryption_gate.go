package operator

import (
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	reportFreshness = 3 * v1alpha1.AgentReportInterval
	leaseFreshness  = 30 * time.Second
	clockSkewLimit  = 30 * time.Second
)

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
	reported := host.Status.ReportedAt
	if reported == nil || !withinBound(now.Sub(reported.Time), reportFreshness) {
		return fmt.Sprintf("a fresh report of host %s", host.Name)
	}
	if _, found := findNode(nodes, host.Name); !found {
		return fmt.Sprintf("host %s: it has no Node", host.Name)
	}
	renewed, found := leaseRenewal(leases, host.Name)
	if !found || !withinBound(now.Sub(renewed), leaseFreshness) {
		return fmt.Sprintf("a fresh node lease of host %s", host.Name)
	}
	return ""
}

func withinBound(age, bound time.Duration) bool {
	return age <= bound && age >= -clockSkewLimit
}

func leaseRenewal(leases []coordinationv1.Lease, name string) (time.Time, bool) {
	for _, lease := range leases {
		if lease.Name == name && lease.Spec.RenewTime != nil {
			return lease.Spec.RenewTime.Time, true
		}
	}
	return time.Time{}, false
}
