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

func TestOperatorRoleHasEveryVerbTheClusterControllerUses(t *testing.T) {
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
	used := []struct {
		group, resource string
		verbs           []string
	}{
		{"k0smotron.io", "clusters", []string{"get", "list", "watch", "create", "patch", "delete"}},
		{"k0smotron.io", "jointokenrequests", []string{"get", "list", "watch", "create", "patch", "delete"}},
		{"apps", "statefulsets", []string{"get", "list", "watch"}},
		{"", "persistentvolumeclaims", []string{"get", "list", "watch"}},
		{"", "pods", []string{"get", "list", "watch"}},
		{"", "secrets", []string{"get", "create", "patch", "delete"}},
		{"", "namespaces", []string{"get", "list", "watch"}},
		{"bedrock.cloudyfolks.io", "kubernetesclusters", []string{"get", "list", "watch", "update", "patch"}},
		{"bedrock.cloudyfolks.io", "kubernetesclusters/status", []string{"update", "patch"}},
		{"bedrock.cloudyfolks.io", "kubernetesclusters/finalizers", []string{"update"}},
		{"bedrock.cloudyfolks.io", "virtualmachines", []string{"get", "list", "watch", "create", "patch", "delete"}},
		{"bedrock.cloudyfolks.io", "containers", []string{"list"}},
		{"bedrock.cloudyfolks.io", "subnets", []string{"get", "list", "watch"}},
		{"bedrock.cloudyfolks.io", "securitygroups", []string{"get", "list", "watch"}},
		{"bedrock.cloudyfolks.io", "networks", []string{"get"}},
		{"bedrock.cloudyfolks.io", "addons", []string{"get", "list", "watch"}},
		{"bedrock.cloudyfolks.io", "settings", []string{"get", "list", "watch"}},
		{"fabric.cloudyfolks.io", "ips", []string{"list", "watch"}},
	}
	for _, candidate := range []rbacv1.ClusterRole{role, committed} {
		for _, entry := range used {
			for _, verb := range entry.verbs {
				if !allows(candidate, entry.group, entry.resource, verb) {
					t.Errorf("the cluster controller uses %s on %s/%s: the role lacks it (run the release rbac command)", verb, entry.group, entry.resource)
				}
			}
		}
	}
}
