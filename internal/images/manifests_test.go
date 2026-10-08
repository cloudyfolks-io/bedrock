package images

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

func manifestObjects(t *testing.T, elements ...string) []*unstructured.Unstructured {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, elements...)...))
	if err != nil {
		t.Fatal(err)
	}
	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(content), 4096)
	var objects []*unstructured.Unstructured
	for {
		obj := &unstructured.Unstructured{}
		err := decoder.Decode(&obj.Object)
		if errors.Is(err, io.EOF) {
			return objects
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(obj.Object) > 0 {
			objects = append(objects, obj)
		}
	}
}

func objectsOfKind(objects []*unstructured.Unstructured, kind string) []*unstructured.Unstructured {
	var found []*unstructured.Unstructured
	for _, obj := range objects {
		if obj.GetKind() == kind {
			found = append(found, obj)
		}
	}
	return found
}

func decodeInto[T any](t *testing.T, obj *unstructured.Unstructured) T {
	t.Helper()
	var typed T
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &typed); err != nil {
		t.Fatal(err)
	}
	return typed
}

func TestCatalogNamespaceMatchesTheConstant(t *testing.T) {
	objects := manifestObjects(t, "manifests", "90-bedrock", "images.yaml")
	namespaces := objectsOfKind(objects, "Namespace")
	if len(namespaces) != 1 || namespaces[0].GetName() != CatalogNamespace {
		t.Fatalf("namespaces %v", namespaces)
	}
	for _, kind := range []string{"Role", "RoleBinding"} {
		found := objectsOfKind(objects, kind)
		if len(found) != 1 || found[0].GetNamespace() != CatalogNamespace {
			t.Fatalf("%s %v", kind, found)
		}
	}
}

func TestCloneRoleIsScopedToTheCatalogNamespace(t *testing.T) {
	objects := manifestObjects(t, "manifests", "90-bedrock", "images.yaml")
	if len(objectsOfKind(objects, "ClusterRole")) != 0 || len(objectsOfKind(objects, "ClusterRoleBinding")) != 0 {
		t.Fatal("images.yaml must hold no cluster scoped RBAC")
	}
	role := decodeInto[rbacv1.Role](t, objectsOfKind(objects, "Role")[0])
	if len(role.Rules) != 1 {
		t.Fatalf("rules %v", role.Rules)
	}
	rule := role.Rules[0]
	if !slices.Equal(rule.APIGroups, []string{"cdi.kubevirt.io"}) || !slices.Equal(rule.Resources, []string{"datavolumes/source"}) || !slices.Equal(rule.Verbs, []string{"create"}) {
		t.Fatalf("rule %+v", rule)
	}
	binding := decodeInto[rbacv1.RoleBinding](t, objectsOfKind(objects, "RoleBinding")[0])
	if len(binding.Subjects) != 1 || binding.Subjects[0].Kind != "ServiceAccount" || binding.Subjects[0].Name != "bedrock-operator" || binding.Subjects[0].Namespace != "bedrock-system" {
		t.Fatalf("subjects %+v", binding.Subjects)
	}
	if binding.RoleRef.Kind != "Role" || binding.RoleRef.Name != role.Name {
		t.Fatalf("role ref %+v", binding.RoleRef)
	}
}

func TestCloneRoleBindsTheOperatorServiceAccount(t *testing.T) {
	account := objectsOfKind(manifestObjects(t, "manifests", "90-bedrock", "operator.yaml"), "ServiceAccount")[0]
	binding := decodeInto[rbacv1.RoleBinding](t, objectsOfKind(manifestObjects(t, "manifests", "90-bedrock", "images.yaml"), "RoleBinding")[0])
	if binding.Subjects[0].Name != account.GetName() || binding.Subjects[0].Namespace != account.GetNamespace() {
		t.Fatalf("subject %+v, account %s/%s", binding.Subjects[0], account.GetNamespace(), account.GetName())
	}
}

func TestCatalogReaderIsReadOnly(t *testing.T) {
	objects := manifestObjects(t, "manifests", "90-bedrock", "images-catalog-reader.yaml")
	if len(objects) != 2 {
		t.Fatalf("objects %d", len(objects))
	}
	roles := objectsOfKind(objects, "ClusterRole")
	bindings := objectsOfKind(objects, "ClusterRoleBinding")
	if len(roles) != 1 || len(bindings) != 1 {
		t.Fatalf("roles %d bindings %d", len(roles), len(bindings))
	}
	role := decodeInto[rbacv1.ClusterRole](t, roles[0])
	if len(role.Rules) != 1 {
		t.Fatalf("rules %+v", role.Rules)
	}
	rule := role.Rules[0]
	if !slices.Equal(rule.APIGroups, []string{"bedrock.cloudyfolks.io"}) || !slices.Equal(rule.Resources, []string{"clusterimages"}) || !slices.Equal(rule.Verbs, []string{"get", "list", "watch"}) {
		t.Fatalf("rule %+v", rule)
	}
	binding := decodeInto[rbacv1.ClusterRoleBinding](t, bindings[0])
	if len(binding.Subjects) != 1 || binding.Subjects[0].Kind != "Group" || binding.Subjects[0].Name != "system:authenticated" || binding.RoleRef.Name != role.Name {
		t.Fatalf("binding %+v", binding)
	}
}
