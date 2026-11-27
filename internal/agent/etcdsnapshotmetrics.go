package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	textfileDir          = "var/lib/node_exporter/textfile_collector"
	etcdSnapshotTextfile = "bedrock_etcd_snapshot.prom"
)

func etcdSnapshotMetrics(newest time.Time, failedAttempts int) string {
	failing := 0
	if failedAttempts > 0 {
		failing = 1
	}
	var last int64
	if !newest.IsZero() {
		last = newest.Unix()
	}
	return fmt.Sprintf("bedrock_etcd_snapshot_last_success_timestamp_seconds %d\nbedrock_etcd_snapshot_failing %d\n", last, failing)
}

func writeEtcdSnapshotMetrics(root string) error {
	dir := filepath.Join(root, textfileDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_, attempts := readSnapshotFailure(filepath.Join(root, backupDir))
	content := etcdSnapshotMetrics(newestEtcdSnapshot(filepath.Join(root, backupDir)), attempts)
	temp := filepath.Join(dir, "."+etcdSnapshotTextfile+".tmp")
	if err := os.WriteFile(temp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(temp, filepath.Join(dir, etcdSnapshotTextfile))
}
