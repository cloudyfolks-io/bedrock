package addons

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/internal/audit"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func buildAddon(t *testing.T, names ...string) (release.Bundle, string) {
	t.Helper()
	cfg, err := release.LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Components = componentsNamed(cfg.Components, names...)
	out := t.TempDir()
	opts := release.BuildOptions{Version: "v9.9.9", Image: "ghcr.io/cloudyfolks-io/bedrock:v9.9.9", Out: out, Helm: "helm", Root: filepath.Join("..", ".."), CacheDir: t.TempDir()}
	if err := release.Build(cfg, opts); err != nil {
		t.Fatal(err)
	}
	bundle, err := release.Load(os.DirFS(out))
	if err != nil {
		t.Fatal(err)
	}
	return bundle, out
}

func componentsNamed(all []release.ComponentConfig, names ...string) []release.ComponentConfig {
	selected := all[:0:0]
	for _, component := range all {
		if slices.Contains(names, component.Name) {
			selected = append(selected, component)
		}
	}
	return selected
}

func lokiObjects(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	return testBundle.Addons["loki"]
}

func find(t *testing.T, objects []*unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	t.Helper()
	for _, obj := range objects {
		if obj.GetKind() == kind && obj.GetName() == name {
			return obj
		}
	}
	t.Fatalf("missing %s %s", kind, name)
	return nil
}

func configData(t *testing.T, obj *unstructured.Unstructured, key string) string {
	t.Helper()
	value, _, _ := unstructured.NestedString(obj.Object, "data", key)
	if value == "" {
		t.Fatalf("%s has no %s", obj.GetName(), key)
	}
	return value
}

func podSpec(t *testing.T, obj *unstructured.Unstructured) map[string]any {
	t.Helper()
	spec, found, _ := unstructured.NestedMap(obj.Object, "spec", "template", "spec")
	if !found {
		t.Fatalf("%s has no pod spec", obj.GetName())
	}
	return spec
}

func firstContainer(t *testing.T, obj *unstructured.Unstructured) map[string]any {
	t.Helper()
	return podSpec(t, obj)["containers"].([]any)[0].(map[string]any)
}

func TestLokiManifestsUseOnlyKnownPlaceholders(t *testing.T) {
	raw, err := json.Marshal(lokiObjects(t))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, match := range release.Placeholder.FindAllString(string(raw), -1) {
		found[match[2:len(match)-1]] = true
	}
	var names []string
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	want := []string{"BEDROCK_LOKI_PERSISTENCE_SIZE", "BEDROCK_LOKI_PERSISTENCE_STORAGE_CLASS", "BEDROCK_LOKI_RETENTION"}
	if !slices.Equal(names, want) {
		t.Fatalf("placeholders %v", names)
	}
}

func TestLokiHasTheTenObjectsOfTheContract(t *testing.T) {
	objects := lokiObjects(t)
	want := []string{
		"ConfigMap/loki", "ServiceAccount/loki", "Service/loki", "StatefulSet/loki",
		"ConfigMap/alloy", "ServiceAccount/alloy", "ClusterRole/bedrock-alloy", "ClusterRoleBinding/bedrock-alloy",
		"DaemonSet/alloy", "ConfigMap/loki-datasource",
	}
	var got []string
	for _, obj := range objects {
		got = append(got, obj.GetKind()+"/"+obj.GetName())
	}
	sort.Strings(want)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Fatalf("objects %v", got)
	}
}

func TestLokiPVCTemplateUsesPlaceholders(t *testing.T) {
	statefulSet := find(t, lokiObjects(t), "StatefulSet", "loki")
	claims, _, _ := unstructured.NestedSlice(statefulSet.Object, "spec", "volumeClaimTemplates")
	spec := claims[0].(map[string]any)["spec"].(map[string]any)
	size := spec["resources"].(map[string]any)["requests"].(map[string]any)["storage"]
	if size != "${BEDROCK_LOKI_PERSISTENCE_SIZE}" || spec["storageClassName"] != "${BEDROCK_LOKI_PERSISTENCE_STORAGE_CLASS}" {
		t.Fatalf("claim %v", spec)
	}
}

func TestLokiDisablesReporting(t *testing.T) {
	objects := lokiObjects(t)
	if !strings.Contains(configData(t, find(t, objects, "ConfigMap", "loki"), "config.yaml"), "reporting_enabled: false") {
		t.Error("loki reports usage")
	}
	if !slices.Contains(toStrings(firstContainer(t, find(t, objects, "DaemonSet", "alloy"))["args"]), "--disable-reporting") {
		t.Error("alloy reports usage")
	}
}

func toStrings(value any) []string {
	var out []string
	for _, item := range value.([]any) {
		out = append(out, item.(string))
	}
	return out
}

func TestLokiRetentionIsEnabled(t *testing.T) {
	config := configData(t, find(t, lokiObjects(t), "ConfigMap", "loki"), "config.yaml")
	if !strings.Contains(config, "retention_enabled: true") || !strings.Contains(config, "retention_period: ${BEDROCK_LOKI_RETENTION}") {
		t.Fatalf("config %s", config)
	}
}

func TestAlloyReadsEverySource(t *testing.T) {
	objects := lokiObjects(t)
	config := configData(t, find(t, objects, "ConfigMap", "alloy"), "config.alloy")
	for _, part := range []string{"/var/log/pods", "loki.source.journal", "guest-console-log", audit.LogDir + "/" + audit.LogFile} {
		if !strings.Contains(config, part) {
			t.Errorf("alloy config lacks %s", part)
		}
	}
	mounts := map[string]bool{}
	for _, mount := range firstContainer(t, find(t, objects, "DaemonSet", "alloy"))["volumeMounts"].([]any) {
		entry := mount.(map[string]any)
		readOnly, _ := entry["readOnly"].(bool)
		mounts[entry["mountPath"].(string)] = readOnly
	}
	for _, path := range []string{"/var/log", "/var/log/journal", "/run/log/journal", "/etc/machine-id"} {
		if !mounts[path] {
			t.Errorf("%s is not mounted read-only", path)
		}
	}
}

func TestAlloyCanReadRootOnlyFiles(t *testing.T) {
	security := firstContainer(t, find(t, lokiObjects(t), "DaemonSet", "alloy"))["securityContext"].(map[string]any)
	capabilities := security["capabilities"].(map[string]any)
	if security["runAsUser"] != int64(0) || !slices.Equal(toStrings(capabilities["add"]), []string{"DAC_READ_SEARCH"}) || !slices.Equal(toStrings(capabilities["drop"]), []string{"ALL"}) {
		t.Fatalf("security context %v", security)
	}
}

func TestAlloyRunsOnEveryNode(t *testing.T) {
	tolerations := podSpec(t, find(t, lokiObjects(t), "DaemonSet", "alloy"))["tolerations"].([]any)
	if len(tolerations) != 1 || tolerations[0].(map[string]any)["operator"] != "Exists" {
		t.Fatalf("tolerations %v", tolerations)
	}
}

func TestLokiDatasourceIsLabelled(t *testing.T) {
	datasource := find(t, lokiObjects(t), "ConfigMap", "loki-datasource")
	if datasource.GetLabels()["grafana_datasource"] != "1" {
		t.Fatalf("labels %v", datasource.GetLabels())
	}
	if !strings.Contains(configData(t, datasource, "loki-datasource.yaml"), "uid: loki") {
		t.Fatal("datasource uid")
	}
}

func TestLokiImagesArePinnedAndListed(t *testing.T) {
	bundle, out := buildAddon(t, "loki")
	raw, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{"docker.io/grafana/loki:3.7.8", "docker.io/grafana/alloy:v1.20.1"} {
		if !slices.Contains(bundle.Images, image) || !slices.Contains(strings.Split(string(raw), "\n"), image) {
			t.Errorf("image %s missing", image)
		}
	}
}

var commentLine = regexp.MustCompile(`(?m)^\s*#`)

func TestAddonManifestsHaveNoComments(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "manifests", "addons", "loki", "*.yaml"))
	if err != nil || len(files) != 5 {
		t.Fatalf("files %v %v", files, err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if commentLine.Match(raw) {
			t.Errorf("%s has a comment line", file)
		}
	}
}

func TestAlloyReadsPersistentAndVolatileJournals(t *testing.T) {
	spec := podSpec(t, find(t, lokiObjects(t), "DaemonSet", "alloy"))
	types := map[string]string{}
	for _, item := range spec["volumes"].([]any) {
		volume := item.(map[string]any)
		if hostPath, ok := volume["hostPath"].(map[string]any); ok {
			types[hostPath["path"].(string)] = hostPath["type"].(string)
		}
	}
	for _, path := range []string{"/var/log/journal", "/run/log/journal"} {
		if types[path] != "DirectoryOrCreate" {
			t.Errorf("%s has type %q", path, types[path])
		}
	}
}
