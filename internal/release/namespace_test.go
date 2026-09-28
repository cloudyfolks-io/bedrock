package release

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func manifest(apiVersion, kind, namespace, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name}}}
	if namespace != "" {
		obj.SetNamespace(namespace)
	}
	return obj
}

func crd(group, kind, scope string) *unstructured.Unstructured {
	obj := manifest("apiextensions.k8s.io/v1", "CustomResourceDefinition", "", strings.ToLower(kind)+"s."+group)
	obj.Object["spec"] = map[string]any{"group": group, "scope": scope, "names": map[string]any{"kind": kind}}
	return obj
}

func TestDefaultNamespaces(t *testing.T) {
	objects := []*unstructured.Unstructured{
		crd("test.example.io", "Gadget", "Namespaced"),
		crd("test.example.io", "Fleet", "Cluster"),
		manifest("v1", "ServiceAccount", "", "sa"),
		manifest("rbac.authorization.k8s.io/v1", "Role", "", "role"),
		manifest("apps/v1", "Deployment", "other", "deploy"),
		manifest("rbac.authorization.k8s.io/v1", "ClusterRole", "", "cluster-role"),
		manifest("test.example.io/v1", "Gadget", "", "gadget"),
		manifest("test.example.io/v1", "Fleet", "", "fleet"),
	}
	want := map[string]string{
		"gadgets.test.example.io": "",
		"fleets.test.example.io":  "",
		"sa":                      "target",
		"role":                    "target",
		"deploy":                  "other",
		"cluster-role":            "",
		"gadget":                  "target",
		"fleet":                   "",
	}
	got, err := defaultNamespaces(objects, "target")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(objects) {
		t.Fatalf("got %d objects, want %d", len(got), len(objects))
	}
	for _, obj := range got {
		if obj.GetNamespace() != want[obj.GetName()] {
			t.Errorf("%s %s: namespace %q, want %q", obj.GetKind(), obj.GetName(), obj.GetNamespace(), want[obj.GetName()])
		}
	}
	if objects[2].GetNamespace() != "" {
		t.Fatal("defaultNamespaces must not mutate its input")
	}
}

func TestDefaultNamespacesRejectsUnknownScope(t *testing.T) {
	objects := []*unstructured.Unstructured{manifest("unknown.example.io/v1", "Mystery", "", "mystery")}
	if _, err := defaultNamespaces(objects, "target"); err == nil || !strings.Contains(err.Error(), "Mystery") {
		t.Fatalf("expected an unknown scope error naming the kind, got %v", err)
	}
}

func TestClusterScopedKindsMatchAPIServer(t *testing.T) {
	_, cfg := startTestEnv(t)
	client, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	lists, err := client.ServerPreferredResources()
	if err != nil {
		t.Fatal(err)
	}
	for _, list := range lists {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			t.Fatal(err)
		}
		if gv.Group == v1alpha1.GroupVersion.Group {
			continue
		}
		for _, resource := range list.APIResources {
			if strings.Contains(resource.Name, "/") {
				continue
			}
			kind := schema.GroupKind{Group: gv.Group, Kind: resource.Kind}
			if _, listed := clusterScopedKinds[kind]; listed == resource.Namespaced {
				t.Errorf("%s: served namespaced %v, listed as cluster scoped %v", kind, resource.Namespaced, listed)
			}
		}
	}
}
