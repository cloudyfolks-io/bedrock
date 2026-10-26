package database

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func days(n int32) *int32 { return &n }

func TestConfigReadsTheRetentionBounds(t *testing.T) {
	cfg, err := ParseConfig(nil)
	if err != nil || cfg.RetentionMin != 1 || cfg.RetentionMax != 365 {
		t.Fatalf("defaults %+v %v", cfg, err)
	}
	cfg, err = ParseConfig(map[string]string{"database.backup.retention-days-min": "3", "database.backup.retention-days-max": "30"})
	if err != nil || cfg.RetentionMin != 3 || cfg.RetentionMax != 30 {
		t.Fatalf("values %+v %v", cfg, err)
	}
	cfg, err = ParseConfig(map[string]string{"database.backup.retention-days-min": "40", "database.backup.retention-days-max": "30"})
	if err != nil || cfg.RetentionMin != 1 || cfg.RetentionMax != 365 {
		t.Fatalf("conflict %+v %v", cfg, err)
	}
}

func TestRetentionOfADatabaseFallsBackToTheGlobalValue(t *testing.T) {
	cfg, _ := ParseConfig(map[string]string{"database.backup.retention-days": "9"})
	if got := BackupSpecOf(cfg, BackupStore{}, v1alpha1.DatabaseBackupSpec{Enabled: true}).RetentionDays; got != 9 {
		t.Fatalf("default %d", got)
	}
	if got := BackupSpecOf(cfg, BackupStore{}, v1alpha1.DatabaseBackupSpec{Enabled: true, RetentionDays: days(30)}).RetentionDays; got != 30 {
		t.Fatalf("own %d", got)
	}
}

func TestRetentionRefusalNamesTheBounds(t *testing.T) {
	cfg, _ := ParseConfig(map[string]string{"database.backup.retention-days-min": "3", "database.backup.retention-days-max": "30"})
	for _, tc := range []struct {
		value   *int32
		refused bool
	}{{nil, false}, {days(3), false}, {days(30), false}, {days(2), true}, {days(31), true}} {
		spec := v1alpha1.DatabaseBackupSpec{Enabled: true, RetentionDays: tc.value}
		if refusal := RetentionRefusal(cfg, spec); (refusal != nil) != tc.refused || refusal != nil && refusal.Reason != ReasonRetentionOutOfRange {
			t.Errorf("%v: %+v", tc.value, refusal)
		}
	}
}

func retentionPolicyOf(t *testing.T, w *world) string {
	t.Helper()
	objects, err := EngineObjects(context.Background(), w.c, *w.db)
	must(t, err)
	for _, object := range objects {
		if object.GetKind() == "ObjectStore" {
			policy, _, _ := unstructured.NestedString(object.Object, "spec", "retentionPolicy")
			return policy
		}
	}
	t.Fatal("no ObjectStore")
	return ""
}

func TestTheRetentionOfTheDatabaseReachesTheBackupConfig(t *testing.T) {
	w := newWorld(t, func(spec *v1alpha1.DatabaseSpec) { spec.Backup.RetentionDays = days(30) })
	w.reconcile()
	if got := retentionPolicyOf(t, w); got != "30d" {
		t.Fatalf("policy %q", got)
	}
}

func TestADatabaseWithoutRetentionKeepsTheGlobalValue(t *testing.T) {
	w := newWorld(t, nil)
	w.reconcile()
	if got := retentionPolicyOf(t, w); got != "7d" {
		t.Fatalf("policy %q", got)
	}
}

func TestARetentionOutsideTheBoundsIsRefusedBeforeAnyObject(t *testing.T) {
	w := newWorld(t, func(spec *v1alpha1.DatabaseSpec) { spec.Backup.RetentionDays = days(90) })
	setSetting(t, w.c, "database.backup.retention-days-max", "30")
	w.reconcile()
	if w.reason() != ReasonRetentionOutOfRange || w.engineObjects() != 0 {
		t.Fatalf("reason %s objects %d", w.reason(), w.engineObjects())
	}
}
