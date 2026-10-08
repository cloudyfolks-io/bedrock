package release

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func aaqManifest(t *testing.T, file string) []*unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "manifests", "52-aaq", file))
	if err != nil {
		t.Fatal(err)
	}
	objects, err := splitDocuments(raw)
	if err != nil {
		t.Fatal(err)
	}
	return objects
}

func aaqOperatorContainer(t *testing.T) map[string]any {
	t.Helper()
	for _, obj := range aaqManifest(t, "10-aaq-operator.yaml") {
		if obj.GetKind() != "Deployment" || obj.GetName() != "aaq-operator" {
			continue
		}
		containers, _, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
		if err != nil || len(containers) != 1 {
			t.Fatalf("containers %v, %v", containers, err)
		}
		return containers[0].(map[string]any)
	}
	t.Fatal("Deployment aaq-operator missing")
	return nil
}

func envOf(container map[string]any) map[string]string {
	env := map[string]string{}
	items, _ := container["env"].([]any)
	for _, item := range items {
		entry := item.(map[string]any)
		value, _ := entry["value"].(string)
		env[entry["name"].(string)] = value
	}
	return env
}

func TestAAQOperatorNeverPullsAlways(t *testing.T) {
	container := aaqOperatorContainer(t)
	if container["imagePullPolicy"] != "IfNotPresent" {
		t.Fatalf("imagePullPolicy = %v", container["imagePullPolicy"])
	}
	if got := envOf(container)["PULL_POLICY"]; got != "IfNotPresent" {
		t.Fatalf("PULL_POLICY = %q", got)
	}
}

func TestAAQOperatorImagesShareTheVersion(t *testing.T) {
	container := aaqOperatorContainer(t)
	env := envOf(container)
	if container["image"] != "quay.io/kubevirt/aaq-operator:v1.8.0" {
		t.Fatalf("image = %v", container["image"])
	}
	want := map[string]string{
		"OPERATOR_VERSION": "v1.8.0",
		"CONTROLLER_IMAGE": "quay.io/kubevirt/aaq-controller:v1.8.0",
		"AAQ_SERVER_IMAGE": "quay.io/kubevirt/aaq-server:v1.8.0",
	}
	for name, value := range want {
		if env[name] != value {
			t.Fatalf("%s = %q, want %q", name, env[name], value)
		}
	}
}

func TestAAQCountsVirtualMachinesLikePods(t *testing.T) {
	objects := aaqManifest(t, "20-aaq.yaml")
	if len(objects) != 1 || objects[0].GetKind() != "AAQ" || objects[0].GetName() != "aaq" {
		t.Fatalf("objects = %v", objects)
	}
	policy, _, _ := unstructured.NestedString(objects[0].Object, "spec", "imagePullPolicy")
	name, _, _ := unstructured.NestedString(objects[0].Object, "spec", "configuration", "vmiCalculatorConfiguration", "configName")
	if policy != "IfNotPresent" || name != "VirtualResources" {
		t.Fatalf("imagePullPolicy %q, configName %q", policy, name)
	}
}

func TestAAQComponentIsConfigured(t *testing.T) {
	cfg, err := LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range cfg.Components {
		if component.Name != "aaq" {
			continue
		}
		images := []string{"quay.io/kubevirt/aaq-controller:v1.8.0", "quay.io/kubevirt/aaq-server:v1.8.0"}
		if component.Order != 52 || component.Version != "v1.8.0" || !slices.Equal(component.Dirs, []string{"manifests/52-aaq"}) || component.CRDsToGroup != "crds" || !component.KeepTags || !slices.Equal(component.ExtraImages, images) {
			t.Fatalf("component = %+v", component)
		}
		return
	}
	t.Fatal("component aaq missing")
}
