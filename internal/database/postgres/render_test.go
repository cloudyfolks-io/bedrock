package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

const (
	testUID   = "0b0c0d0e-1111-4222-8333-444455556666"
	sourceUID = "aaaaaaaa-1111-4222-8333-444455556666"
)

func labels() map[string]string {
	return map[string]string{
		"bedrock.cloudyfolks.io/kind":    "Database",
		"bedrock.cloudyfolks.io/name":    "orders",
		"bedrock.cloudyfolks.io/tenant":  "acme",
		"bedrock.cloudyfolks.io/project": "web",
		"app.kubernetes.io/managed-by":   "bedrock",
		"app.kubernetes.io/instance":     testUID,
	}
}

func podLabels() map[string]string {
	all := labels()
	all["app.kubernetes.io/component"] = "engine"
	return all
}

func bedrockSpec() engine.Spec {
	return engine.Spec{
		Name:         testUID,
		Namespace:    "bedrock-dbaas",
		Version:      "17",
		Image:        "ghcr.io/cloudnative-pg/postgresql:17.9-minimal-trixie@sha256:3c9f2df53e15f3fff0aee630beffe73936590333b7fd80f2239bf59b22a9852e",
		CPUMilli:     500,
		MemoryMi:     1024,
		StorageGi:    20,
		Instances:    1,
		StorageClass: "block",
		Labels:       labels(),
		PodLabels:    podLabels(),
		Backup: engine.BackupSpec{
			Enabled: true, Bucket: "bedrock-database-backups", Endpoint: "http://rook-ceph-rgw-bedrock-rgw.rook-ceph.svc:80",
			Region: "us-east-1", RetentionDays: 7, SecretName: "database-backups-s3",
		},
	}
}

func restoreSpec(base engine.Spec, at *time.Time) engine.Spec {
	base.Restore = &engine.RestoreSpec{SourceUID: sourceUID, TargetTime: at}
	return base
}

func mustRender(t *testing.T, s engine.Spec) []*unstructured.Unstructured {
	t.Helper()
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

func TestRenderGolden(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)
	ha := func(s engine.Spec) engine.Spec { s.Instances = 3; return s }
	noBackup := func(s engine.Spec) engine.Spec { s.Backup.Enabled = false; return s }
	cases := map[string]engine.Spec{
		"cluster_one":                 bedrockSpec(),
		"cluster_three":               ha(bedrockSpec()),
		"cluster_one_no_backup":       noBackup(bedrockSpec()),
		"cluster_three_no_backup":     ha(noBackup(bedrockSpec())),
		"cluster_restore":             restoreSpec(bedrockSpec(), nil),
		"cluster_restore_target_time": restoreSpec(bedrockSpec(), &at),
		"cluster_restore_no_backup":   restoreSpec(noBackup(bedrockSpec()), nil),
	}
	for name, spec := range cases {
		goldenObjects(t, name, mustRender(t, spec))
	}
}

func dadehatSpec(restore bool) engine.Spec {
	labels := map[string]string{
		"app.kubernetes.io/instance":          testUID,
		"app.kubernetes.io/managed-by":        "database-api",
		"bedrock.cloudyfolks.com/environment": "staging",
		"bedrock.cloudyfolks.com/project-id":  "99999999-1111-4222-8333-444455556666",
	}
	spec := engine.Spec{
		Name: testUID, Namespace: "dbaas", Version: "17",
		Image:    "registry.cloudyfolks.com/mirror-ghcr/example/engine@sha256:" + strings.Repeat("0", 64),
		CPUMilli: 500, MemoryMi: 1024, StorageGi: 20, Instances: 1, StorageClass: "storage-block",
		Labels: labels, PodLabels: labels,
		Backup: engine.BackupSpec{
			Enabled: true, Bucket: "dbaas-backups", Endpoint: "https://rgw.example.internal",
			Region: "us-east-1", RetentionDays: 7, SecretName: "dbaas-backups-rgw",
		},
	}
	if restore {
		at := time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)
		spec = restoreSpec(spec, &at)
	}
	return spec
}

func withoutPriorityClass(t *testing.T, raw []byte) []byte {
	t.Helper()
	var objs []map[string]any
	if err := json.Unmarshal(raw, &objs); err != nil {
		t.Fatal(err)
	}
	for _, obj := range objs {
		if spec, ok := obj["spec"].(map[string]any); ok {
			delete(spec, "priorityClassName")
		}
	}
	out, err := json.MarshalIndent(objs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func TestRenderEqualsTheDadehatGoldenExceptForThePriorityClass(t *testing.T) {
	for file, restore := range map[string]bool{"postgres_cluster.golden": false, "postgres_cluster_restore.golden": true} {
		raw, err := os.ReadFile(filepath.Join("testdata", "dadehat", file))
		if err != nil {
			t.Fatal(err)
		}
		want := withoutPriorityClass(t, raw)
		got := objectsJSON(t, mustRender(t, dadehatSpec(restore)))
		if string(want) != string(got) {
			t.Errorf("%s differs.\n--- got ---\n%s\n--- want ---\n%s", file, got, want)
		}
	}
}

func clusterOf(t *testing.T, objs []*unstructured.Unstructured) map[string]any {
	t.Helper()
	for _, obj := range objs {
		if obj.GetKind() == "Cluster" {
			return obj.Object
		}
	}
	t.Fatal("no cluster")
	return nil
}

func TestRenderClusterRules(t *testing.T) {
	cluster := clusterOf(t, mustRender(t, bedrockSpec()))
	raw, _ := json.Marshal(cluster)
	text := string(raw)
	for _, want := range []string{
		`"host all postgres all reject"`, `"enableSuperuserAccess":true`, `"log_min_error_statement":"panic"`,
		`"barmanObjectName":"` + testUID + `"`, `"owner":"dbadmin"`, `"enablePodMonitor":true`, `"password_encryption":"scram-sha-256"`,
		`"timezone":"UTC"`, `"app.kubernetes.io/component":"engine"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("cluster is missing %s", want)
		}
	}
	if strings.Contains(text, "certificates") {
		t.Error("the cluster must not carry custom certificates; the gate ends TLS")
	}
	resources := cluster["spec"].(map[string]any)["resources"].(map[string]any)
	requests, _ := json.Marshal(resources["requests"])
	limits, _ := json.Marshal(resources["limits"])
	if string(requests) != string(limits) {
		t.Errorf("requests and limits differ: %v", resources)
	}
}

func TestParametersByMemory(t *testing.T) {
	cases := []struct {
		memoryMi                              int
		sharedBuffers, effectiveCache, maxCon string
	}{
		{512, "128MB", "307MB", "50"},
		{1024, "256MB", "614MB", "100"},
		{2048, "512MB", "1228MB", "200"},
		{4096, "1024MB", "2457MB", "300"},
		{8192, "2048MB", "4915MB", "500"},
	}
	for _, c := range cases {
		spec := bedrockSpec()
		spec.MemoryMi = c.memoryMi
		params := clusterOf(t, mustRender(t, spec))["spec"].(map[string]any)["postgresql"].(map[string]any)["parameters"].(map[string]any)
		if params["shared_buffers"] != c.sharedBuffers || params["effective_cache_size"] != c.effectiveCache || params["max_connections"] != c.maxCon {
			t.Errorf("%dMi: %v", c.memoryMi, params)
		}
	}
}

func TestRenderRestoreUsesRecoveryAndTheSourcePrefix(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)
	objs := mustRender(t, restoreSpec(bedrockSpec(), &at))
	raw, _ := json.Marshal(clusterOf(t, objs))
	for _, want := range []string{`"recovery"`, `2026-10-01 12:30:00.000000+00`, `"serverName":"` + sourceUID + `"`, `"barmanObjectName":"` + testUID + `-source"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("restore cluster is missing %s: %s", want, raw)
		}
	}
	var sources []string
	for _, obj := range objs {
		if obj.GetKind() == "ObjectStore" {
			path, _, _ := unstructured.NestedString(obj.Object, "spec", "configuration", "destinationPath")
			sources = append(sources, obj.GetName()+" "+path)
		}
	}
	want := []string{testUID + " s3://bedrock-database-backups/" + testUID + "/", testUID + "-source s3://bedrock-database-backups/" + sourceUID + "/"}
	if strings.Join(sources, "|") != strings.Join(want, "|") {
		t.Errorf("object stores %v", sources)
	}
}

func TestRenderWithoutBackupHasNoPluginAndNoSchedule(t *testing.T) {
	spec := bedrockSpec()
	spec.Backup.Enabled = false
	objs := mustRender(t, spec)
	for _, obj := range objs {
		if obj.GetKind() == "ObjectStore" || obj.GetKind() == "ScheduledBackup" {
			t.Errorf("rendered %s without backup", obj.GetKind())
		}
	}
	if _, found, _ := unstructured.NestedSlice(clusterOf(t, objs), "spec", "plugins"); found {
		t.Error("plugins set without backup")
	}
}

func TestRenderRefusesABadSpec(t *testing.T) {
	for name, edit := range map[string]func(*engine.Spec){
		"two instances": func(s *engine.Spec) { s.Instances = 2 },
		"no cpu":        func(s *engine.Spec) { s.CPUMilli = 0 },
		"no memory":     func(s *engine.Spec) { s.MemoryMi = 0 },
		"no storage":    func(s *engine.Spec) { s.StorageGi = 0 },
	} {
		spec := bedrockSpec()
		edit(&spec)
		if _, err := Render(spec); err == nil {
			t.Errorf("%s accepted", name)
		}
		if _, err := Patches(spec); err == nil {
			t.Errorf("%s accepted by Patches", name)
		}
	}
}

func TestPatchesCarryTheMutableFields(t *testing.T) {
	spec := bedrockSpec()
	spec.Instances, spec.MemoryMi, spec.StorageGi = 3, 4096, 50
	patches, err := Patches(spec)
	if err != nil || len(patches) != 1 {
		t.Fatalf("patches %v %v", patches, err)
	}
	patch := patches[0]
	if patch.GVK != ClusterGVK || patch.Name != testUID {
		t.Fatalf("patch target %+v", patch)
	}
	golden(t, "patch_three_4096_50", append(patch.Body, '\n'))
}

func TestSuspendAndRestartPatches(t *testing.T) {
	if got := string(SuspendPatch(testUID, true).Body); got != `{"metadata":{"annotations":{"cnpg.io/hibernation":"on"}}}` {
		t.Errorf("suspend %s", got)
	}
	if got := string(SuspendPatch(testUID, false).Body); got != `{"metadata":{"annotations":{"cnpg.io/hibernation":"off"}}}` {
		t.Errorf("resume %s", got)
	}
	restart := RestartPatch(testUID, "2026-10-19T10:00:00Z")
	if restart.Name != testUID || string(restart.Body) != `{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"2026-10-19T10:00:00Z"}}}` {
		t.Errorf("restart %+v", restart)
	}
}

func clusterWithStatus(phase string, ready, want int64) *unstructured.Unstructured {
	status := map[string]any{"readyInstances": ready}
	if phase != "" {
		status["phase"] = phase
	}
	return &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"instances": want}, "status": status}}
}

func TestHealthForEachClusterPhase(t *testing.T) {
	cases := []struct {
		name   string
		obj    *unstructured.Unstructured
		ready  bool
		reason string
	}{
		{"healthy one", clusterWithStatus("Cluster in healthy state", 1, 1), true, ""},
		{"healthy three", clusterWithStatus("Cluster in healthy state", 3, 3), true, ""},
		{"healthy but one replica down", clusterWithStatus("Cluster in healthy state", 2, 3), false, "Cluster in healthy state"},
		{"setting up", clusterWithStatus("Setting up primary", 0, 1), false, "Setting up primary"},
		{"upgrading", clusterWithStatus("Upgrading cluster", 1, 1), false, "Upgrading cluster"},
		{"unhealthy", clusterWithStatus("Unable to create required cluster objects", 0, 1), false, "Unable to create required cluster objects"},
		{"no status", clusterWithStatus("", 0, 1), false, "waiting for the operator"},
		{"no instances wanted", clusterWithStatus("Cluster in healthy state", 0, 0), false, "Cluster in healthy state"},
	}
	for _, c := range cases {
		got := Health(c.obj)
		if got.Ready != c.ready || got.Reason != c.reason {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}
