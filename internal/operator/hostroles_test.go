package operator

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestRoleLabels(t *testing.T) {
	labels := RoleLabels([]string{v1alpha1.RoleControlPlane, v1alpha1.RoleFabricGateway})
	if labels["bedrock.cloudyfolks.io/role-control-plane"] != "true" {
		t.Fatal("control-plane label missing")
	}
	if labels["fabric.cloudyfolks.io/external-gw"] != "true" || labels["fabric/role"] != "master" {
		t.Fatal("fabric labels missing")
	}
	if _, ok := labels["bedrock.cloudyfolks.io/role-workload"]; ok {
		t.Fatal("workload label must be absent")
	}
}

func TestRoleTaintsWithoutWorkload(t *testing.T) {
	taints := RoleTaints([]string{v1alpha1.RoleControlPlane})
	if len(taints) != 1 || taints[0].Key != "bedrock.cloudyfolks.io/no-workload" || taints[0].Effect != corev1.TaintEffectNoSchedule {
		t.Fatalf("taints %v", taints)
	}
	if len(RoleTaints([]string{v1alpha1.RoleWorkload})) != 0 {
		t.Fatal("workload node must have no taint")
	}
}

func TestApplyRolesIsIdempotentAndClears(t *testing.T) {
	node := &corev1.Node{}
	node.Labels = map[string]string{"kubernetes.io/hostname": "n1", "bedrock.cloudyfolks.io/role-ceph-osd": "true"}
	changed := ApplyRoles(node, []string{v1alpha1.RoleWorkload}, false)
	if !changed {
		t.Fatal("first apply must report a change")
	}
	if _, ok := node.Labels["bedrock.cloudyfolks.io/role-ceph-osd"]; ok {
		t.Fatal("stale role label must be cleared")
	}
	if node.Labels["kubernetes.io/hostname"] != "n1" {
		t.Fatal("foreign labels must be kept")
	}
	if ApplyRoles(node, []string{v1alpha1.RoleWorkload}, false) {
		t.Fatal("second apply must report no change")
	}
}

func TestApplyRolesTaintOrderIsIgnored(t *testing.T) {
	node := &corev1.Node{}
	node.Spec.Taints = []corev1.Taint{
		{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
		{Key: "bedrock.cloudyfolks.io/no-workload", Effect: corev1.TaintEffectNoSchedule},
	}
	ApplyRoles(node, []string{v1alpha1.RoleControlPlane}, false)

	node.Spec.Taints = []corev1.Taint{
		{Key: "bedrock.cloudyfolks.io/no-workload", Effect: corev1.TaintEffectNoSchedule},
		{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
	}
	if ApplyRoles(node, []string{v1alpha1.RoleControlPlane}, false) {
		t.Fatal("reordered taints must not report a change")
	}
}
