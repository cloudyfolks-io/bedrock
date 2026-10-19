package operator

import (
	"os"
	"path/filepath"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	sigyaml "sigs.k8s.io/yaml"
)

func TestOperatorRoleCoversTheRegistry(t *testing.T) {
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
	var allowed [][3]string
	for _, verb := range []string{"create", "delete", "get", "list", "patch", "update", "watch"} {
		allowed = append(allowed,
			[3]string{"traefik.io", "middlewares", verb},
			[3]string{"traefik.io", "ingressroutes", verb},
			[3]string{"cert-manager.io", "certificates", verb},
			[3]string{"bedrock.cloudyfolks.io", "registries", verb},
			[3]string{"bedrock.cloudyfolks.io", "registrycredentials", verb},
		)
	}
	allowed = append(allowed,
		[3]string{"", "secrets", "get"},
		[3]string{"", "secrets", "create"},
		[3]string{"", "secrets", "patch"},
		[3]string{"", "namespaces", "get"},
		[3]string{"", "namespaces", "create"},
		[3]string{"", "namespaces", "patch"},
		[3]string{"apps", "deployments", "list"},
		[3]string{"apps", "deployments", "watch"},
		[3]string{"bedrock.cloudyfolks.io", "registries/status", "update"},
		[3]string{"bedrock.cloudyfolks.io", "registries/status", "patch"},
		[3]string{"bedrock.cloudyfolks.io", "registries/finalizers", "update"},
		[3]string{"bedrock.cloudyfolks.io", "registrycredentials/status", "update"},
		[3]string{"bedrock.cloudyfolks.io", "registrycredentials/finalizers", "update"},
	)
	for _, candidate := range []rbacv1.ClusterRole{role, committed} {
		for _, rule := range allowed {
			if !allows(candidate, rule[0], rule[1], rule[2]) {
				t.Errorf("the role must allow %s %s/%s (run the release rbac command)", rule[2], rule[0], rule[1])
			}
		}
	}
}
