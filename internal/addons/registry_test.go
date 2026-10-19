package addons

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func registryFacts() Facts {
	return Facts{
		Settings:         map[string]string{"registry.host": "registry.example.com", "database.enabled": "true"},
		EnabledAddons:    map[string]bool{CNPG: true},
		CertManagerReady: true,
	}
}

func TestRegistryIsKnownAndOffByDefault(t *testing.T) {
	def, ok := Lookup(Registry)
	if !ok || def.EnabledSetting != "registry.enabled" || def.Check == nil || def.Vars == nil || def.Adjust == nil || def.Preflight != nil {
		t.Fatalf("registry %+v", def)
	}
}

func TestRegistryCheckPassesWithAllPrerequisites(t *testing.T) {
	if got := RegistryCheck(registryFacts()); got.State != VerdictPassed || got.Reason != "Passed" {
		t.Fatalf("verdict %+v", got)
	}
}

func TestRegistryCheckNamesEveryMissingPiece(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Facts)
		reason string
		hint   string
	}{
		{"no host", func(f *Facts) { f.Settings["registry.host"] = "" }, "HostNotSet", "registry.host"},
		{"cnpg off", func(f *Facts) { f.EnabledAddons[CNPG] = false }, "CNPGAddonOff", "cnpg"},
		{"cnpg missing", func(f *Facts) { delete(f.EnabledAddons, CNPG) }, "CNPGAddonOff", "cnpg"},
		{"database off", func(f *Facts) { f.Settings["database.enabled"] = "false" }, "DatabaseDisabled", "database.enabled"},
		{"database unset", func(f *Facts) { delete(f.Settings, "database.enabled") }, "DatabaseDisabled", "database.enabled"},
		{"cert-manager", func(f *Facts) { f.CertManagerReady = false }, "CertManagerNotReady", "cert-manager"},
	}
	for _, c := range cases {
		facts := registryFacts()
		c.change(&facts)
		got := RegistryCheck(facts)
		if got.State != VerdictUnknown || got.Reason != c.reason || !strings.Contains(got.Message, c.hint) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestRegistryCheckListsAllMissingPieces(t *testing.T) {
	got := RegistryCheck(Facts{Settings: map[string]string{}, EnabledAddons: map[string]bool{}})
	for _, hint := range []string{"registry.host", "cnpg", "database.enabled", "cert-manager"} {
		if !strings.Contains(got.Message, hint) {
			t.Errorf("message %q lacks %q", got.Message, hint)
		}
	}
	if got.Reason != "HostNotSet" {
		t.Errorf("reason %q", got.Reason)
	}
}

func TestRegistryVars(t *testing.T) {
	settings := map[string]string{"registry.host": "registry.example.com", "registry.storage-class": "block", "registry.bucket": "images", "registry.http-proxy": "http://proxy:3128"}
	vars, err := registryVars(nil, settings)
	want := map[string]string{
		"BEDROCK_REGISTRY_HOST":          "registry.example.com",
		"BEDROCK_REGISTRY_STORAGE_CLASS": "block",
		"BEDROCK_REGISTRY_BUCKET":        "images",
		"BEDROCK_REGISTRY_HTTP_PROXY":    "http://proxy:3128",
	}
	if err != nil || len(vars) != len(want) {
		t.Fatalf("vars %v %v", vars, err)
	}
	for key, value := range want {
		if vars[key] != value {
			t.Errorf("%s = %q", key, vars[key])
		}
	}
	if _, err := registryVars(nil, map[string]string{}); err == nil {
		t.Error("an empty host was accepted")
	}
}

func deployment(name string, replicas int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": RegistryNamespace},
		"spec":     map[string]any{"replicas": replicas},
	}}
}

func TestRegistryAdjustScalesTheThreeServicesAndDropsTheNamespace(t *testing.T) {
	namespace := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": RegistryNamespace}}}
	input := []*unstructured.Unstructured{namespace, deployment("harbor-core", 1), deployment("harbor-registry", 1), deployment("harbor-jobservice", 1), deployment("harbor-portal", 1)}
	got, err := registryAdjust(input, map[string]string{"registry.replicas": "3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("objects %d", len(got))
	}
	for _, obj := range got {
		replicas, _, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")
		want := int64(3)
		if obj.GetName() == "harbor-portal" {
			want = 1
		}
		if replicas != want {
			t.Errorf("%s replicas %d", obj.GetName(), replicas)
		}
	}
	if replicas, _, _ := unstructured.NestedInt64(input[1].Object, "spec", "replicas"); replicas != 1 {
		t.Error("the input object changed")
	}
	if _, err := registryAdjust(input, map[string]string{"registry.replicas": "x"}); err == nil {
		t.Error("a bad replica count was accepted")
	}
}

func registryObjects(t *testing.T, settings map[string]string) []*unstructured.Unstructured {
	t.Helper()
	bundle, _ := buildAddon(t, Registry)
	def, _ := Lookup(Registry)
	vars, err := def.Vars(nil, settings)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := Render(Registry, bundle.Addons[Registry], vars)
	if err != nil {
		t.Fatal(err)
	}
	adjusted, err := def.Adjust(rendered, settings)
	if err != nil {
		t.Fatal(err)
	}
	return adjusted
}

func registrySettings() map[string]string {
	return map[string]string{
		"registry.host":          "registry.example.com",
		"registry.storage-class": "block",
		"registry.bucket":        "images",
		"registry.http-proxy":    "",
		"registry.replicas":      "2",
	}
}

func TestRegistryRendersTheExternalURLFromTheHost(t *testing.T) {
	objects := registryObjects(t, registrySettings())
	core := find(t, objects, "ConfigMap", "harbor-core")
	if got := configData(t, core, "EXT_ENDPOINT"); got != "https://registry.example.com" {
		t.Errorf("EXT_ENDPOINT %q", got)
	}
}

func TestRegistryRendersTheBucketAndTheStorageClass(t *testing.T) {
	objects := registryObjects(t, registrySettings())
	config := configData(t, find(t, objects, "ConfigMap", "harbor-registry"), "config.yml")
	for _, want := range []string{"bucket: images", "regionendpoint: http://rook-ceph-rgw-bedrock-rgw.rook-ceph.svc:80"} {
		if !strings.Contains(config, want) {
			t.Errorf("registry config lacks %q", want)
		}
	}
	redis := find(t, objects, "StatefulSet", "harbor-redis")
	claims, _, _ := unstructured.NestedSlice(redis.Object, "spec", "volumeClaimTemplates")
	class, _, _ := unstructured.NestedString(claims[0].(map[string]any), "spec", "storageClassName")
	if class != "block" {
		t.Errorf("redis storage class %q", class)
	}
}

func TestRegistryRendersTheProxyOnlyWhenSet(t *testing.T) {
	settings := registrySettings()
	settings["registry.http-proxy"] = "http://proxy.kube-system.svc:10808"
	objects := registryObjects(t, settings)
	env := configData(t, find(t, objects, "ConfigMap", "harbor-core"), "HTTP_PROXY")
	if env != "http://proxy.kube-system.svc:10808" {
		t.Errorf("core HTTP_PROXY %q", env)
	}
	if got := configData(t, find(t, objects, "ConfigMap", "harbor-jobservice-env"), "HTTPS_PROXY"); got != "http://proxy.kube-system.svc:10808" {
		t.Errorf("jobservice HTTPS_PROXY %q", got)
	}
}

func TestRegistryScalesAndKeepsNoNamespace(t *testing.T) {
	settings := registrySettings()
	settings["registry.replicas"] = "4"
	objects := registryObjects(t, settings)
	for _, name := range registryScaledDeployments {
		replicas, _, _ := unstructured.NestedInt64(find(t, objects, "Deployment", name).Object, "spec", "replicas")
		if replicas != 4 {
			t.Errorf("%s replicas %d", name, replicas)
		}
	}
	for _, obj := range objects {
		if obj.GetKind() == "Namespace" {
			t.Errorf("namespace %s is in the addon", obj.GetName())
		}
	}
}

func TestRegistryReadsEverySecretFromTheRuntimeNames(t *testing.T) {
	objects := registryObjects(t, registrySettings())
	core := find(t, objects, "Deployment", "harbor-core")
	raw := podSpecJSON(t, core)
	for _, name := range []string{"harbor-admin", "harbor-core-keys", "harbor-jobservice-keys", "harbor-db-credentials", "harbor-registry-credentials", "harbor-token"} {
		if !strings.Contains(raw, name) {
			t.Errorf("harbor-core does not use the Secret %s", name)
		}
	}
	registry := podSpecJSON(t, find(t, objects, "Deployment", "harbor-registry"))
	for _, name := range []string{"bedrock-registry", "harbor-registry-keys", "harbor-registry-credentials"} {
		if !strings.Contains(registry, name) {
			t.Errorf("harbor-registry does not use the Secret %s", name)
		}
	}
}

func TestRegistryReadSettingsTriggerTheAddon(t *testing.T) {
	for _, name := range []string{"registry.enabled", "registry.host", "registry.replicas", "registry.bucket", "registry.storage-class", "registry.http-proxy", "database.enabled"} {
		if !strings.Contains(strings.Join(readSettings(), ","), name) {
			t.Errorf("%s does not trigger the addon", name)
		}
	}
}

func podSpecJSON(t *testing.T, obj *unstructured.Unstructured) string {
	t.Helper()
	raw, err := json.Marshal(podSpec(t, obj))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
