package release

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const releaseYAML = "version: v0.0.1\n"

func addonTree() fstest.MapFS {
	return fstest.MapFS{
		"release.yaml":                       {Data: []byte(releaseYAML)},
		"images.txt":                         {Data: []byte("\n")},
		"manifests/90-base/base.yaml":        {Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: base\n")},
		"manifests/addons/demo/10-demo.yaml": {Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  size: ${BEDROCK_DEMO_X}\n")},
	}
}

func TestLoadKeepsAddonManifestsOutOfGroups(t *testing.T) {
	bundle, err := Load(addonTree())
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Groups) != 1 || bundle.Groups[0].Name != "base" {
		t.Fatalf("groups %+v", bundle.Groups)
	}
	if len(bundle.Addons["demo"]) != 1 {
		t.Fatalf("addons %+v", bundle.Addons)
	}
}

func TestLoadAddonObjectsCarryTheComponentLabel(t *testing.T) {
	bundle, err := Load(addonTree())
	if err != nil {
		t.Fatal(err)
	}
	if got := bundle.Addons["demo"][0].GetLabels()[ComponentLabel]; got != "demo" {
		t.Fatalf("label %q", got)
	}
}

func TestSubstituteLeavesAddonsAlone(t *testing.T) {
	bundle, err := Load(addonTree())
	if err != nil {
		t.Fatal(err)
	}
	out, err := Substitute(bundle, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	value, _, _ := unstructured.NestedString(out.Addons["demo"][0].Object, "data", "size")
	if value != "${BEDROCK_DEMO_X}" {
		t.Fatalf("value %q", value)
	}
}

func TestLoadStillRejectsOtherManifestDirectories(t *testing.T) {
	tree := addonTree()
	tree["manifests/extras/10-x.yaml"] = &fstest.MapFile{Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n")}
	if _, err := Load(tree); err == nil {
		t.Fatal("expected a group name error")
	}
}

func TestLoadBuildConfigAcceptsAddonComponents(t *testing.T) {
	if _, err := LoadBuildConfig(filepath.Join("testdata", "build-addon", "components.yaml")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "components.yaml")
	if err := os.WriteFile(path, []byte("k0sVersion: v1\ncomponents:\n  - name: Demo\n    addon: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBuildConfig(path); err == nil {
		t.Fatal("expected an error for an upper-case addon name")
	}
}

func buildAddonTree(t *testing.T, edit func(BuildConfig) BuildConfig) (Bundle, string) {
	t.Helper()
	cfg, err := LoadBuildConfig(filepath.Join("testdata", "build-addon", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	opts := BuildOptions{Version: "v9.9.9", Image: "ghcr.io/cloudyfolks-io/bedrock:v9.9.9", Out: out, Helm: "helm", Root: filepath.Join("testdata", "build-addon"), CacheDir: t.TempDir()}
	if err := Build(edit(cfg), opts); err != nil {
		t.Fatal(err)
	}
	bundle, err := Load(os.DirFS(out))
	if err != nil {
		t.Fatal(err)
	}
	return bundle, out
}

func unchanged(cfg BuildConfig) BuildConfig { return cfg }

func hooksAllowed(cfg BuildConfig) BuildConfig {
	components := slices.Clone(cfg.Components)
	for i := range components {
		components[i].NoHooks = false
	}
	cfg.Components = components
	return cfg
}

func TestBuildWritesAddonComponentsUnderAddons(t *testing.T) {
	_, out := buildAddonTree(t, unchanged)
	for _, name := range []string{"demo", "authored"} {
		if _, err := os.Stat(filepath.Join(out, "manifests", "addons", name, name+".yaml")); err != nil {
			t.Fatal(err)
		}
		matches, _ := filepath.Glob(filepath.Join(out, "manifests", "*-"+name))
		if len(matches) != 0 {
			t.Fatalf("group directory %v", matches)
		}
	}
}

func kinds(objects []*unstructured.Unstructured) []string {
	var out []string
	for _, obj := range objects {
		out = append(out, obj.GetKind())
	}
	return out
}

func TestBuildNoHooksDropsHookManifests(t *testing.T) {
	without, _ := buildAddonTree(t, unchanged)
	if slices.Contains(kinds(without.Addons["demo"]), "Job") {
		t.Fatalf("hook kept: %v", kinds(without.Addons["demo"]))
	}
	with, _ := buildAddonTree(t, hooksAllowed)
	if !slices.Contains(kinds(with.Addons["demo"]), "Job") {
		t.Fatalf("hook missing: %v", kinds(with.Addons["demo"]))
	}
}

func TestBuildListsTheImagesOfAddons(t *testing.T) {
	bundle, out := buildAddonTree(t, unchanged)
	raw, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{"quay.io/example/demo:1", "quay.io/example/authored:2"} {
		if !slices.Contains(bundle.Images, image) || !slices.Contains(bundle.Spec.Images, image) || !contains(string(raw), image) {
			t.Fatalf("image %s missing: %v", image, bundle.Images)
		}
	}
}

func contains(text, part string) bool {
	return slices.Contains(strings.Split(text, "\n"), part)
}

func TestBuildReportsTheAddonComponentImage(t *testing.T) {
	bundle, _ := buildAddonTree(t, unchanged)
	found := map[string]string{}
	for _, component := range bundle.Spec.Components {
		found[component.Name] = component.Image
	}
	if found["demo"] != "quay.io/example/demo:1" || found["authored"] != "quay.io/example/authored:2" {
		t.Fatalf("components %+v", bundle.Spec.Components)
	}
}

func TestLoadBuildConfigRejectsDuplicateComponentNames(t *testing.T) {
	cases := map[string]string{
		"two groups":      "k0sVersion: v1\ncomponents:\n  - name: demo\n    order: 10\n  - name: demo\n    order: 20\n",
		"group and addon": "k0sVersion: v1\ncomponents:\n  - name: monitoring\n    order: 70\n  - name: monitoring\n    addon: true\n",
		"two addons":      "k0sVersion: v1\ncomponents:\n  - name: demo\n    addon: true\n  - name: demo\n    addon: true\n",
	}
	for name, content := range cases {
		path := filepath.Join(t.TempDir(), "components.yaml")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBuildConfig(path); err == nil || !strings.Contains(err.Error(), "listed twice") {
			t.Errorf("%s: error %v", name, err)
		}
	}
	if _, err := LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml")); err != nil {
		t.Fatalf("the real components file: %v", err)
	}
}
