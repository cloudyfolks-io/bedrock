package operator

import (
	"os"
	"path/filepath"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	sigyaml "sigs.k8s.io/yaml"
)

func TestOperatorRoleAllowsCompute(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "manifests", "90-bedrock", "operator-rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var committed rbacv1.ClusterRole
	if err := sigyaml.Unmarshal(body, &committed); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []rbacv1.ClusterRole{role, committed} {
		for _, want := range []struct{ group, resource, verb string }{
			{"snapshot.storage.k8s.io", "volumesnapshots", "create"},
			{"kubevirt.io", "virtualmachines", "patch"},
			{"", "pods", "patch"},
			{"cdi.kubevirt.io", "datavolumes", "create"},
			{"fabric.cloudyfolks.io", "security-groups", "get"},
			{"fabric.cloudyfolks.io", "ips", "list"},
		} {
			if !allows(candidate, want.group, want.resource, want.verb) {
				t.Errorf("the role must allow %s %s/%s (run the release rbac command)", want.verb, want.group, want.resource)
			}
		}
		for _, rule := range candidate.Rules {
			for _, resource := range rule.Resources {
				if resource == "datavolumes/source" {
					t.Errorf("the role must not hold datavolumes/source: %+v", rule)
				}
			}
		}
	}
}

func TestOperatorRoleManagesMigrationPolicies(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "manifests", "90-bedrock", "operator-rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var committed rbacv1.ClusterRole
	if err := sigyaml.Unmarshal(body, &committed); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []rbacv1.ClusterRole{role, committed} {
		for _, verb := range []string{"create", "get", "patch", "delete"} {
			if !allows(candidate, "migrations.kubevirt.io", "migrationpolicies", verb) {
				t.Errorf("the role must allow %s migrationpolicies (run the release rbac command)", verb)
			}
		}
		for _, verb := range []string{"list", "watch"} {
			if !allows(candidate, "kubevirt.io", "virtualmachineinstancemigrations", verb) {
				t.Errorf("the role must allow %s virtualmachineinstancemigrations (run the release rbac command)", verb)
			}
		}
	}
}
