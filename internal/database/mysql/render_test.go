package mysql

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
	testUID     = "0b0c0d0e-1111-4222-8333-444455556666"
	sourceUID   = "aaaaaaaa-1111-4222-8333-444455556666"
	destination = "s3://bedrock-database-backups/" + sourceUID + "/2026-10-01-02:00:00-full"
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

func testPodLabels() map[string]string {
	all := labels()
	all["app.kubernetes.io/component"] = "engine"
	return all
}

func companions() map[string]string {
	return map[string]string{
		ImageBackup:  "docker.io/percona/percona-xtrabackup:8.4.0-6.1@sha256:aa",
		ImageHAProxy: "docker.io/percona/haproxy:2.8.18-1@sha256:bb",
		ImageInit:    "docker.io/percona/percona-server-mysql-operator:1.2.0@sha256:cc",
		ImageToolkit: "docker.io/percona/percona-toolkit:3.7.1-4@sha256:dd",
	}
}

func bedrockSpec() engine.Spec {
	return engine.Spec{
		Name:         testUID,
		Namespace:    "bedrock-dbaas",
		Version:      "8.4",
		Image:        "docker.io/percona/percona-server:8.4.10-10.1@sha256:ee",
		CPUMilli:     500,
		MemoryMi:     1024,
		StorageGi:    20,
		Instances:    1,
		StorageClass: "block",
		Labels:       labels(),
		PodLabels:    testPodLabels(),
		Images:       companions(),
		Backup: engine.BackupSpec{
			Enabled: true, Bucket: "bedrock-database-backups", Endpoint: "http://rook-ceph-rgw-bedrock-rgw.rook-ceph.svc:80",
			Region: "us-east-1", RetentionDays: 7, SecretName: "database-backups-s3",
		},
	}
}

func withInstances(s engine.Spec, n int) engine.Spec { s.Instances = n; return s }

func withoutBackup(s engine.Spec) engine.Spec { s.Backup.Enabled = false; return s }

func withRestore(s engine.Spec) engine.Spec {
	s.Restore = &engine.RestoreSpec{SourceUID: sourceUID, Destination: destination}
	return s
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
	cases := map[string]engine.Spec{
		"cluster_one":             bedrockSpec(),
		"cluster_three":           withInstances(bedrockSpec(), 3),
		"cluster_one_no_backup":   withoutBackup(bedrockSpec()),
		"cluster_three_no_backup": withInstances(withoutBackup(bedrockSpec()), 3),
		"cluster_restore":         withRestore(bedrockSpec()),
		"cluster_restore_three":   withInstances(withRestore(bedrockSpec()), 3),
	}
	for name, spec := range cases {
		goldenObjects(t, name, mustRender(t, spec))
	}
}

func dadehatSpec(instances int, restore bool) engine.Spec {
	dadehatLabels := map[string]string{
		"app.kubernetes.io/instance":          testUID,
		"app.kubernetes.io/managed-by":        "database-api",
		"bedrock.cloudyfolks.com/environment": "staging",
		"bedrock.cloudyfolks.com/project-id":  "99999999-1111-4222-8333-444455556666",
	}
	spec := engine.Spec{
		Name: testUID, Namespace: "dbaas", Version: "17",
		Image:    "registry.cloudyfolks.com/mirror-ghcr/example/engine@sha256:" + strings.Repeat("0", 64),
		CPUMilli: 500, MemoryMi: 1024, StorageGi: 20, Instances: instances, StorageClass: "storage-block",
		Labels: dadehatLabels, PodLabels: dadehatLabels,
		Backup: engine.BackupSpec{
			Enabled: true, Bucket: "dbaas-backups", Endpoint: "https://rgw.example.internal",
			Region: "us-east-1", RetentionDays: 7, SecretName: "dbaas-backups-rgw",
		},
	}
	if restore {
		spec.Restore = &engine.RestoreSpec{
			SourceUID:   testUID,
			Destination: "s3://dbaas-backups/" + testUID + "/2026-10-01-02:00:00-full",
		}
	}
	return spec
}

func TestRenderEqualsTheDadehatGolden(t *testing.T) {
	cases := map[string]engine.Spec{
		"mysql_single.golden":  dadehatSpec(1, false),
		"mysql_ha.golden":      dadehatSpec(3, false),
		"mysql_restore.golden": dadehatSpec(1, true),
	}
	for file, spec := range cases {
		want, err := os.ReadFile(filepath.Join("testdata", "dadehat", file))
		if err != nil {
			t.Fatal(err)
		}
		got := objectsJSON(t, mustRender(t, spec))
		if string(want) != string(got) {
			t.Errorf("%s differs.\n--- got ---\n%s\n--- want ---\n%s", file, got, want)
		}
	}
}

func clusterOf(t *testing.T, objs []*unstructured.Unstructured) map[string]any {
	t.Helper()
	for _, obj := range objs {
		if obj.GetKind() == ClusterGVK.Kind {
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
		`"clusterType":"group-replication"`, `"unsafeFlags":{"mysqlSize":true,"proxy":true}`, `partial_revokes=ON`,
		`"secretsName":"` + testUID + `-superuser"`, `"prefix":"` + testUID + `"`, `"storageName":"rgw"`,
		`"schedule":"0 2 * * *"`, `"keep":7`, `"crVersion":"1.2.0"`,
		`"initImage":"docker.io/percona/percona-server-mysql-operator:1.2.0@sha256:cc"`,
		`"toolkit":{"image":"docker.io/percona/percona-toolkit:3.7.1-4@sha256:dd"}`,
		`"image":"docker.io/percona/percona-xtrabackup:8.4.0-6.1@sha256:aa"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("cluster is missing %s", want)
		}
	}
	if strings.Contains(text, `"pause":true`) || strings.Contains(text, "haproxy") {
		t.Error("a new single instance cluster must not start paused or carry HAProxy")
	}
}

func TestRenderThreeInstancesUseHAProxyWithItsImage(t *testing.T) {
	cluster := clusterOf(t, mustRender(t, withInstances(bedrockSpec(), 3)))
	raw, _ := json.Marshal(cluster["spec"])
	text := string(raw)
	for _, want := range []string{`"proxy":{"haproxy":{"enabled":true,"image":"docker.io/percona/haproxy:2.8.18-1@sha256:bb","size":3}}`, `"size":3`} {
		if !strings.Contains(text, want) {
			t.Errorf("spec is missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, "unsafeFlags") {
		t.Error("three instances must not set unsafeFlags")
	}
}

func TestRenderPodLabelsDoNotFightTheOperatorComponentLabel(t *testing.T) {
	cluster := clusterOf(t, mustRender(t, bedrockSpec()))
	podLabelMap := cluster["spec"].(map[string]any)["mysql"].(map[string]any)["labels"].(map[string]any)
	if _, set := podLabelMap["app.kubernetes.io/component"]; set {
		t.Errorf("the component label belongs to the operator: %v", podLabelMap)
	}
	if podLabelMap["app.kubernetes.io/instance"] != testUID {
		t.Errorf("pod labels %v", podLabelMap)
	}
}

func TestRenderServiceSelectsThePrimary(t *testing.T) {
	objs := mustRender(t, bedrockSpec())
	service := objs[len(objs)-1]
	selector, _, _ := unstructured.NestedStringMap(service.Object, "spec", "selector")
	want := map[string]string{
		"app.kubernetes.io/instance": testUID, "app.kubernetes.io/component": "mysql", "mysql.percona.com/primary": "true",
	}
	if len(selector) != len(want) {
		t.Fatalf("selector %v", selector)
	}
	for key, value := range want {
		if selector[key] != value {
			t.Errorf("selector %v", selector)
		}
	}
	if service.GetName() != testUID+"-db" {
		t.Errorf("service %s", service.GetName())
	}
}

func TestParametersByMemory(t *testing.T) {
	cases := []struct {
		memoryMi int
		pool     string
		maxConn  string
	}{
		{512, "innodb_buffer_pool_size=281M", "max_connections=60"},
		{1024, "innodb_buffer_pool_size=563M", "max_connections=150"},
		{2048, "innodb_buffer_pool_size=1126M", "max_connections=150"},
		{4096, "innodb_buffer_pool_size=2252M", "max_connections=250"},
		{8192, "innodb_buffer_pool_size=4505M", "max_connections=400"},
	}
	for _, c := range cases {
		text := configuration(c.memoryMi)
		for _, want := range []string{"utf8mb4", "partial_revokes=ON", c.pool, c.maxConn} {
			if !strings.Contains(text, want) {
				t.Errorf("%dMi: configuration lacks %s: %s", c.memoryMi, want, text)
			}
		}
	}
}

func TestRestoreRefusesATargetTimeAndAMissingDestination(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)
	spec := withRestore(bedrockSpec())
	spec.Restore.TargetTime = &at
	if _, err := Render(spec); err != ErrTargetTimeUnsupported {
		t.Errorf("target time: %v", err)
	}
	spec = withRestore(bedrockSpec())
	spec.Restore.Destination = ""
	if _, err := Render(spec); err != ErrDestinationMissing {
		t.Errorf("destination: %v", err)
	}
}

func TestRestoreObjectPointsAtTheDestination(t *testing.T) {
	objs := mustRender(t, withRestore(bedrockSpec()))
	var restore *unstructured.Unstructured
	for _, obj := range objs {
		if obj.GetKind() == RestoreGVK.Kind {
			restore = obj
		}
	}
	if restore == nil {
		t.Fatal("no restore object")
	}
	got, _, _ := unstructured.NestedString(restore.Object, "spec", "backupSource", "destination")
	cluster, _, _ := unstructured.NestedString(restore.Object, "spec", "clusterName")
	if got != destination || cluster != testUID || restore.GetName() != testUID+"-restore" {
		t.Errorf("restore %v", restore.Object)
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

func TestPatchesKeepSizeAndProxyTogether(t *testing.T) {
	for _, instances := range []int{1, 3} {
		patches, err := Patches(withInstances(bedrockSpec(), instances))
		if err != nil || len(patches) != 1 || patches[0].GVK != ClusterGVK || patches[0].Name != testUID {
			t.Fatalf("patches %v %v", patches, err)
		}
		body := string(patches[0].Body)
		if instances == 1 && (!strings.Contains(body, `"unsafeFlags":{"mysqlSize":true,"proxy":true}`) || !strings.Contains(body, `"haproxy":{"enabled":false}`)) {
			t.Errorf("one instance: %s", body)
		}
		if instances == 3 && (!strings.Contains(body, `"unsafeFlags":null`) || !strings.Contains(body, `"haproxy":{"enabled":true,"size":3}`)) {
			t.Errorf("three instances: %s", body)
		}
		golden(t, "patch_"+string(rune('0'+instances)), append(patches[0].Body, '\n'))
	}
}

func TestPatchesCarryTheBackupImageOnlyWithBackup(t *testing.T) {
	patches, _ := Patches(bedrockSpec())
	if !strings.Contains(string(patches[0].Body), `"backup":{"image":"docker.io/percona/percona-xtrabackup`) {
		t.Errorf("patch %s", patches[0].Body)
	}
	patches, _ = Patches(withoutBackup(bedrockSpec()))
	if strings.Contains(string(patches[0].Body), `"backup"`) {
		t.Errorf("patch %s", patches[0].Body)
	}
}

func TestSuspendAndRestartPatches(t *testing.T) {
	if got := string(SuspendPatch(testUID, true).Body); got != `{"spec":{"pause":true}}` {
		t.Errorf("suspend %s", got)
	}
	if got := string(SuspendPatch(testUID, false).Body); got != `{"spec":{"pause":false}}` {
		t.Errorf("resume %s", got)
	}
	restart := RestartPatch(testUID, "2026-10-19T10:00:00Z")
	want := `{"spec":{"mysql":{"annotations":{"kubectl.kubernetes.io/restartedAt":"2026-10-19T10:00:00Z"}}}}`
	if restart.Name != testUID || restart.GVK != ClusterGVK || string(restart.Body) != want {
		t.Errorf("restart %+v", restart)
	}
}

func TestHealthForEachState(t *testing.T) {
	cases := []struct {
		state  string
		ready  bool
		reason string
	}{
		{"ready", true, ""},
		{"initializing", false, "initializing"},
		{"paused", false, "paused"},
		{"error", false, "error"},
		{"", false, "waiting for the operator"},
	}
	for _, c := range cases {
		obj := &unstructured.Unstructured{Object: map[string]any{}}
		if c.state != "" {
			obj.Object["status"] = map[string]any{"state": c.state}
		}
		got := Health(obj)
		if got.Ready != c.ready || got.Reason != c.reason {
			t.Errorf("%q: %+v", c.state, got)
		}
	}
}

func TestTargetKeepsThePasswordOutOfArgv(t *testing.T) {
	target := Target("bedrock-dbaas", testUID)
	command := strings.Join(target.Command, " ")
	if !strings.Contains(command, `$(cat /etc/mysql/mysql-users-secret/root)`) || strings.Contains(command, " -p") {
		t.Fatalf("the password must come from the mounted file: %s", command)
	}
	if target.Container != "mysql" || target.Selector["mysql.percona.com/primary"] != "true" || target.Selector["app.kubernetes.io/instance"] != testUID {
		t.Fatalf("target %+v", target)
	}
}

func TestGVKsListEveryEngineKind(t *testing.T) {
	kinds := []string{}
	for _, gvk := range GVKs() {
		kinds = append(kinds, gvk.Kind)
	}
	if strings.Join(kinds, ",") != "PerconaServerMySQL,PerconaServerMySQLBackup,PerconaServerMySQLRestore" {
		t.Fatalf("kinds %v", kinds)
	}
}
