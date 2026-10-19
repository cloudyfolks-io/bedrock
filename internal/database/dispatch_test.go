package database

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

func engineSpecFor(images map[string]string) engine.Spec {
	return engine.Spec{Name: "uid-1", Namespace: PlatformNamespace, Image: "engine@sha256:aaa", CPUMilli: 500, MemoryMi: 1024, StorageGi: 20, Instances: 1, Images: images}
}

func TestEnginePatchesRouteByEngine(t *testing.T) {
	kinds := map[string]string{
		v1alpha1.DatabaseEnginePostgreSQL: "Cluster",
		v1alpha1.DatabaseEngineMySQL:      "PerconaServerMySQL",
		v1alpha1.DatabaseEngineValkey:     "Deployment",
	}
	for kind, want := range kinds {
		patches, err := EnginePatches(kind, engineSpecFor(map[string]string{"exporter": "exporter@sha256:bbb"}))
		if err != nil || len(patches) == 0 || patches[0].GVK.Kind != want {
			t.Errorf("%s: %+v %v", kind, patches, err)
		}
	}
	if _, err := EnginePatches("oracle", engineSpecFor(nil)); err == nil {
		t.Error("an unknown engine got patches")
	}
}

func TestSuspendAndResumeScaleTheGateAndTheProxy(t *testing.T) {
	spec := engineSpecFor(nil)
	for _, c := range []struct {
		step     Step
		replicas string
	}{{StepSuspend, `"replicas":0`}, {StepResume, `"replicas":1`}} {
		patches, err := StepPatches(v1alpha1.DatabaseEngineValkey, spec, "acme-web", []Step{c.step}, "")
		if err != nil || len(patches) != 3 {
			t.Fatalf("%s: %+v %v", c.step, patches, err)
		}
		gate, proxy := patches[1], patches[2]
		if gate.Name != "uid-1-gate" || gate.Namespace != PlatformNamespace || proxy.Name != "uid-1-proxy" || proxy.Namespace != "acme-web" {
			t.Errorf("%s: gate %+v proxy %+v", c.step, gate, proxy)
		}
		if !strings.Contains(string(gate.Body), c.replicas) || !strings.Contains(string(proxy.Body), c.replicas) {
			t.Errorf("%s: bodies %s %s", c.step, gate.Body, proxy.Body)
		}
	}
}

func TestSuspendUsesTheEnginePatch(t *testing.T) {
	cases := map[string]string{
		v1alpha1.DatabaseEnginePostgreSQL: `cnpg.io/hibernation":"on"`,
		v1alpha1.DatabaseEngineMySQL:      `"pause":true`,
		v1alpha1.DatabaseEngineValkey:     `"replicas":0`,
	}
	for kind, want := range cases {
		patches, err := StepPatches(kind, engineSpecFor(nil), "ns", []Step{StepSuspend}, "")
		if err != nil || !strings.Contains(string(patches[0].Body), want) {
			t.Errorf("%s: %+v %v", kind, patches, err)
		}
	}
}

func TestRestartUsesTheEnginePatch(t *testing.T) {
	cases := map[string]string{
		v1alpha1.DatabaseEnginePostgreSQL: `{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"now"}}}`,
		v1alpha1.DatabaseEngineMySQL:      `{"spec":{"mysql":{"annotations":{"kubectl.kubernetes.io/restartedAt":"now"}}}}`,
		v1alpha1.DatabaseEngineValkey:     `{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"now"}}}}}`,
	}
	for kind, want := range cases {
		patches, err := StepPatches(kind, engineSpecFor(nil), "ns", []Step{StepRestart}, "now")
		if err != nil || len(patches) != 1 || string(patches[0].Body) != want {
			t.Errorf("%s: %+v %v", kind, patches, err)
		}
	}
}

func TestResizeStepsNeedTheEnginePatchesNotAStepPatch(t *testing.T) {
	patches, err := StepPatches(v1alpha1.DatabaseEnginePostgreSQL, engineSpecFor(nil), "ns", []Step{StepResize, StepGrowStorage, StepUpgrade, StepRefreshImage}, "")
	if err != nil || len(patches) != 0 {
		t.Fatalf("patches %+v %v", patches, err)
	}
	if !ChangesPatchSteps([]Step{StepRestart, StepUpgrade}) || ChangesPatchSteps([]Step{StepRestart, StepSuspend}) {
		t.Fatal("ChangesPatchSteps")
	}
	if _, err := StepPatches(v1alpha1.DatabaseEnginePostgreSQL, engineSpecFor(nil), "ns", []Step{"Explode"}, ""); err == nil {
		t.Fatal("unknown step accepted")
	}
}

func backupObject(name, phase string, completed *time.Time, key string) *unstructured.Unstructured {
	status := map[string]any{"phase": phase}
	if completed != nil {
		status[key] = completed.Format(time.RFC3339)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"status": status}}
	obj.SetName(name)
	return obj
}

func TestUpgradeBackupFacts(t *testing.T) {
	old, fresh := time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC), time.Date(2026, 10, 19, 2, 0, 0, 0, time.UTC)
	state := BackupStateOf(v1alpha1.DatabaseEnginePostgreSQL)
	cases := []struct {
		name         string
		backups      []*unstructured.Unstructured
		latest       *time.Time
		preRequested bool
	}{
		{"none", nil, nil, false},
		{"newest completed wins", []*unstructured.Unstructured{backupObject("a", "completed", &old, "stoppedAt"), backupObject("b", "completed", &fresh, "stoppedAt")}, &fresh, false},
		{"running is not completed", []*unstructured.Unstructured{backupObject("a", "running", nil, "")}, nil, false},
		{"a pre-upgrade backup that runs counts as requested", []*unstructured.Unstructured{backupObject("uid-1-pre-100", "running", nil, "")}, nil, true},
		{"a failed pre-upgrade backup does not", []*unstructured.Unstructured{backupObject("uid-1-pre-100", "failed", nil, "")}, nil, false},
		{"a pre-upgrade backup of another database does not", []*unstructured.Unstructured{backupObject("uid-2-pre-100", "running", nil, "")}, nil, false},
		{"a completed pre-upgrade backup is both", []*unstructured.Unstructured{backupObject("uid-1-pre-100", "completed", &fresh, "stoppedAt")}, &fresh, true},
	}
	for _, c := range cases {
		latest, requested := UpgradeBackupFacts("uid-1", c.backups, state)
		if (latest == nil) != (c.latest == nil) || latest != nil && !latest.Equal(*c.latest) || requested != c.preRequested {
			t.Errorf("%s: latest %v requested %v", c.name, latest, requested)
		}
	}
}

func TestPreUpgradeBackupByEngine(t *testing.T) {
	spec := engineSpecFor(nil)
	for kind, want := range map[string]string{v1alpha1.DatabaseEnginePostgreSQL: "Backup", v1alpha1.DatabaseEngineMySQL: "PerconaServerMySQLBackup"} {
		obj, err := PreUpgradeBackup(kind, spec, "uid-1-pre-100")
		if err != nil || obj.GetKind() != want || obj.GetName() != "uid-1-pre-100" || obj.GetNamespace() != PlatformNamespace {
			t.Errorf("%s: %v %v", kind, obj, err)
		}
	}
	if _, err := PreUpgradeBackup(v1alpha1.DatabaseEngineValkey, spec, "x"); err == nil {
		t.Error("valkey has no backup")
	}
}

func TestApplyPatchMergesIntoTheObjectAndIgnoresAMissingOne(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	cfg, err := LoadConfig(ctx, c)
	must(t, err)
	must(t, EnsurePlatform(ctx, c, cfg))
	name := "patched-" + nextSequence()
	create(t, c, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: PlatformNamespace}, Data: map[string]string{"keep": "1", "change": "old"}})
	gvk := schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	must(t, ApplyPatches(ctx, c, []engine.Patch{
		{GVK: gvk, Name: "missing-" + nextSequence(), Body: []byte(`{"data":{"x":"y"}}`)},
		{GVK: gvk, Name: name, Body: []byte(`{"data":{"change":"new","add":"2"}}`)},
	}))
	var current corev1.ConfigMap
	must(t, c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: name}, &current))
	if !slices.Equal([]string{current.Data["keep"], current.Data["change"], current.Data["add"]}, []string{"1", "new", "2"}) {
		t.Fatalf("data %v", current.Data)
	}
}
