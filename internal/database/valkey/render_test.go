package valkey

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

const (
	testUID  = "0b0c0d0e-1111-4222-8333-444455556666"
	exporter = "docker.io/oliver006/redis_exporter:v1.94.0@sha256:0842ef7110e62b40bbde2d5c11f38c6fbd99f7100c75e3866637c1b335a1eff7"
)

func labels() map[string]string {
	return map[string]string{
		"bedrock.cloudyfolks.io/kind":    "Database",
		"bedrock.cloudyfolks.io/name":    "cache",
		"bedrock.cloudyfolks.io/tenant":  "acme",
		"bedrock.cloudyfolks.io/project": "web",
		"app.kubernetes.io/managed-by":   "bedrock",
		"app.kubernetes.io/instance":     testUID,
	}
}

func testPodLabels() map[string]string {
	all := labels()
	all["app.kubernetes.io/component"] = "engine"
	return all
}

func bedrockSpec() engine.Spec {
	return engine.Spec{
		Name:         testUID,
		Namespace:    "bedrock-dbaas",
		Version:      "9.1",
		Image:        "docker.io/valkey/valkey:9.1.2@sha256:418652cfb58ef879d4978c33553735d7147016032d5aefaa14c828e611eb9dfd",
		CPUMilli:     500,
		MemoryMi:     1024,
		StorageGi:    20,
		Instances:    1,
		StorageClass: "block",
		Labels:       labels(),
		PodLabels:    testPodLabels(),
		Images:       map[string]string{ImageExporter: exporter},
	}
}

func mustRender(t *testing.T, s engine.Spec) []*unstructured.Unstructured {
	t.Helper()
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

func kindOf(t *testing.T, objs []*unstructured.Unstructured, kind string) *unstructured.Unstructured {
	t.Helper()
	for _, obj := range objs {
		if obj.GetKind() == kind {
			return obj
		}
	}
	t.Fatalf("no %s", kind)
	return nil
}

func TestRenderGolden(t *testing.T) {
	golden(t, "instance", objectsJSON(t, mustRender(t, bedrockSpec())))
}

func dadehatSpec() engine.Spec {
	dadehatLabels := map[string]string{
		"app.kubernetes.io/managed-by":        "database-api",
		"app.kubernetes.io/instance":          testUID,
		"bedrock.cloudyfolks.com/project-id":  "99999999-1111-4222-8333-444455556666",
		"bedrock.cloudyfolks.com/environment": "staging",
	}
	podLabels := map[string]string{"app.kubernetes.io/component": "engine"}
	for key, value := range dadehatLabels {
		podLabels[key] = value
	}
	return engine.Spec{
		Name: testUID, Namespace: "dbaas", Version: "17",
		Image:    "registry.cloudyfolks.com/mirror-ghcr/example/engine@sha256:" + strings.Repeat("0", 64),
		CPUMilli: 500, MemoryMi: 1024, StorageGi: 20, Instances: 1, StorageClass: "storage-block",
		Labels: dadehatLabels, PodLabels: podLabels,
		Images: map[string]string{ImageExporter: exporter},
	}
}

func withoutKeys(object map[string]any, keys ...string) {
	for _, key := range keys {
		delete(object, key)
	}
}

func normalizedDeployment(t *testing.T, raw []byte, dadehat bool) map[string]any {
	t.Helper()
	var objs []map[string]any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(raw), "dbaas.cloudyfolks.io/component", "app.kubernetes.io/component")), &objs); err != nil {
		t.Fatal(err)
	}
	for _, obj := range objs {
		if obj["kind"] != "Deployment" {
			continue
		}
		pod := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		withoutKeys(pod, "priorityClassName")
		init := pod["initContainers"].([]any)[0].(map[string]any)
		withoutKeys(init, "command", "env")
		containers := pod["containers"].([]any)
		pod["containers"] = containers[:1]
		if !dadehat && len(containers) != 2 {
			t.Fatalf("containers %d", len(containers))
		}
		return obj
	}
	t.Fatal("no deployment")
	return nil
}

func TestRenderEqualsTheDadehatGoldenExceptForTheExporterAndTheMetricsUser(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "dadehat", "valkey_deployment.golden"))
	if err != nil {
		t.Fatal(err)
	}
	got := objectsJSON(t, mustRender(t, dadehatSpec()))
	wantJSON, _ := json.Marshal(normalizedDeployment(t, want, true))
	gotJSON, _ := json.Marshal(normalizedDeployment(t, got, false))
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("deployment differs.\n--- got ---\n%s\n--- want ---\n%s", gotJSON, wantJSON)
	}
	var dadehatObjects []map[string]any
	_ = json.Unmarshal(want, &dadehatObjects)
	rendered := mustRender(t, dadehatSpec())
	for _, kind := range []string{"PersistentVolumeClaim", "Service"} {
		var original map[string]any
		for _, obj := range dadehatObjects {
			if obj["kind"] == kind {
				original = obj
			}
		}
		originalJSON, _ := json.Marshal(original)
		var normalized map[string]any
		_ = json.Unmarshal([]byte(strings.ReplaceAll(string(originalJSON), "dbaas.cloudyfolks.io/component", "app.kubernetes.io/component")), &normalized)
		normalizedJSON, _ := json.Marshal(normalized)
		renderedJSON, _ := json.Marshal(kindOf(t, rendered, kind).Object)
		if string(normalizedJSON) != string(renderedJSON) {
			t.Errorf("%s differs.\n--- got ---\n%s\n--- want ---\n%s", kind, renderedJSON, normalizedJSON)
		}
	}
}

func TestRenderRules(t *testing.T) {
	deployment := kindOf(t, mustRender(t, bedrockSpec()), "Deployment")
	raw, _ := json.Marshal(deployment.Object)
	text := string(raw)
	for _, want := range []string{
		`user default off`, `"--aclfile","/data/users.acl"`, `"--maxmemory","768mb"`, `"--maxmemory-policy","noeviction"`,
		`"--appendonly","yes"`, `"--appendfsync","everysec"`,
		`"subPath":"data"`, `"REDISCLI_AUTH"`, `"secretKeyRef"`, `"type":"Recreate"`, `"replicas":1`, `"automountServiceAccountToken":false`,
		`"runAsUser":999`, `"drop":["ALL"]`, `"claimName":"` + testUID + `-data"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(text, "--requirepass") {
		t.Error("requirepass would be discarded by the aclfile loader")
	}
	for _, rename := range []string{"CONFIG", "DEBUG", "MODULE", "SHUTDOWN", "REPLICAOF", "SLAVEOF"} {
		if !strings.Contains(text, `"--rename-command","`+rename+`",""`) {
			t.Errorf("%s is not renamed away", rename)
		}
	}
}

func TestRenderExporterSidecarRunsAsTheMetricsUser(t *testing.T) {
	deployment := kindOf(t, mustRender(t, bedrockSpec()), "Deployment")
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if len(containers) != 2 {
		t.Fatalf("containers %d", len(containers))
	}
	sidecar := containers[1].(map[string]any)
	raw, _ := json.Marshal(sidecar)
	text := string(raw)
	if sidecar["name"] != "exporter" || sidecar["image"] != exporter {
		t.Errorf("sidecar %v", sidecar)
	}
	for _, want := range []string{`"REDIS_USER","value":"_exporter"`, `"key":"exporter-password"`, `"containerPort":9121`, `"readOnlyRootFilesystem":true`, `"runAsNonRoot":true`} {
		if !strings.Contains(text, want) {
			t.Errorf("sidecar lacks %s: %s", want, text)
		}
	}
	if strings.Contains(text, `"key":"password"`) || strings.Contains(text, "_platform") {
		t.Error("the exporter must not hold the platform credentials")
	}
}

func TestACLFileWritesAReadOnlyMetricsUser(t *testing.T) {
	if !strings.Contains(initScript, "user _exporter on #%s ~* &* -@all +info +ping +client|list +slowlog|get +slowlog|len +latency|latest +latency|history +dbsize") {
		t.Fatal("the metrics user has no restricted rule")
	}
	for _, line := range strings.Split(initScript, `\n`) {
		if strings.HasPrefix(line, "user _exporter") {
			for _, bad := range []string{"@write", "@admin", "@dangerous", "+@all", "+set", "+del", "+flushall"} {
				if strings.Contains(line, bad) {
					t.Errorf("the metrics user holds %s", bad)
				}
			}
		}
	}
	if !strings.Contains(initScript, "user default off") {
		t.Error("the default user must be off")
	}
}

func TestRenderPodMonitorScrapesTheMetricsPort(t *testing.T) {
	monitor := kindOf(t, mustRender(t, bedrockSpec()), "PodMonitor")
	selector, _, _ := unstructured.NestedStringMap(monitor.Object, "spec", "selector", "matchLabels")
	endpoints, _, _ := unstructured.NestedSlice(monitor.Object, "spec", "podMetricsEndpoints")
	if selector["app.kubernetes.io/instance"] != testUID || selector["app.kubernetes.io/component"] != "engine" || len(endpoints) != 1 {
		t.Fatalf("monitor %v", monitor.Object)
	}
	if endpoints[0].(map[string]any)["port"] != "metrics" || monitor.GetName() != testUID {
		t.Fatalf("endpoints %v", endpoints)
	}
}

func TestRenderServiceAndClaim(t *testing.T) {
	objs := mustRender(t, bedrockSpec())
	service := kindOf(t, objs, "Service")
	claim := kindOf(t, objs, "PersistentVolumeClaim")
	selector, _, _ := unstructured.NestedStringMap(service.Object, "spec", "selector")
	size, _, _ := unstructured.NestedString(claim.Object, "spec", "resources", "requests", "storage")
	if service.GetName() != testUID+"-db" || selector["app.kubernetes.io/instance"] != testUID || claim.GetName() != testUID+"-data" || size != "20Gi" {
		t.Fatalf("service %v claim %v", service.Object, claim.Object)
	}
}

func TestRenderRefusesABadSpec(t *testing.T) {
	for name, edit := range map[string]func(*engine.Spec){
		"three instances": func(s *engine.Spec) { s.Instances = 3 },
		"no memory":       func(s *engine.Spec) { s.MemoryMi = 0 },
		"no cpu":          func(s *engine.Spec) { s.CPUMilli = 0 },
		"no storage":      func(s *engine.Spec) { s.StorageGi = 0 },
		"no exporter":     func(s *engine.Spec) { s.Images = nil },
		"restore":         func(s *engine.Spec) { s.Restore = &engine.RestoreSpec{SourceUID: "x"} },
	} {
		spec := bedrockSpec()
		edit(&spec)
		if _, err := Render(spec); err == nil {
			t.Errorf("%s accepted by Render", name)
		}
		if _, err := Patches(spec); err == nil {
			t.Errorf("%s accepted by Patches", name)
		}
	}
}

func TestPatchesCarryThePodAndTheClaimSize(t *testing.T) {
	spec := bedrockSpec()
	spec.MemoryMi, spec.StorageGi = 2048, 50
	patches, err := Patches(spec)
	if err != nil || len(patches) != 2 {
		t.Fatalf("patches %v %v", patches, err)
	}
	if patches[0].GVK != DeploymentGVK || patches[0].Name != testUID || !strings.Contains(string(patches[0].Body), `"--maxmemory","1536mb"`) {
		t.Errorf("deployment patch %+v", patches[0])
	}
	if patches[1].GVK != ClaimGVK || patches[1].Name != testUID+"-data" || string(patches[1].Body) != `{"spec":{"resources":{"requests":{"storage":"50Gi"}}}}` {
		t.Errorf("claim patch %+v", patches[1])
	}
}

func TestSuspendAndRestartPatches(t *testing.T) {
	if got := string(SuspendPatch(testUID, true).Body); got != `{"spec":{"replicas":0}}` {
		t.Errorf("suspend %s", got)
	}
	if got := string(SuspendPatch(testUID, false).Body); got != `{"spec":{"replicas":1}}` {
		t.Errorf("resume %s", got)
	}
	restart := RestartPatch(testUID, "2026-10-19T10:00:00Z")
	want := `{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"2026-10-19T10:00:00Z"}}}}}`
	if restart.GVK != DeploymentGVK || restart.Name != testUID || string(restart.Body) != want {
		t.Errorf("restart %+v", restart)
	}
}

func TestHealthFollowsReadyReplicas(t *testing.T) {
	cases := []struct {
		name  string
		obj   map[string]any
		ready bool
	}{
		{"ready", map[string]any{"spec": map[string]any{"replicas": int64(1)}, "status": map[string]any{"readyReplicas": int64(1)}}, true},
		{"not ready", map[string]any{"spec": map[string]any{"replicas": int64(1)}, "status": map[string]any{}}, false},
		{"suspended", map[string]any{"spec": map[string]any{"replicas": int64(0)}, "status": map[string]any{"readyReplicas": int64(0)}}, false},
		{"replicas unset", map[string]any{"status": map[string]any{"readyReplicas": int64(1)}}, true},
	}
	for _, c := range cases {
		if got := Health(&unstructured.Unstructured{Object: c.obj}); got.Ready != c.ready || (!c.ready && got.Reason == "") {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestTargetUsesThePlatformUserAndNoPasswordInArgv(t *testing.T) {
	target := Target(testUID)
	command := strings.Join(target.Command, " ")
	if target.Container != "valkey" || !strings.Contains(command, "--user _platform") || strings.Contains(command, "-a ") || strings.Contains(command, "--pass") {
		t.Fatalf("target %+v", target)
	}
	if !strings.Contains(target.Selector, testUID) || !strings.Contains(target.Selector, "app.kubernetes.io/component=engine") {
		t.Fatalf("selector %s", target.Selector)
	}
}
