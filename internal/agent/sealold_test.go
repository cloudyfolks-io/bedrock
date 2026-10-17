package agent

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
	"github.com/cloudyfolks-io/bedrock/internal/host"
)

func backupDirOf(root string) string {
	return filepath.Join(root, backupDir)
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func openSealed(t *testing.T, path string, identity age.Identity) []byte {
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
	content, err := io.ReadAll(plain)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func sealedBytes(t *testing.T, content string, recipient age.Recipient) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := seal.Seal(&buffer, bytes.NewReader([]byte(content)), []age.Recipient{recipient}); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func setModTime(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestSealPlaintextArchivesSealsEveryArchive(t *testing.T) {
	root := t.TempDir()
	dir := backupDirOf(root)
	identity := newRecipient(t)
	writeFixtureFile(t, filepath.Join(dir, "bedrock-v0.1.0-20260901T100000Z.tar.gz"), "upgrade backup")
	writeFixtureFile(t, filepath.Join(dir, "etcd-20260902T100000Z.tar.gz"), "etcd snapshot")
	if err := sealPlaintextArchives(root, []age.Recipient{identity.Recipient()}); err != nil {
		t.Fatal(err)
	}
	want := []string{"bedrock-v0.1.0-20260901T100000Z.tar.gz.age", "etcd-20260902T100000Z.tar.gz.age"}
	if got := listNames(t, dir); !slices.Equal(got, want) {
		t.Fatalf("files %v, want %v", got, want)
	}
	if got := openSealed(t, filepath.Join(dir, want[0]), identity); string(got) != "upgrade backup" {
		t.Fatalf("upgrade backup opens to %q", got)
	}
	if got := openSealed(t, filepath.Join(dir, want[1]), identity); string(got) != "etcd snapshot" {
		t.Fatalf("etcd snapshot opens to %q", got)
	}
	info, err := os.Stat(filepath.Join(dir, want[0]))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, error %v", info.Mode(), err)
	}
}

func TestSealPlaintextArchivesRedoesAStalePartial(t *testing.T) {
	root := t.TempDir()
	dir := backupDirOf(root)
	identity := newRecipient(t)
	plaintext := filepath.Join(dir, "etcd-20260902T100000Z.tar.gz")
	writeFixtureFile(t, plaintext, "etcd snapshot")
	writeFixtureFile(t, plaintext+".age.partial", "half of a crashed write")
	if err := sealPlaintextArchives(root, []age.Recipient{identity.Recipient()}); err != nil {
		t.Fatal(err)
	}
	if got := listNames(t, dir); !slices.Equal(got, []string{"etcd-20260902T100000Z.tar.gz.age"}) {
		t.Fatalf("files %v", got)
	}
	if got := openSealed(t, plaintext+".age", identity); string(got) != "etcd snapshot" {
		t.Fatalf("archive opens to %q", got)
	}
}

func TestSealPlaintextArchivesFinishesACrashedRun(t *testing.T) {
	root := t.TempDir()
	dir := backupDirOf(root)
	identity := newRecipient(t)
	plaintext := filepath.Join(dir, "etcd-20260902T100000Z.tar.gz")
	sealed := sealedBytes(t, "etcd snapshot", identity.Recipient())
	writeFixtureFile(t, plaintext, "etcd snapshot")
	writeFixtureFile(t, plaintext+".age", string(sealed))
	if err := sealPlaintextArchives(root, nil); err != nil {
		t.Fatal(err)
	}
	if got := listNames(t, dir); !slices.Equal(got, []string{"etcd-20260902T100000Z.tar.gz.age"}) {
		t.Fatalf("files %v", got)
	}
	after, err := os.ReadFile(plaintext + ".age")
	if err != nil || !bytes.Equal(after, sealed) {
		t.Fatalf("the sealed archive changed, error %v", err)
	}
}

func TestSealPlaintextArchivesKeepsThePlaintextWhenSealingFails(t *testing.T) {
	root := t.TempDir()
	dir := backupDirOf(root)
	plaintext := filepath.Join(dir, "etcd-20260902T100000Z.tar.gz")
	writeFixtureFile(t, plaintext, "etcd snapshot")
	if err := sealPlaintextArchives(root, nil); err == nil {
		t.Fatal("sealing without a recipient must fail")
	}
	if got := listNames(t, dir); !slices.Equal(got, []string{"etcd-20260902T100000Z.tar.gz"}) {
		t.Fatalf("files %v", got)
	}
	if got := readFixtureFile(t, plaintext); got != "etcd snapshot" {
		t.Fatalf("plaintext %q", got)
	}
}

func TestSealPlaintextArchivesGoesOnAfterAFailedArchive(t *testing.T) {
	root := t.TempDir()
	dir := backupDirOf(root)
	identity := newRecipient(t)
	writeFixtureFile(t, filepath.Join(dir, "bedrock-v0.1.0-20260901T100000Z.tar.gz"), "upgrade backup")
	writeFixtureFile(t, filepath.Join(dir, "etcd-20260902T100000Z.tar.gz"), "etcd snapshot")
	if err := os.Mkdir(filepath.Join(dir, "bedrock-v0.1.0-20260901T100000Z.tar.gz.age.partial"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(dir, "bedrock-v0.1.0-20260901T100000Z.tar.gz.age.partial", "keep"), "a directory cannot be removed with Remove")
	if err := sealPlaintextArchives(root, []age.Recipient{identity.Recipient()}); err == nil {
		t.Fatal("the failed archive must be reported")
	}
	if _, err := os.Stat(filepath.Join(dir, "etcd-20260902T100000Z.tar.gz.age")); err != nil {
		t.Fatalf("the second archive must be sealed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "etcd-20260902T100000Z.tar.gz")); err == nil {
		t.Fatal("the second plaintext must be gone")
	}
}

func TestSealPlaintextArchivesLeavesOtherFilesAlone(t *testing.T) {
	root := t.TempDir()
	dir := backupDirOf(root)
	identity := newRecipient(t)
	untouched := map[string]string{
		".restore-abc/k0s_backup.tar.gz":                "restore work",
		".work-abc/k0s_backup.tar.gz":                   "backup work",
		"notes.txt":                                     "notes",
		"other-20260901T100000Z.tar.gz":                 "other archive",
		"bedrock-v0.1.0-20260801T100000Z.tar.gz.age":    "already sealed",
		".etcd-snapshot-failure":                        "1 1",
		"bedrock-v0.1.0-20260802T100000Z.tar.gz.sha256": "digest",
	}
	for name, content := range untouched {
		writeFixtureFile(t, filepath.Join(dir, name), content)
	}
	writeFixtureFile(t, filepath.Join(dir, "etcd-20260902T100000Z.tar.gz"), "etcd snapshot")
	if err := sealPlaintextArchives(root, []age.Recipient{identity.Recipient()}); err != nil {
		t.Fatal(err)
	}
	for name, content := range untouched {
		if got := readFixtureFile(t, filepath.Join(dir, name)); got != content {
			t.Fatalf("%s changed to %q", name, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "etcd-20260902T100000Z.tar.gz.age")); err != nil {
		t.Fatal(err)
	}
}

func TestSealPlaintextArchivesWithoutABackupDirectory(t *testing.T) {
	if err := sealPlaintextArchives(t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveStaleWorkDirsRemovesOnlyDirsBeforeTheCutoff(t *testing.T) {
	root := t.TempDir()
	dir := backupDirOf(root)
	cutoff := at("2026-10-08T01:30:00Z")
	for name, modified := range map[string]time.Time{
		".work-old":     cutoff.Add(-time.Minute),
		".restore-old":  cutoff.Add(-time.Hour),
		".work-live":    cutoff.Add(time.Minute),
		".restore-live": cutoff.Add(10 * time.Minute),
	} {
		writeFixtureFile(t, filepath.Join(dir, name, "k0s_backup.tar.gz"), "cluster keys")
		setModTime(t, filepath.Join(dir, name), modified)
	}
	writeFixtureFile(t, filepath.Join(dir, ".work-file"), "a file, not a work dir")
	writeFixtureFile(t, filepath.Join(dir, "etcd-20260902T100000Z.tar.gz.age"), "sealed")
	setModTime(t, filepath.Join(dir, ".work-file"), cutoff.Add(-time.Hour))
	if err := removeStaleWorkDirs(root, cutoff); err != nil {
		t.Fatal(err)
	}
	want := []string{".restore-live", ".work-file", ".work-live", "etcd-20260902T100000Z.tar.gz.age"}
	if got := listNames(t, dir); !slices.Equal(got, want) {
		t.Fatalf("files %v, want %v", got, want)
	}
}

func TestRemoveStaleWorkDirsWithoutABackupDirectory(t *testing.T) {
	if err := removeStaleWorkDirs(t.TempDir(), time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestTidyBackupDirKeepsTheWorkDirsOfARunningStep(t *testing.T) {
	deps := snapshotDeps(t, &fakeK0sBackup{}, at("2026-10-08T02:30:00Z"))
	dir := backupDirOf(deps.Root)
	for name, modified := range map[string]time.Time{
		".work-crashed":  deps.Now().Add(-2 * time.Hour),
		".restore-live":  deps.Now().Add(-10 * time.Minute),
		".work-starting": deps.Now(),
	} {
		writeFixtureFile(t, filepath.Join(dir, name, "k0s_backup.tar.gz"), "cluster keys")
		setModTime(t, filepath.Join(dir, name), modified)
	}
	if err := tidyBackupDir(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if got := listNames(t, dir); !slices.Equal(got, []string{".restore-live", ".work-starting"}) {
		t.Fatalf("files %v", got)
	}
}

func TestTidyBackupDirSealsToTheHostAndTheAdmins(t *testing.T) {
	admin := newRecipient(t)
	createSetting(t, archiveRecipientsKey, admin.Recipient().String())
	deps := snapshotDeps(t, &fakeK0sBackup{}, at("2026-10-08T02:30:00Z"))
	plaintext := filepath.Join(backupDirOf(deps.Root), "bedrock-v0.1.0-20260901T100000Z.tar.gz")
	writeFixtureFile(t, plaintext, "upgrade backup")
	if err := tidyBackupDir(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if got := openSealed(t, plaintext+".age", admin); string(got) != "upgrade backup" {
		t.Fatalf("admin opens %q", got)
	}
	if got := openSealed(t, plaintext+".age", hostIdentity(t, deps.Root)); string(got) != "upgrade backup" {
		t.Fatalf("host opens %q", got)
	}
	if _, err := os.Stat(plaintext); err == nil {
		t.Fatal("the plaintext must be gone")
	}
}

func TestTidyBackupDirCreatesNoHostKeyWithoutPlaintextArchives(t *testing.T) {
	deps := snapshotDeps(t, &fakeK0sBackup{}, at("2026-10-08T02:30:00Z"))
	writeFixtureFile(t, filepath.Join(backupDirOf(deps.Root), "etcd-20260902T100000Z.tar.gz.age"), "sealed")
	if err := tidyBackupDir(context.Background(), k8sClient, deps); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(deps.Root, seal.HostKeyFile)); err == nil {
		t.Fatal("a host without plaintext archives needs no host key")
	}
}

func TestRunSealsOlderArchivesAndRemovesStaleWorkDirsAtStart(t *testing.T) {
	watching, err := client.NewWithWatch(cfg, client.Options{Scheme: k8sClient.Scheme()})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := backupDirOf(root)
	plaintext := filepath.Join(dir, "etcd-20260902T100000Z.tar.gz")
	writeFixtureFile(t, plaintext, "etcd snapshot")
	writeFixtureFile(t, filepath.Join(dir, ".work-crashed", "k0s_backup.tar.gz"), "cluster keys")
	writeFixtureFile(t, filepath.Join(dir, ".restore-crashed", "k0s_backup.tar.gz"), "cluster keys")
	started := time.Now()
	for _, name := range []string{".work-crashed", ".restore-crashed"} {
		setModTime(t, filepath.Join(dir, name), started.Add(-time.Minute))
	}
	deps := newDeps(&host.FakeExec{}, started)
	deps.Root = root
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, watching, deps) }()
	stop := sync.OnceValue(func() error {
		cancel()
		return <-done
	})
	t.Cleanup(func() { _ = stop() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(plaintext + ".age"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the plaintext archive was never sealed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	want := []string{"etcd-20260902T100000Z.tar.gz.age"}
	if got := listNames(t, dir); !slices.Equal(got, want) {
		t.Fatalf("files %v, want %v", got, want)
	}
	if got := openSealed(t, plaintext+".age", hostIdentity(t, root)); string(got) != "etcd snapshot" {
		t.Fatalf("archive opens to %q", got)
	}
}
