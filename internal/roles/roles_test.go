package roles

import (
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestLabelsAndFromLabelsRoundTrip(t *testing.T) {
	in := []string{v1alpha1.RoleWorkload, v1alpha1.RoleControlPlane}
	labels := Labels(in)
	if labels[LabelPrefix+"control-plane"] != "true" || labels[LabelPrefix+"workload"] != "true" {
		t.Fatalf("labels %v", labels)
	}
	out := FromLabels(labels)
	if len(out) != 2 || out[0] != v1alpha1.RoleControlPlane || out[1] != v1alpha1.RoleWorkload {
		t.Fatalf("round trip %v", out)
	}
}

func TestFromLabelsIgnoresForeignAndFalse(t *testing.T) {
	out := FromLabels(map[string]string{"kubernetes.io/hostname": "n", LabelPrefix + "ceph-osd": "false", LabelPrefix + "workload": "true"})
	if len(out) != 1 || out[0] != v1alpha1.RoleWorkload {
		t.Fatalf("got %v", out)
	}
}

func TestTaintsWithoutWorkload(t *testing.T) {
	if len(Taints([]string{v1alpha1.RoleControlPlane})) != 1 || len(Taints([]string{v1alpha1.RoleWorkload})) != 0 {
		t.Fatal("taint rule broken")
	}
}

func TestLabelsFabricRoleFollowsControlPlane(t *testing.T) {
	labels := Labels([]string{v1alpha1.RoleControlPlane})
	if labels[FabricRoleLabel] != "master" {
		t.Fatalf("control-plane must carry %s=master: %v", FabricRoleLabel, labels)
	}
	if _, ok := labels[FabricGWLabel]; ok {
		t.Fatalf("control-plane alone must not carry %s: %v", FabricGWLabel, labels)
	}
}

func TestLabelsFabricGatewayWithoutControlPlane(t *testing.T) {
	labels := Labels([]string{v1alpha1.RoleFabricGateway})
	if labels[FabricGWLabel] != "true" {
		t.Fatalf("fabric-gateway must carry %s=true: %v", FabricGWLabel, labels)
	}
	if _, ok := labels[FabricRoleLabel]; ok {
		t.Fatalf("fabric-gateway alone must not carry %s: %v", FabricRoleLabel, labels)
	}
}
