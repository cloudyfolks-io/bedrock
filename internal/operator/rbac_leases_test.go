package operator

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	sigyaml "sigs.k8s.io/yaml"
)

func TestOperatorClusterRoleReadsLeasesOnly(t *testing.T) {
	role, err := OperatorRole(rbacBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"get", "list", "watch"} {
		if !allows(role, "coordination.k8s.io", "leases", verb) {
			t.Errorf("the cluster role must allow %s on leases", verb)
		}
	}
	for _, verb := range []string{"create", "update", "patch", "delete"} {
		if allows(role, "coordination.k8s.io", "leases", verb) {
			t.Errorf("the cluster role must not allow %s on leases", verb)
		}
	}
}

func TestOperatorHasANamespacedRoleForItsLeaderElectionLease(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "manifests", "90-bedrock", "operator.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.Role
	var binding rbacv1.RoleBinding
	for _, document := range strings.Split(string(body), "\n---\n") {
		var head struct{ Kind string }
		if err := sigyaml.Unmarshal([]byte(document), &head); err != nil {
			t.Fatal(err)
		}
		switch head.Kind {
		case "Role":
			if err := sigyaml.Unmarshal([]byte(document), &role); err != nil {
				t.Fatal(err)
			}
		case "RoleBinding":
			if err := sigyaml.Unmarshal([]byte(document), &binding); err != nil {
				t.Fatal(err)
			}
		}
	}
	if role.Namespace != "bedrock-system" || len(role.Rules) != 1 {
		t.Fatalf("role %+v", role)
	}
	rule := role.Rules[0]
	for _, verb := range []string{"create", "get", "list", "patch", "update", "watch", "delete"} {
		if !slices.Contains(rule.Verbs, verb) {
			t.Errorf("the election role must allow %s", verb)
		}
	}
	if !slices.Contains(rule.Resources, "leases") || !slices.Contains(rule.APIGroups, "coordination.k8s.io") {
		t.Fatalf("rule %+v", rule)
	}
	if binding.RoleRef.Name != role.Name || len(binding.Subjects) != 1 || binding.Subjects[0].Name != "bedrock-operator" || binding.Subjects[0].Namespace != "bedrock-system" {
		t.Fatalf("binding %+v", binding)
	}
}
