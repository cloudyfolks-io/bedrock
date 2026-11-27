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

func TestSuspendUsesTheEnginePatch(t *testing.T) {
	cases := map[string]string{
		v1alpha1.DatabaseEnginePostgreSQL: `cnpg.io/hibernation":"on"`,
		v1alpha1.DatabaseEngineMySQL:      `"pause":true`,
		v1alpha1.DatabaseEngineValkey:     `"replicas":0`,
	}
	for kind, want := range cases {
		patches, err := StepPatches(kind, engineSpecFor(nil), []Step{StepSuspend}, "")
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
		patches, err := StepPatches(kind, engineSpecFor(nil), []Step{StepRestart}, "now")
		if err != nil || len(patches) != 1 || string(patches[0].Body) != want {
			t.Errorf("%s: %+v %v", kind, patches, err)
		}
	}
}

func TestResizeStepsNeedTheEnginePatchesNotAStepPatch(t *testing.T) {
	patches, err := StepPatches(v1alpha1.DatabaseEnginePostgreSQL, engineSpecFor(nil), []Step{StepResize, StepGrowStorage, StepUpgrade, StepRefreshImage}, "")
	if err != nil || len(patches) != 0 {
		t.Fatalf("patches %+v %v", patches, err)
	}
	if !ChangesPatchSteps([]Step{StepRestart, StepUpgrade}) || ChangesPatchSteps([]Step{StepRestart, StepSuspend}) {
		t.Fatal("ChangesPatchSteps")
	}
	if _, err := StepPatches(v1alpha1.DatabaseEnginePostgreSQL, engineSpecFor(nil), []Step{"Explode"}, ""); err == nil {
		t.Fatal("unknown step accepted")
	}
}

func cnpgBackupObject(name, phase string, completed *time.Time, key string) *unstructured.Unstructured {
	status := map[string]any{"phase": phase}
	if completed != nil {
		status[key] = completed.Format(time.RFC3339)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"status": status}}
	obj.SetName(name)
	return obj
}

func annotated(obj *unstructured.Unstructured, target string, requested time.Time) *unstructured.Unstructured {
	obj.SetAnnotations(map[string]string{annotationUpgradeTarget: target, annotationUpgradeRequest: requested.UTC().Format(time.RFC3339)})
	return obj
}

func TestUpgradeBackupFacts(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	old, fresh := now.Add(-34*time.Hour), now.Add(-10*time.Hour)
	recent, stale := now.Add(-time.Hour), now.Add(-72*time.Hour)
	state := BackupStateOf(v1alpha1.DatabaseEnginePostgreSQL)
	cases := []struct {
		name      string
		backups   []*unstructured.Unstructured
		latest    *time.Time
		requested bool
		failed    *time.Time
	}{
		{"none", nil, nil, false, nil},
		{"newest completed wins", []*unstructured.Unstructured{cnpgBackupObject("a", "completed", &old, "stoppedAt"), cnpgBackupObject("b", "completed", &fresh, "stoppedAt")}, &fresh, false, nil},
		{"running is not completed", []*unstructured.Unstructured{cnpgBackupObject("a", "running", nil, "")}, nil, false, nil},
		{"a pre-upgrade backup that runs counts as requested", []*unstructured.Unstructured{annotated(cnpgBackupObject("uid-1-pre-100", "running", nil, ""), "18", recent)}, nil, true, nil},
		{"a failed pre-upgrade backup is reported", []*unstructured.Unstructured{annotated(cnpgBackupObject("uid-1-pre-100", "failed", nil, ""), "18", recent)}, nil, false, &recent},
		{"a pre-upgrade backup of another database does not", []*unstructured.Unstructured{annotated(cnpgBackupObject("uid-2-pre-100", "running", nil, ""), "18", recent)}, nil, false, nil},
		{"a completed pre-upgrade backup is both", []*unstructured.Unstructured{annotated(cnpgBackupObject("uid-1-pre-100", "completed", &fresh, "stoppedAt"), "18", recent)}, &fresh, true, nil},
		{"a pre-upgrade backup without a record does not", []*unstructured.Unstructured{cnpgBackupObject("uid-1-pre-100", "completed", &fresh, "stoppedAt")}, nil, false, nil},
		{"a pre-upgrade backup for another target does not", []*unstructured.Unstructured{annotated(cnpgBackupObject("uid-1-pre-100", "completed", &fresh, "stoppedAt"), "17", recent)}, nil, false, nil},
		{"a pre-upgrade backup requested 72 hours ago does not", []*unstructured.Unstructured{annotated(cnpgBackupObject("uid-1-pre-100", "completed", &stale, "stoppedAt"), "18", stale)}, nil, false, nil},
	}
	for _, c := range cases {
		got := UpgradeBackupFacts("uid-1", "18", now, 26*time.Hour, c.backups, state)
		if (got.Latest == nil) != (c.latest == nil) || got.Latest != nil && !got.Latest.Equal(*c.latest) || got.Requested != c.requested || (got.FailedAt == nil) != (c.failed == nil) || got.FailedAt != nil && !got.FailedAt.Equal(*c.failed) {
			t.Errorf("%s: %+v", c.name, got)
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
