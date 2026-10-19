package operator

import (
	"os"
	"path/filepath"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	sigyaml "sigs.k8s.io/yaml"
)

func TestOperatorRoleAllowsKubernetesClusters(t *testing.T) {
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
			{"bedrock.cloudyfolks.io", "kubernetesclusters", "get"},
			{"bedrock.cloudyfolks.io", "kubernetesclusters", "patch"},
			{"bedrock.cloudyfolks.io", "kubernetesclusters/status", "patch"},
			{"bedrock.cloudyfolks.io", "kubernetesclusters/finalizers", "update"},
			{"bedrock.cloudyfolks.io", "virtualmachines", "create"},
			{"k0smotron.io", "clusters", "create"},
			{"k0smotron.io", "clusters", "patch"},
			{"k0smotron.io", "clusters", "delete"},
			{"k0smotron.io", "clusters", "watch"},
			{"k0smotron.io", "jointokenrequests", "create"},
			{"k0smotron.io", "jointokenrequests", "get"},
			{"k0smotron.io", "jointokenrequests", "delete"},
			{"apps", "statefulsets", "get"},
			{"apps", "statefulsets", "watch"},
			{"", "persistentvolumeclaims", "list"},
			{"", "persistentvolumeclaims", "watch"},
			{"", "secrets", "get"},
			{"", "secrets", "create"},
			{"", "secrets", "delete"},
			{"", "secrets", "patch"},
		} {
			if !allows(candidate, want.group, want.resource, want.verb) {
				t.Errorf("the role must allow %s %s/%s (run the release rbac command)", want.verb, want.group, want.resource)
			}
		}
		if allows(candidate, "", "secrets", "list") {
			t.Error("the role must not list or watch every Secret")
		}
	}
}
