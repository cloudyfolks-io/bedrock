package v1alpha1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidRole(t *testing.T) {
	for _, role := range AllRoles() {
		if !ValidRole(role) {
			t.Fatalf("%q must be valid", role)
		}
	}
	if ValidRole("storage") {
		t.Fatal("storage must be invalid")
	}
}

func TestHostHasRole(t *testing.T) {
	host := Host{Spec: HostSpec{Roles: []string{RoleControlPlane, RoleWorkload}}}
	if !HostHasRole(host, RoleWorkload) {
		t.Fatal("workload expected")
	}
	if HostHasRole(host, RoleCephOSD) {
		t.Fatal("ceph-osd not expected")
	}
}

func TestHostConditionsAreAListMap(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "00-crds", "bedrock.cloudyfolks.io_hosts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "x-kubernetes-list-map-keys:\n                - type") || !strings.Contains(text, "x-kubernetes-list-type: map") {
		t.Fatal("hosts CRD conditions must be a list map keyed by type")
	}
}

func TestManagedLabel(t *testing.T) {
	if LabelManaged != "bedrock.cloudyfolks.io/managed" || AgentFieldManager != "bedrock-agent" {
		t.Fatal("agent constants changed")
	}
}
