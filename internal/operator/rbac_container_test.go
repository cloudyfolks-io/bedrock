package operator

import (
	"os"
	"path/filepath"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	sigyaml "sigs.k8s.io/yaml"
)

func TestOperatorRoleAllowsContainers(t *testing.T) {
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
			{"bedrock.cloudyfolks.io", "containers", "get"},
			{"bedrock.cloudyfolks.io", "containers/status", "patch"},
			{"bedrock.cloudyfolks.io", "containers/finalizers", "update"},
			{"apps", "deployments", "create"},
			{"apps", "deployments", "list"},
			{"apps", "deployments", "watch"},
			{"apps", "replicasets", "list"},
			{"apps", "replicasets", "watch"},
			{"apps", "deployments", "patch"},
			{"autoscaling", "horizontalpodautoscalers", "create"},
			{"autoscaling", "horizontalpodautoscalers", "patch"},
			{"autoscaling", "horizontalpodautoscalers", "watch"},
			{"autoscaling", "horizontalpodautoscalers", "delete"},
			{"node.k8s.io", "runtimeclasses", "list"},
			{"node.k8s.io", "runtimeclasses", "watch"},
			{"", "pods", "list"},
			{"", "secrets", "get"},
			{"bedrock.cloudyfolks.io", "builds", "get"},
			{"bedrock.cloudyfolks.io", "builds/status", "patch"},
			{"bedrock.cloudyfolks.io", "builds/finalizers", "update"},
			{"batch", "jobs", "create"},
			{"batch", "jobs", "delete"},
			{"batch", "jobs", "list"},
			{"batch", "jobs", "watch"},
			{"", "configmaps", "patch"},
			{"bedrock.cloudyfolks.io", "addons", "get"},
		} {
			if !allows(candidate, want.group, want.resource, want.verb) {
				t.Errorf("the role must allow %s %s/%s (run the release rbac command)", want.verb, want.group, want.resource)
			}
		}
	}
}
