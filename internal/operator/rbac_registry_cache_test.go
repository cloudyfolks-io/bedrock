package operator

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var cachedKind = regexp.MustCompile(`(?:For|Owns|Watches)\(&(\w+)\.(\w+)\{\}`)

var kindGroups = map[string]string{"corev1": "", "appsv1": "apps", "v1alpha1": v1alpha1.GroupVersion.Group}

func TestRegistryControllersOnlyCacheKindsTheRoleCanListAndWatch(t *testing.T) {
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
	files, err := filepath.Glob(filepath.Join("..", "registry", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, file := range files {
		if filepath.Ext(file) != ".go" || regexp.MustCompile(`_test\.go$`).MatchString(file) {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range cachedKind.FindAllStringSubmatch(string(source), -1) {
			group, known := kindGroups[match[1]]
			if !known {
				t.Fatalf("%s: unknown package alias %s", file, match[1])
			}
			seen++
			resource := resourceOf(schema.GroupKind{Group: group, Kind: match[2]}, nil)
			for _, candidate := range []rbacv1.ClusterRole{role, committed} {
				for _, verb := range []string{"list", "watch"} {
					if !allows(candidate, group, resource, verb) {
						t.Errorf("%s caches %s/%s but the operator role has no %s", filepath.Base(file), group, resource, verb)
					}
				}
			}
		}
	}
	if seen < 6 {
		t.Fatalf("found %d cached kinds in the registry controllers", seen)
	}
}
