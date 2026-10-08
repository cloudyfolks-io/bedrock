package backup

import (
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSummarize(t *testing.T) {
	if got := Summarize(nil); got.Phase != "Pending" || len(got.Completed) != 0 {
		t.Fatalf("empty %+v", got)
	}
	if got := Summarize([]BackupInfo{{ID: "x", Phase: "InProgress"}}); got.Phase != "InProgress" {
		t.Fatalf("running %+v", got)
	}
	done := Summarize([]BackupInfo{completed("old", 30), completed("new", 5)})
	if done.Phase != "Completed" || !done.Last.Equal(fixedNow.Add(-5*time.Hour)) || done.Completed[0].ID != "new" || done.Completed[1].ID != "old" {
		t.Fatalf("done %+v", done)
	}
	failed := Summarize([]BackupInfo{completed("old", 30), {ID: "bad", Phase: "PartiallyFailed", Time: fixedNow}})
	if failed.Phase != "Failed" || len(failed.Completed) != 1 {
		t.Fatalf("failed %+v", failed)
	}
	var many []BackupInfo
	for i := 0; i < 60; i++ {
		many = append(many, completed(fmt.Sprintf("b%02d", i), i))
	}
	if got := Summarize(many); len(got.Completed) != 50 || got.Completed[0].ID != "b00" {
		t.Fatalf("cut %d", len(got.Completed))
	}
}

func TestInfoOfReadsTheBackup(t *testing.T) {
	b := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "velero.io/v1",
		"kind":       "Backup",
		"metadata":   map[string]any{"name": "vmb-daily-x-20261008020000"},
		"status":     map[string]any{"phase": "Completed", "completionTimestamp": "2026-10-08T02:05:00Z"},
	}}
	got := InfoOf(b, 42)
	want := BackupInfo{ID: "vmb-daily-x-20261008020000", Phase: "Completed", Time: time.Date(2026, 10, 8, 2, 5, 0, 0, time.UTC), Bytes: 42}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
