package release

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestInstanceTypeReaderIsReadOnly(t *testing.T) {
	objects, err := loadObjects(os.DirFS(filepath.Join("..", "..")), "manifests/50-kubevirt", "kubevirt")
	if err != nil {
		t.Fatal(err)
	}
	var role, binding *unstructured.Unstructured
	for _, obj := range objects {
		if obj.GetName() != "bedrock-instancetype-reader" {
			continue
		}
		switch obj.GetKind() {
		case "ClusterRole":
			role = obj
		case "ClusterRoleBinding":
			binding = obj
		}
	}
	if role == nil || binding == nil {
		t.Fatal("ClusterRole and ClusterRoleBinding bedrock-instancetype-reader must exist")
	}
	rules, _, _ := unstructured.NestedSlice(role.Object, "rules")
	want := []any{map[string]any{
		"apiGroups": []any{"instancetype.kubevirt.io"},
		"resources": []any{"virtualmachineclusterinstancetypes", "virtualmachineclusterpreferences"},
		"verbs":     []any{"get", "list", "watch"},
	}}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules %+v", rules)
	}
	subjects, _, _ := unstructured.NestedSlice(binding.Object, "subjects")
	wantSubjects := []any{map[string]any{"kind": "Group", "name": "system:authenticated", "apiGroup": "rbac.authorization.k8s.io"}}
	roleRef, _, _ := unstructured.NestedMap(binding.Object, "roleRef")
	wantRef := map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "bedrock-instancetype-reader"}
	if !reflect.DeepEqual(subjects, wantSubjects) || !reflect.DeepEqual(roleRef, wantRef) {
		t.Fatalf("subjects %+v roleRef %+v", subjects, roleRef)
	}
}
