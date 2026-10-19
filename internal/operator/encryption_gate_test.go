package operator

import (
	"crypto/rand"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
)

func reportedHost(name, hash string, members int32, age time.Duration) v1alpha1.Host {
	host := controlPlaneHost(name, hash)
	host.Status.Checks.EtcdMembers = members
	reported := metav1.NewTime(rotationNow.Add(-age))
	host.Status.ReportedAt = &reported
	return host
}

func heartbeatNode(name string, age time.Duration) corev1.Node {
	beat := metav1.NewTime(rotationNow.Add(-age))
	return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastHeartbeatTime: beat}}}}
}

func leaseOf(name string, age time.Duration) coordinationv1.Lease {
	renewed := metav1.NewMicroTime(rotationNow.Add(-age))
	return coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: coordinationv1.LeaseSpec{RenewTime: &renewed}}
}

func gateInput(t *testing.T, hosts []v1alpha1.Host, nodes []corev1.Node) rotationInput {
	t.Helper()
	chain := newConfigChain(t)
	return rotationInput{Status: phaseStatus(v1alpha1.RotationAddingKey), Secret: chain.added, Hosts: hosts, Nodes: nodes, Leases: []coordinationv1.Lease{leaseOf("a", 5*time.Second), leaseOf("b", 5*time.Second), leaseOf("c", 5*time.Second)}, Now: rotationNow, Random: rand.Reader}
}

func threeFresh(t *testing.T) ([]v1alpha1.Host, []corev1.Node) {
	t.Helper()
	old := encryption.Hash(newConfigChain(t).one)
	hosts := []v1alpha1.Host{reportedHost("a", old, 3, time.Minute), reportedHost("b", old, 3, time.Minute), reportedHost("c", old, 3, time.Minute)}
	return hosts, []corev1.Node{heartbeatNode("a", time.Minute), heartbeatNode("b", time.Minute), heartbeatNode("c", time.Minute)}
}

func TestGateGrantsWhenEveryReportAndNodeIsFresh(t *testing.T) {
	hosts, nodes := threeFresh(t)
	chain := newConfigChain(t)
	for index := range hosts {
		hosts[index].Status.Encryption.Hash = encryption.Hash(chain.one)
	}
	got := nextRotation(gateInput(t, hosts, nodes))
	if got.Grant != "a" {
		t.Fatalf("decision %+v", got)
	}
}

func TestGateRefusesAStaleButHealthyHostReport(t *testing.T) {
	hosts, nodes := threeFresh(t)
	hosts[2].Status.ReportedAt = ptrTime(rotationNow.Add(-10 * time.Minute))
	got := nextRotation(gateInput(t, hosts, nodes))
	if got.Grant != "" || got.Stalled != stalledWaitingForEtcd || !strings.Contains(got.Status.Message, "c") || !strings.Contains(got.Status.Message, "report") {
		t.Fatalf("decision %+v", got)
	}
}

func TestGateRefusesAHostThatNeverReported(t *testing.T) {
	hosts, nodes := threeFresh(t)
	hosts[1].Status.ReportedAt = nil
	got := nextRotation(gateInput(t, hosts, nodes))
	if got.Grant != "" || got.Stalled != stalledWaitingForEtcd {
		t.Fatalf("decision %+v", got)
	}
}

func TestGateRefusesAControlPlaneHostWithoutANode(t *testing.T) {
	hosts, nodes := threeFresh(t)
	got := nextRotation(gateInput(t, hosts, nodes[:2]))
	if got.Grant != "" || got.Stalled != stalledWaitingForEtcd || !strings.Contains(got.Status.Message, "no Node") {
		t.Fatalf("decision %+v", got)
	}
}

func TestGateNeedsAFreshNodeLeaseAndIgnoresTheReadyHeartbeat(t *testing.T) {
	hosts, nodes := threeFresh(t)
	input := gateInput(t, hosts, nodes)
	input.Leases[2] = leaseOf("c", 45*time.Second)
	got := nextRotation(input)
	if got.Grant != "" || got.Stalled != stalledWaitingForEtcd || !strings.Contains(got.Status.Message, "lease") {
		t.Fatalf("a lease of 45 s must stop the grant while the Node is Ready: %+v", got)
	}
	input = gateInput(t, hosts, nodes)
	input.Leases = input.Leases[:2]
	if got := nextRotation(input); got.Grant != "" || !strings.Contains(got.Status.Message, "lease") {
		t.Fatalf("a missing lease must stop the grant: %+v", got)
	}
}

func TestGateRefusesTimestampsFromTheFuture(t *testing.T) {
	hosts, nodes := threeFresh(t)
	hosts[0].Status.ReportedAt = ptrTime(rotationNow.Add(2 * time.Minute))
	if got := nextRotation(gateInput(t, hosts, nodes)); got.Grant != "" || got.Stalled != stalledWaitingForEtcd {
		t.Fatalf("a report from the future must stop the grant: %+v", got)
	}
	hosts, nodes = threeFresh(t)
	input := gateInput(t, hosts, nodes)
	input.Leases[1] = leaseOf("b", -2*time.Minute)
	if got := nextRotation(input); got.Grant != "" || got.Stalled != stalledWaitingForEtcd {
		t.Fatalf("a lease from the future must stop the grant: %+v", got)
	}
}

func TestGateRefusesAnyMemberMismatch(t *testing.T) {
	hosts, nodes := threeFresh(t)
	hosts[0].Status.Checks.EtcdMembers = 4
	got := nextRotation(gateInput(t, hosts, nodes))
	if got.Grant != "" || got.Stalled != stalledWaitingForEtcd || !strings.Contains(got.Status.Message, "4 of 3") {
		t.Fatalf("decision %+v", got)
	}
}

func ptrTime(at time.Time) *metav1.Time {
	stamp := metav1.NewTime(at)
	return &stamp
}
