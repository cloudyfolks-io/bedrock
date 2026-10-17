package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
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
	return StepEnv{Deps: deps, Client: k8sClient, Upgrade: v1alpha1.NodeUpgrade{Spec: v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v0.3.0", From: "v0.2.0", Attempt: 1}}}
}

func hostIdentity(t *testing.T, root string) *age.X25519Identity {
	t.Helper()
	identity, err := seal.EnsureHostIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func archiveEntries(t *testing.T, path string, identity age.Identity) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	plain, err := seal.Open(file, []age.Identity{identity})
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(plain)
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
	hostPath := "/var/lib/bedrock/backups/bedrock-v0.2.0-20261001T100000Z.tar.gz.age"
	archive := filepath.Join(env.Deps.Root, hostPath)
	sealed, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(sealed)
	want := hostPath + " sha256:" + hex.EncodeToString(digest[:])
	if outcome.Message != want {
		t.Fatalf("message %q, want %q", outcome.Message, want)
	}
	entries := archiveEntries(t, archive, hostIdentity(t, env.Deps.Root))
	wantEntries := []string{"k0s_backup_2026-10-01T10_00_00Z.tar.gz", "ovn/ovnnb_db.db", "ovn/ovnsb_db.db"}
	if !slices.Equal(entries, wantEntries) {
		t.Fatalf("archive entries %v, want %v", entries, wantEntries)
	}
}

func TestBackupWritesOnlyTheSealedFile(t *testing.T) {
	env := backupEnv(t, &fakeK0sBackup{})
	if _, err := backup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(env.Deps.Root, backupDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "bedrock-v0.2.0-20261001T100000Z.tar.gz.age" {
		t.Fatalf("backup dir %v", entries)
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "age-encryption.org/v1\n") {
		t.Fatalf("the archive is not an age file: %q", raw[:min(len(raw), 24)])
	}
	if info, err := entries[0].Info(); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, error %v", info.Mode(), err)
	}
}

func TestBackupSealsToAdminRecipients(t *testing.T) {
	admin := newRecipient(t)
	createSetting(t, archiveRecipientsKey, admin.Recipient().String())
	env := backupEnv(t, &fakeK0sBackup{})
	if _, err := backup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(env.Deps.Root, backupDir, "bedrock-v0.2.0-20261001T100000Z.tar.gz.age")
	want := []string{"k0s_backup_2026-10-01T10_00_00Z.tar.gz"}
	if got := archiveEntries(t, archive, admin); !slices.Equal(got, want) {
		t.Fatalf("admin entries %v, want %v", got, want)
	}
	if got := archiveEntries(t, archive, hostIdentity(t, env.Deps.Root)); !slices.Equal(got, want) {
		t.Fatalf("host entries %v, want %v", got, want)
	}
}

func TestBackupWithAnInvalidRecipientStaysOpenToTheHost(t *testing.T) {
	createSetting(t, archiveRecipientsKey, "ssh-ed25519 AAAA")
	env := backupEnv(t, &fakeK0sBackup{})
	if _, err := backup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(env.Deps.Root, backupDir, "bedrock-v0.2.0-20261001T100000Z.tar.gz.age")
	if got := archiveEntries(t, archive, hostIdentity(t, env.Deps.Root)); len(got) != 1 {
		t.Fatalf("entries %v", got)
	}
}

func TestBackupFailsWithoutAUsableHostKeyDirectory(t *testing.T) {
	env := backupEnv(t, &fakeK0sBackup{})
	writeFixtureFile(t, filepath.Join(env.Deps.Root, "etc"), "a file where the key directory must go")
	if _, err := backup(context.Background(), env); err == nil {
		t.Fatal("a backup without a host key must fail")
	}
	if entries, _ := os.ReadDir(filepath.Join(env.Deps.Root, backupDir)); len(entries) != 0 {
		t.Fatalf("a failed backup must leave no archive: %v", entries)
	}
}

func TestBackupWithoutOVN(t *testing.T) {
	env := backupEnv(t, &fakeK0sBackup{})
	if _, err := backup(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	entries := archiveEntries(t, filepath.Join(env.Deps.Root, "/var/lib/bedrock/backups/bedrock-v0.2.0-20261001T100000Z.tar.gz.age"), hostIdentity(t, env.Deps.Root))
	if !slices.Equal(entries, []string{"k0s_backup_2026-10-01T10_00_00Z.tar.gz"}) {
		t.Fatalf("entries %v", entries)
	}
}

func TestBackupKeepsTheNewestThree(t *testing.T) {
	env := backupEnv(t, &fakeK0sBackup{})
	dir := filepath.Join(env.Deps.Root, "var/lib/bedrock/backups")
	for stamp, suffix := range map[string]string{"20260901T100000Z": ".tar.gz", "20260902T100000Z": ".tar.gz.age", "20260903T100000Z": ".tar.gz", "20260904T100000Z": ".tar.gz.age"} {
		writeFixtureFile(t, filepath.Join(dir, "bedrock-v0.1.9-"+stamp+suffix), "old")
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
	want := []string{"bedrock-v0.1.9-20260903T100000Z.tar.gz", "bedrock-v0.1.9-20260904T100000Z.tar.gz.age", "bedrock-v0.2.0-20261001T100000Z.tar.gz.age", "notes.txt"}
	if !slices.Equal(names, want) {
		t.Fatalf("backups %v, want %v", names, want)
	}
}

func writeArchiveFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		writeFixtureFile(t, filepath.Join(dir, name), "archive")
	}
}

func TestRemoveFilesSkipsAFileThatIsAlreadyGone(t *testing.T) {
	dir := t.TempDir()
	writeArchiveFiles(t, dir, "etcd-20260901T100000Z.tar.gz", "etcd-20260902T100000Z.tar.gz")
	paths := []string{
		filepath.Join(dir, "etcd-20260901T100000Z.tar.gz"),
		filepath.Join(dir, "etcd-20260901T100000Z.tar.gz.age"),
		filepath.Join(dir, "etcd-20260902T100000Z.tar.gz"),
	}
	if err := removeFiles(paths); err != nil {
		t.Fatal(err)
	}
	if got := listNames(t, dir); len(got) != 0 {
		t.Fatalf("files %v", got)
	}
}

func TestKeepNewestCountsASealedAndAPlainCopyOnce(t *testing.T) {
	dir := t.TempDir()
	writeArchiveFiles(t, dir,
		"bedrock-v0.1.9-20260901T100000Z.tar.gz.age",
		"bedrock-v0.1.9-20260902T100000Z.tar.gz",
		"bedrock-v0.1.9-20260902T100000Z.tar.gz.age",
		"bedrock-v0.1.9-20260903T100000Z.tar.gz.age",
		"bedrock-v0.1.9-20260904T100000Z.tar.gz.age",
	)
	if err := keepNewest(dir, upgradeBackupPrefix, 3); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bedrock-v0.1.9-20260902T100000Z.tar.gz",
		"bedrock-v0.1.9-20260902T100000Z.tar.gz.age",
		"bedrock-v0.1.9-20260903T100000Z.tar.gz.age",
		"bedrock-v0.1.9-20260904T100000Z.tar.gz.age",
	}
	if got := listNames(t, dir); !slices.Equal(got, want) {
		t.Fatalf("files %v, want %v", got, want)
	}
}

func TestKeepNewestRemovesEveryFileOfAnOldStamp(t *testing.T) {
	dir := t.TempDir()
	writeArchiveFiles(t, dir,
		"etcd-20260901T100000Z.tar.gz",
		"etcd-20260901T100000Z.tar.gz.age",
		"etcd-20260902T100000Z.tar.gz",
		"etcd-20260902T100000Z.tar.gz.age",
		"etcd-20260903T100000Z.tar.gz.age",
	)
	if err := keepNewest(dir, etcdSnapshotPrefix, 1); err != nil {
		t.Fatal(err)
	}
	if got := listNames(t, dir); !slices.Equal(got, []string{"etcd-20260903T100000Z.tar.gz.age"}) {
		t.Fatalf("files %v", got)
	}
}

func TestKeepNewestRemovesTheLowerNameOfTheSameStamp(t *testing.T) {
	dir := t.TempDir()
	writeArchiveFiles(t, dir,
		"bedrock-v0.2.0-20260901T100000Z.tar.gz.age",
		"bedrock-v0.1.0-20260901T100000Z.tar.gz.age",
		"bedrock-v0.3.0-20260901T100000Z.tar.gz",
	)
	if err := keepNewest(dir, upgradeBackupPrefix, 2); err != nil {
		t.Fatal(err)
	}
	want := []string{"bedrock-v0.2.0-20260901T100000Z.tar.gz.age", "bedrock-v0.3.0-20260901T100000Z.tar.gz"}
	if got := listNames(t, dir); !slices.Equal(got, want) {
		t.Fatalf("files %v, want %v", got, want)
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

type failingWriter struct {
	dst   io.Writer
	limit int
	err   error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.limit <= 0 {
		return 0, w.err
	}
	n := min(len(p), w.limit)
	written, err := w.dst.Write(p[:n])
	w.limit -= written
	if err != nil {
		return written, err
	}
	if n < len(p) {
		return written, w.err
	}
	return written, nil
}

type brokenWorkBackup struct {
	fakeK0sBackup
	damage func(work string) error
}

func (b *brokenWorkBackup) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := b.fakeK0sBackup.Run(ctx, name, args...)
	if err != nil {
		return out, err
	}
	return out, b.damage(args[2])
}

func requireNoGoroutineLeak(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("%d goroutines before, %d after", before, after)
	}
}

func TestBackupLeavesNoFileWhenTheArchiveCannotBePacked(t *testing.T) {
	damages := map[string]func(work string) error{
		"dangling link": func(work string) error {
			return os.Symlink("missing", filepath.Join(work, "dangling"))
		},
		"unreadable file": func(work string) error {
			path := filepath.Join(work, "unreadable")
			return errors.Join(os.WriteFile(path, []byte("secret"), 0o600), os.Chmod(path, 0))
		},
	}
	for name, damage := range damages {
		t.Run(name, func(t *testing.T) {
			if name == "unreadable file" && os.Geteuid() == 0 {
				t.Skip("root reads every file")
			}
			env := backupEnv(t, &fakeK0sBackup{})
			env.Deps.Exec = &brokenWorkBackup{damage: damage}
			before := runtime.NumGoroutine()
			if _, err := backup(context.Background(), env); err == nil {
				t.Fatal("a backup that cannot pack a file must fail")
			}
			if entries, _ := os.ReadDir(filepath.Join(env.Deps.Root, backupDir)); len(entries) != 0 {
				t.Fatalf("a failed backup must leave no archive, partial file or work dir: %v", entries)
			}
			requireNoGoroutineLeak(t, before)
		})
	}
}

func TestSealTarGzStopsWhenTheWriterFails(t *testing.T) {
	failure := errors.New("disk full")
	work := restoreWork(t, "ovn/ovnsb_db.db")
	before := runtime.NumGoroutine()
	err := sealTarGz(&failingWriter{dst: io.Discard, limit: 70 << 10, err: failure}, work, []age.Recipient{newRecipient(t).Recipient()})
	if !errors.Is(err, failure) {
		t.Fatalf("error %v, want %v", err, failure)
	}
	requireNoGoroutineLeak(t, before)
}

func TestSealTarGzStopsWhenAFileCannotBePacked(t *testing.T) {
	work := restoreWork(t, "ovn/ovnsb_db.db")
	if err := os.Symlink("missing", filepath.Join(work, "dangling")); err != nil {
		t.Fatal(err)
	}
	before := runtime.NumGoroutine()
	if err := sealTarGz(io.Discard, work, []age.Recipient{newRecipient(t).Recipient()}); err == nil {
		t.Fatal("a dangling link must fail the archive")
	}
	requireNoGoroutineLeak(t, before)
}

func TestWriteAtomicLeavesNoFileWhenTheWriterFails(t *testing.T) {
	failure := errors.New("disk full")
	dir := t.TempDir()
	work := restoreWork(t, "ovn/ovnsb_db.db")
	recipients := []age.Recipient{newRecipient(t).Recipient()}
	before := runtime.NumGoroutine()
	err := writeAtomic(filepath.Join(dir, "etcd-20261001T100000Z.tar.gz.age"), func(dst io.Writer) error {
		return sealTarGz(&failingWriter{dst: dst, limit: 70 << 10, err: failure}, work, recipients)
	})
	if !errors.Is(err, failure) {
		t.Fatalf("error %v, want %v", err, failure)
	}
	if got := listNames(t, dir); len(got) != 0 {
		t.Fatalf("a failed write must leave no .age or .partial file: %v", got)
	}
	requireNoGoroutineLeak(t, before)
}

func TestBackupIsRegistered(t *testing.T) {
	if _, ok := Steps()[v1alpha1.StepBackup]; !ok {
		t.Fatal("Steps must include Backup")
	}
}
