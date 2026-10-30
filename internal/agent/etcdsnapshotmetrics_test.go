package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEtcdSnapshotMetricsReportTheNewestSnapshotAndFailures(t *testing.T) {
	newest := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	want := "bedrock_etcd_snapshot_last_success_timestamp_seconds 1793325600\nbedrock_etcd_snapshot_failing 1\n"
	if got := etcdSnapshotMetrics(newest, 2); got != want {
		t.Fatalf("got %q", got)
	}
	if got := etcdSnapshotMetrics(time.Time{}, 0); got != "bedrock_etcd_snapshot_last_success_timestamp_seconds 0\nbedrock_etcd_snapshot_failing 0\n" {
		t.Fatalf("got %q", got)
	}
}

func TestEtcdSnapshotMetricsFileIsWrittenForNodeExporter(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, backupDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, etcdSnapshotPrefix+"20261030T020000Z"+sealedSuffix), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeEtcdSnapshotMetrics(root); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, textfileDir, etcdSnapshotTextfile))
	if err != nil {
		t.Fatal(err)
	}
	if want := etcdSnapshotMetrics(time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC), 0); string(raw) != want {
		t.Fatalf("got %q", raw)
	}
}
