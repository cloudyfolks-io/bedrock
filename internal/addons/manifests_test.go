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

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
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

func TestLokiHasTheObjectsOfTheContract(t *testing.T) {
	objects := lokiObjects(t)
	want := []string{
		"ConfigMap/loki", "ServiceAccount/loki", "Service/loki", "StatefulSet/loki",
		"ConfigMap/alloy", "ServiceAccount/alloy", "ClusterRole/bedrock-alloy", "ClusterRoleBinding/bedrock-alloy",
		"DaemonSet/alloy", "ConfigMap/loki-datasource", "ServiceMonitor/loki", "PrometheusRule/loki",
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
	for _, path := range []string{"/var/log", "/run/log", "/etc/machine-id"} {
		if !mounts[path] {
			t.Errorf("%s is not mounted read-only", path)
		}
	}
}

func namespaceMetaLabel(label string) string {
	return "__meta_kubernetes_namespace_label_" + regexp.MustCompile(`[^a-zA-Z0-9_]`).ReplaceAllString(label, "_")
}

func alloyRule(source, target string) *regexp.Regexp {
	return regexp.MustCompile(`rule \{\s*source_labels\s*=\s*\["` + regexp.QuoteMeta(source) + `"\]\s*target_label\s*=\s*"` + target + `"\s*\}`)
}

func TestAlloyAttachesNamespaceMetadataToPodTargets(t *testing.T) {
	config := configData(t, find(t, lokiObjects(t), "ConfigMap", "alloy"), "config.alloy")
	discovery := strings.Index(config, `discovery.kubernetes "pods" {`)
	attach := strings.Index(config, "attach_metadata")
	relabel := strings.Index(config, `discovery.relabel "pods" {`)
	if discovery < 0 || attach < discovery || relabel < attach {
		t.Fatalf("attach_metadata is not inside discovery.kubernetes pods: %d %d %d", discovery, attach, relabel)
	}
	if !regexp.MustCompile(`attach_metadata \{\s*namespace = true\s*\}`).MatchString(config) {
		t.Error("attach_metadata does not enable the namespace")
	}
	if !strings.Contains(config, `field = "spec.nodeName=" + coalesce(sys.env("NODE_NAME"), "")`) {
		t.Error("the node selector is gone")
	}
}

func TestAlloyLabelsStreamsWithTheNamespaceTenantAndProject(t *testing.T) {
	config := configData(t, find(t, lokiObjects(t), "ConfigMap", "alloy"), "config.alloy")
	pods := config[strings.Index(config, `discovery.relabel "pods" {`):strings.Index(config, `local.file_match "pods" {`)]
	for label, target := range map[string]string{v1alpha1.LabelTenant: "tenant", v1alpha1.LabelProject: "project"} {
		if !alloyRule(namespaceMetaLabel(label), target).MatchString(pods) {
			t.Errorf("no relabel rule from namespace label %s to %s", label, target)
		}
	}
	if !alloyRule("__meta_kubernetes_namespace", "namespace").MatchString(pods) {
		t.Error("the namespace label is gone")
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

func TestLokiServiceMonitorScrapesTheHTTPPort(t *testing.T) {
	objects := lokiObjects(t)
	monitor := find(t, objects, "ServiceMonitor", "loki")
	if monitor.GetNamespace() != "monitoring" {
		t.Fatalf("namespace %q", monitor.GetNamespace())
	}
	selector, _, _ := unstructured.NestedStringMap(monitor.Object, "spec", "selector", "matchLabels")
	service := find(t, objects, "Service", "loki")
	for key, value := range selector {
		if service.GetLabels()[key] != value {
			t.Errorf("the Service lacks the selected label %s=%s", key, value)
		}
	}
	if len(selector) == 0 {
		t.Fatal("the selector is empty")
	}
	endpoints, _, _ := unstructured.NestedSlice(monitor.Object, "spec", "endpoints")
	if len(endpoints) != 1 || endpoints[0].(map[string]any)["port"] != "http" {
		t.Fatalf("endpoints %v", endpoints)
	}
	ports, _, _ := unstructured.NestedSlice(service.Object, "spec", "ports")
	if ports[0].(map[string]any)["name"] != "http" {
		t.Fatalf("service ports %v", ports)
	}
}

func TestLokiAlertsWhenLogsAreDropped(t *testing.T) {
	rule := find(t, lokiObjects(t), "PrometheusRule", "loki")
	groups, _, _ := unstructured.NestedSlice(rule.Object, "spec", "groups")
	if len(groups) != 1 {
		t.Fatalf("groups %v", groups)
	}
	rules := groups[0].(map[string]any)["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("rules %v", rules)
	}
	alert := rules[0].(map[string]any)
	if alert["expr"] != "increase(loki_discarded_samples_total[10m]) > 0" {
		t.Errorf("expr %v", alert["expr"])
	}
	if alert["labels"].(map[string]any)["severity"] != "warning" || alert["alert"] == nil {
		t.Errorf("alert %v", alert)
	}
}

func TestLokiInventoryListsTheMonitoringObjects(t *testing.T) {
	inventory := inventoryOf(testBundle.Addons["loki"])
	for _, kind := range []string{"ServiceMonitor", "PrometheusRule"} {
		ref := release.ObjectRef{APIVersion: "monitoring.coreos.com/v1", Kind: kind, Namespace: "monitoring", Name: "loki"}
		if _, found := inventory[ref]; !found {
			t.Errorf("the inventory lacks %v", ref)
		}
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
	if err != nil || len(files) != 7 {
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

func TestAlloyReadsBothJournalPaths(t *testing.T) {
	config := configData(t, find(t, lokiObjects(t), "ConfigMap", "alloy"), "config.alloy")
	for _, path := range []string{`path          = "/var/log/journal"`, `path          = "/run/log/journal"`} {
		if !strings.Contains(config, path) {
			t.Errorf("alloy config lacks %s", path)
		}
	}
}

func TestLokiHostPathsAreReadOnlyAndNeverCreated(t *testing.T) {
	for _, name := range []string{Loki} {
		for _, obj := range testBundle.Addons[name] {
			if !workloadKinds[obj.GetKind()] {
				continue
			}
			spec := podSpec(t, obj)
			readOnly := map[string]bool{}
			for _, container := range spec["containers"].([]any) {
				mounts, _ := container.(map[string]any)["volumeMounts"].([]any)
				for _, mount := range mounts {
					entry := mount.(map[string]any)
					value, _ := entry["readOnly"].(bool)
					readOnly[entry["name"].(string)] = value
				}
			}
			volumes, _ := spec["volumes"].([]any)
			for _, item := range volumes {
				volume := item.(map[string]any)
				hostPath, ok := volume["hostPath"].(map[string]any)
				if !ok {
					continue
				}
				if kind := hostPath["type"]; kind == "DirectoryOrCreate" || kind == "FileOrCreate" {
					t.Errorf("%s %s: %v creates a host path", obj.GetKind(), obj.GetName(), hostPath["path"])
				}
				if name := volume["name"].(string); !readOnly[name] {
					t.Errorf("%s %s: host path %v is not mounted read-only", obj.GetKind(), obj.GetName(), hostPath["path"])
				}
			}
		}
	}
}
