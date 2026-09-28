package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

type fakeK0sBackup struct {
	calls []string
	err   error
}

func (f *fakeK0sBackup) Run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	if f.err != nil {
		return "", f.err
	}
	if name == "/usr/local/bin/k0s" && len(args) == 3 && args[0] == "backup" && args[1] == "--save-path" {
		return "", os.WriteFile(filepath.Join(args[2], "k0s_backup_2026-10-01T10_00_00Z.tar.gz"), []byte("etcd snapshot"), 0o600)
	}
	return "", errors.New("unexpected command")
}

func backupEnv(t *testing.T, exec *fakeK0sBackup) StepEnv {
	t.Helper()
	deps := newDeps(nil, time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	deps.Exec = exec
	deps.Root = t.TempDir()
	return StepEnv{Deps: deps, Upgrade: v1alpha1.NodeUpgrade{Spec: v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v0.3.0", From: "v0.2.0", Attempt: 1}}}
}

func archiveEntries(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(gz)
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
}

func TestBackupArchivesK0sAndOVN(t *testing.T) {
	env := backupEnv(t, &fakeK0sBackup{})
	for _, name := range []string{"ovnnb_db.db", "ovnsb_db.db"} {
		writeFixtureFile(t, filepath.Join(env.Deps.Root, "etc/origin/ovn", name), name)
	}
	outcome, err := backup(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	hostPath := "/var/lib/bedrock/backups/bedrock-v0.2.0-20261001T100000Z.tar.gz"
	archive := filepath.Join(env.Deps.Root, hostPath)
	sum, err := release.FileSHA256(archive)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Message != hostPath+" "+sum {
		t.Fatalf("message %q, want %q", outcome.Message, hostPath+" "+sum)
	}
	entries := archiveEntries(t, archive)
	want := []string{"k0s_backup_2026-10-01T10_00_00Z.tar.gz", "ovn/ovnnb_db.db", "ovn/ovnsb_db.db"}
	if !slices.Equal(entries, want) {
		t.Fatalf("archive entries %v, want %v", entries, want)
	}
}

func TestBackupWithoutOVN(t *testing.T) {
	env := backupEnv(t, &fakeK0sBackup{})
	if _, err := backup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	entries := archiveEntries(t, filepath.Join(env.Deps.Root, "/var/lib/bedrock/backups/bedrock-v0.2.0-20261001T100000Z.tar.gz"))
	if !slices.Equal(entries, []string{"k0s_backup_2026-10-01T10_00_00Z.tar.gz"}) {
		t.Fatalf("entries %v", entries)
	}
}

func TestBackupKeepsTheNewestThree(t *testing.T) {
	env := backupEnv(t, &fakeK0sBackup{})
	dir := filepath.Join(env.Deps.Root, "var/lib/bedrock/backups")
	for _, stamp := range []string{"20260901T100000Z", "20260902T100000Z", "20260903T100000Z", "20260904T100000Z"} {
		writeFixtureFile(t, filepath.Join(dir, "bedrock-v0.1.9-"+stamp+".tar.gz"), "old")
	}
	writeFixtureFile(t, filepath.Join(dir, "notes.txt"), "not a backup")
	if _, err := backup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := []string{"bedrock-v0.1.9-20260903T100000Z.tar.gz", "bedrock-v0.1.9-20260904T100000Z.tar.gz", "bedrock-v0.2.0-20261001T100000Z.tar.gz", "notes.txt"}
	if !slices.Equal(names, want) {
		t.Fatalf("backups %v, want %v", names, want)
	}
}

func TestBackupFailsWhenK0sBackupFails(t *testing.T) {
	env := backupEnv(t, &fakeK0sBackup{err: errors.New("etcd unavailable")})
	if _, err := backup(context.Background(), env); err == nil || !strings.Contains(err.Error(), "etcd unavailable") {
		t.Fatalf("error %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(env.Deps.Root, "var/lib/bedrock/backups")); len(entries) != 0 {
		t.Fatalf("a failed backup must leave no archive or work dir: %v", entries)
	}
}

func TestBackupIsRegistered(t *testing.T) {
	if _, ok := Steps()[v1alpha1.StepBackup]; !ok {
		t.Fatal("Steps must include Backup")
	}
}
