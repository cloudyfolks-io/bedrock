package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestMultusDaemonHasNoMemoryLimit(t *testing.T) {
	objects, err := loadObjects(os.DirFS(filepath.Join("..", "..")), "manifests/25-multus", "multus")
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objects {
		if obj.GetKind() != "DaemonSet" || obj.GetName() != "kube-multus-ds" {
			continue
		}
		containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
		for _, item := range containers {
			container, _ := item.(map[string]any)
			if container["name"] != "kube-multus" {
				continue
			}
			if limit, found, _ := unstructured.NestedString(container, "resources", "limits", "memory"); found {
				t.Fatalf("kube-multus runs delegate CNI plugins inside its own memory cgroup, so a fixed limit (%s) gets it OOM killed", limit)
			}
			return
		}
	}
	t.Fatal("container kube-multus of DaemonSet kube-multus-ds not found")
}

func TestMultusDoesNotRemoveItsCNIConfigOnExit(t *testing.T) {
	objects, err := loadObjects(os.DirFS(filepath.Join("..", "..")), "manifests/25-multus", "multus")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, obj := range objects {
		switch obj.GetKind() {
		case "DaemonSet":
			containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
			for _, item := range containers {
				container, _ := item.(map[string]any)
				flags := strings.ToLower(strings.Join(append(stringsOf(container["command"]), stringsOf(container["args"])...), " "))
				if strings.Contains(flags, "cleanup-config-on-exit") {
					t.Fatalf("container %v sets a cleanup flag: %s", container["name"], flags)
				}
			}
		case "ConfigMap":
			data, _, _ := unstructured.NestedStringMap(obj.Object, "data")
			for name, content := range data {
				if strings.Contains(strings.ToLower(content), "cleanupconfigonexit") {
					t.Fatalf("%s sets cleanupConfigOnExit", name)
				}
			}
			found = found || obj.GetName() == "multus-daemon-config"
		}
	}
	if !found {
		t.Fatal("ConfigMap multus-daemon-config not found")
	}
}
