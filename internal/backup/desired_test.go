package backup

import (
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var fixedNow = time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)

func vmbOf(spec v1alpha1.VirtualMachineBackupSpec) v1alpha1.VirtualMachineBackup {
	return v1alpha1.VirtualMachineBackup{ObjectMeta: metav1.ObjectMeta{Name: "daily", Namespace: "acme-prod"}, Spec: spec}
}

func TestOneShotForUsesTheOneShotTTL(t *testing.T) {
	b, err := OneShotFor(vmbOf(v1alpha1.VirtualMachineBackupSpec{VMRef: "web", Target: "default"}), fixtureScope)
	if err != nil {
		t.Fatal(err)
	}
	spec := b.Object["spec"].(map[string]any)
	if b.GetKind() != "Backup" || spec["ttl"] != "720h0m0s" {
		t.Fatalf("backup %v", b.Object)
	}
}

func TestScheduleForBuildsTheSchedule(t *testing.T) {
	s, err := ScheduleFor(vmbOf(v1alpha1.VirtualMachineBackupSpec{VMRef: "web", Target: "default", Schedule: "0 2 * * *", Keep: 7}), fixtureScope, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	spec := s.Object["spec"].(map[string]any)
	template := spec["template"].(map[string]any)
	if s.GetKind() != "Schedule" || spec["schedule"] != "0 2 * * *" || template["ttl"] != "8760h0m0s" {
		t.Fatalf("schedule %v", s.Object)
	}
}

func TestScheduleForRefusesFrequentSchedules(t *testing.T) {
	for _, expr := range []string{"* * * * *", "*/5 * * * *", "*/59 * * * *"} {
		_, err := ScheduleFor(vmbOf(v1alpha1.VirtualMachineBackupSpec{VMRef: "web", Target: "default", Schedule: expr}), fixtureScope, fixedNow)
		if !errors.Is(err, ErrScheduleTooFrequent) {
			t.Fatalf("%q: got %v", expr, err)
		}
	}
	if _, err := ScheduleFor(vmbOf(v1alpha1.VirtualMachineBackupSpec{VMRef: "web", Target: "default", Schedule: "0 * * * *"}), fixtureScope, fixedNow); err != nil {
		t.Fatal(err)
	}
	for _, expr := range []string{"@daily", "0 0 2 * * *", ""} {
		_, err := ScheduleFor(vmbOf(v1alpha1.VirtualMachineBackupSpec{VMRef: "web", Target: "default", Schedule: expr}), fixtureScope, fixedNow)
		if !errors.Is(err, ErrScheduleInvalid) {
			t.Fatalf("%q: got %v", expr, err)
		}
	}
}

func TestForRefusesUnknownTargets(t *testing.T) {
	spec := v1alpha1.VirtualMachineBackupSpec{VMRef: "web", Target: "other", Schedule: "0 2 * * *"}
	if _, err := ScheduleFor(vmbOf(spec), fixtureScope, fixedNow); !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("schedule: %v", err)
	}
	if _, err := OneShotFor(vmbOf(spec), fixtureScope); !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("one shot: %v", err)
	}
	for _, name := range []string{"", "default"} {
		spec.Target = name
		if _, err := OneShotFor(vmbOf(spec), fixtureScope); err != nil {
			t.Fatalf("%q: %v", name, err)
		}
	}
}

func TestForRefusesBadKeep(t *testing.T) {
	for _, keep := range []int32{-1, 366} {
		spec := v1alpha1.VirtualMachineBackupSpec{VMRef: "web", Target: "default", Schedule: "0 2 * * *", Keep: keep}
		if _, err := ScheduleFor(vmbOf(spec), fixtureScope, fixedNow); !errors.Is(err, ErrKeepInvalid) {
			t.Fatalf("schedule keep %d: %v", keep, err)
		}
		if _, err := OneShotFor(vmbOf(spec), fixtureScope); !errors.Is(err, ErrKeepInvalid) {
			t.Fatalf("one shot keep %d: %v", keep, err)
		}
	}
	if got := KeepOf(vmbOf(v1alpha1.VirtualMachineBackupSpec{})); got != DefaultKeep {
		t.Fatalf("default keep %d", got)
	}
	if got := KeepOf(vmbOf(v1alpha1.VirtualMachineBackupSpec{Keep: 3})); got != 3 {
		t.Fatalf("keep %d", got)
	}
}

func completed(id string, hoursAgo int) BackupInfo {
	return BackupInfo{ID: id, Phase: "Completed", Time: fixedNow.Add(-time.Duration(hoursAgo) * time.Hour)}
}

func TestPruneKeepsTheNewestCompleted(t *testing.T) {
	var infos []BackupInfo
	for i := 0; i < 10; i++ {
		infos = append(infos, completed("b"+string(rune('a'+i)), i))
	}
	infos = append(infos, BackupInfo{ID: "failed-old", Phase: "Failed", Time: fixedNow.Add(-100 * time.Hour)}, BackupInfo{ID: "running", Phase: "InProgress"})
	got := Prune(infos, 7)
	want := []string{"bj", "bi", "bh"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v want %v", got, want)
	}
	if len(Prune(infos, 20)) != 0 {
		t.Fatal("nothing to prune below keep")
	}
}

func TestPruneBreaksTiesByID(t *testing.T) {
	infos := []BackupInfo{completed("b", 1), completed("a", 1), completed("c", 1)}
	got := Prune(infos, 1)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v", got)
	}
}
