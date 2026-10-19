package release

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func buildReal(t *testing.T, names ...string) (Bundle, string) {
	t.Helper()
	cfg, err := LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	selected := cfg.Components[:0:0]
	for _, component := range cfg.Components {
		if slices.Contains(names, component.Name) || component.Name == "crds" {
			selected = append(selected, component)
		}
	}
	cfg.Components = selected
	out := t.TempDir()
	opts := BuildOptions{Version: "v9.9.9", Image: "ghcr.io/cloudyfolks-io/bedrock:v9.9.9", Out: out, Helm: "helm", Root: filepath.Join("..", ".."), CacheDir: t.TempDir()}
	if err := Build(cfg, opts); err != nil {
		t.Fatal(err)
	}
	bundle, err := Load(os.DirFS(out))
	if err != nil {
		t.Fatal(err)
	}
	return bundle, out
}

func objectNamed(objects []*unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	for _, obj := range objects {
		if obj.GetKind() == kind && obj.GetName() == name {
			return obj
		}
	}
	return nil
}

func TestKataComponentRendersTheDaemonSetAndRuntimeClasses(t *testing.T) {
	bundle, _ := buildReal(t, "kata")
	objects := bundle.Addons["kata"]
	want := map[string]string{
		"ServiceAccount":     "kata-deploy-sa",
		"ConfigMap":          "kata-kata-deploy-state",
		"ClusterRole":        "kata-deploy-role",
		"ClusterRoleBinding": "kata-deploy-rb",
		"DaemonSet":          "kata-deploy",
	}
	for kind, name := range want {
		if objectNamed(objects, kind, name) == nil {
			t.Errorf("missing %s %s", kind, name)
		}
	}
	for _, name := range []string{"kata-qemu", "kata"} {
		class := objectNamed(objects, "RuntimeClass", name)
		if class == nil {
			t.Fatalf("missing RuntimeClass %s", name)
		}
		if handler, _, _ := unstructured.NestedString(class.Object, "handler"); handler != "kata-qemu" {
			t.Errorf("RuntimeClass %s handler %q", name, handler)
		}
	}
	if len(objects) != 8 {
		t.Fatalf("kinds %v", kinds(objects))
	}
	daemonSet := objectNamed(objects, "DaemonSet", "kata-deploy")
	if daemonSet.GetNamespace() != "kube-system" {
		t.Errorf("namespace %q", daemonSet.GetNamespace())
	}
	selector, _, _ := unstructured.NestedStringMap(daemonSet.Object, "spec", "template", "spec", "nodeSelector")
	if selector["bedrock.cloudyfolks.io/kvm"] != "true" {
		t.Errorf("nodeSelector %v", selector)
	}
	containers, _, _ := unstructured.NestedSlice(daemonSet.Object, "spec", "template", "spec", "containers")
	container := containers[0].(map[string]any)
	if container["imagePullPolicy"] != "IfNotPresent" {
		t.Errorf("imagePullPolicy %v", container["imagePullPolicy"])
	}
	distribution := ""
	for _, env := range container["env"].([]any) {
		if entry := env.(map[string]any); entry["name"] == "K8S_DISTRIBUTION" {
			distribution, _ = entry["value"].(string)
		}
	}
	if distribution != "k0s" {
		t.Errorf("K8S_DISTRIBUTION %q", distribution)
	}
}

func TestKataComponentTunesTheRuntime(t *testing.T) {
	bundle, _ := buildReal(t, "kata")
	objects := bundle.Addons["kata"]
	for _, name := range []string{"kata-qemu", "kata"} {
		class := objectNamed(objects, "RuntimeClass", name)
		if class == nil {
			t.Fatalf("missing RuntimeClass %s", name)
		}
		overhead, _, _ := unstructured.NestedStringMap(class.Object, "overhead", "podFixed")
		if overhead["memory"] != "512Mi" || overhead["cpu"] != "250m" {
			t.Errorf("RuntimeClass %s overhead %v", name, overhead)
		}
	}
	config := objectNamed(objects, "ConfigMap", "kata-deploy-custom-configs")
	if config == nil {
		t.Fatalf("missing the drop-in ConfigMap: %v", kinds(objects))
	}
	dropIn, _, _ := unstructured.NestedString(config.Object, "data", "dropin-qemu.toml")
	for _, line := range []string{"[hypervisor.qemu]", "default_memory = 512", `virtio_fs_extra_args = ["-o", "xattr"]`} {
		if !strings.Contains(dropIn, line) {
			t.Errorf("drop-in does not hold %q: %s", line, dropIn)
		}
	}
}

func TestKataComponentHasNoHooksAndNoNamespace(t *testing.T) {
	bundle, _ := buildReal(t, "kata")
	for _, obj := range bundle.Addons["kata"] {
		if obj.GetKind() == "Namespace" {
			t.Errorf("namespace %s", obj.GetName())
		}
		for key := range obj.GetAnnotations() {
			if strings.HasPrefix(key, "helm.sh/hook") {
				t.Errorf("%s %s has %s", obj.GetKind(), obj.GetName(), key)
			}
		}
	}
}

func TestKataImageIsInTheImageList(t *testing.T) {
	bundle, out := buildReal(t, "kata")
	raw, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(raw), "quay.io/kata-containers/kata-deploy:4.2.0") || !slices.Contains(bundle.Images, "quay.io/kata-containers/kata-deploy:4.2.0") {
		t.Fatalf("images %v", bundle.Images)
	}
}
