package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"filippo.io/age"

	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
)

func newTestIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func sealTestFile(t *testing.T, path string, plain []byte, identity *age.X25519Identity) {
	t.Helper()
	var sealed bytes.Buffer
	if err := seal.Seal(&sealed, bytes.NewReader(plain), []age.Recipient{identity.Recipient()}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, sealed.Bytes())
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

func TestBackupKeygenPrintsNewIdentityEachRun(t *testing.T) {
	var first, second, errOut bytes.Buffer
	if code := Run([]string{"backup", "keygen"}, &first, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	Run([]string{"backup", "keygen"}, &second, &errOut)
	identities, err := age.ParseIdentities(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := identities[0].(*age.X25519Identity); !ok || len(identities) != 1 {
		t.Fatalf("identities %v", identities)
	}
	if bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("two keygen runs gave the same key")
	}
}

func TestBackupKeygenCommentHasMatchingRecipient(t *testing.T) {
	var out, errOut bytes.Buffer
	Run([]string{"backup", "keygen"}, &out, &errOut)
	text := out.String()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("output %q", text)
	}
	identities, err := age.ParseIdentities(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	identity := identities[0].(*age.X25519Identity)
	if lines[0] != "# public key: "+identity.Recipient().String() {
		t.Fatalf("comment line %q", lines[0])
	}
}

func TestBackupKeygenRejectsArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"backup", "keygen", "extra"}, &out, &errOut); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "usage: bedrock backup") {
		t.Fatalf("stdout %q, stderr %q", out.String(), errOut.String())
	}
}

func TestBackupUsage(t *testing.T) {
	for _, args := range [][]string{{"backup"}, {"backup", "nope"}} {
		var out, errOut bytes.Buffer
		if code := Run(args, &out, &errOut); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
		if !strings.Contains(errOut.String(), "backup") {
			t.Fatalf("%v: stderr %q", args, errOut.String())
		}
	}
}

func TestBackupDecryptWritesOutFile(t *testing.T) {
	dir := t.TempDir()
	identity := newTestIdentity(t)
	identityPath := filepath.Join(dir, "admin.key")
	writeTestFile(t, identityPath, seal.KeyFile(identity))
	archive := filepath.Join(dir, "etcd-20261017T000000Z.tar.gz.age")
	plain := bytes.Repeat([]byte("archive-bytes"), 20000)
	sealTestFile(t, archive, plain, identity)
	out := filepath.Join(dir, "etcd.tar.gz")
	var stdout, stderr bytes.Buffer
	code := RunBackupDecrypt([]string{"--in", archive, "--out", out, "--identity", identityPath}, "/nonexistent", &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("out file differs, err %v", err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout %q", stdout.String())
	}
	if names := listNames(t, dir); len(names) != 3 {
		t.Fatalf("files %v", names)
	}
}

func TestBackupDecryptOutDashWritesStdout(t *testing.T) {
	dir := t.TempDir()
	identity := newTestIdentity(t)
	identityPath := filepath.Join(dir, "admin.key")
	writeTestFile(t, identityPath, seal.KeyFile(identity))
	archive := filepath.Join(dir, "bedrock.tar.gz.age")
	sealTestFile(t, archive, []byte("plain archive"), identity)
	var stdout, stderr bytes.Buffer
	code := RunBackupDecrypt([]string{"--in", archive, "--out", "-", "--identity", identityPath}, "/", &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if stdout.String() != "plain archive" {
		t.Fatalf("stdout %q", stdout.String())
	}
	if names := listNames(t, dir); len(names) != 2 {
		t.Fatalf("files %v", names)
	}
}

func TestBackupDecryptDefaultsToHostKeyUnderRoot(t *testing.T) {
	root := t.TempDir()
	identity := newTestIdentity(t)
	writeTestFile(t, filepath.Join(root, seal.HostKeyFile), seal.KeyFile(identity))
	archive := filepath.Join(t.TempDir(), "etcd.tar.gz.age")
	sealTestFile(t, archive, []byte("host sealed"), identity)
	var stdout, stderr bytes.Buffer
	if code := RunBackupDecrypt([]string{"--in", archive, "--out", "-"}, root, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if stdout.String() != "host sealed" {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestBackupDecryptMissingHostKeyFails(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "etcd.tar.gz.age")
	sealTestFile(t, archive, []byte("x"), newTestIdentity(t))
	var stdout, stderr bytes.Buffer
	if code := RunBackupDecrypt([]string{"--in", archive, "--out", "-"}, t.TempDir(), &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(stderr.String(), "decrypt "+archive+": ") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestBackupDecryptWrongIdentityFailsAndKeepsOutFile(t *testing.T) {
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "other.key")
	writeTestFile(t, identityPath, seal.KeyFile(newTestIdentity(t)))
	archive := filepath.Join(dir, "etcd.tar.gz.age")
	sealTestFile(t, archive, []byte("secret"), newTestIdentity(t))
	out := filepath.Join(dir, "etcd.tar.gz")
	writeTestFile(t, out, []byte("older"))
	var stdout, stderr bytes.Buffer
	code := RunBackupDecrypt([]string{"--in", archive, "--out", out, "--identity", identityPath}, "/", &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(stderr.String(), "decrypt "+archive+": ") {
		t.Fatalf("stderr %q", stderr.String())
	}
	if got, _ := os.ReadFile(out); string(got) != "older" {
		t.Fatalf("out file %q", got)
	}
	if names := listNames(t, dir); len(names) != 3 {
		t.Fatalf("files %v", names)
	}
}

func TestBackupDecryptTruncatedArchiveLeavesNoOutput(t *testing.T) {
	dir := t.TempDir()
	identity := newTestIdentity(t)
	identityPath := filepath.Join(dir, "admin.key")
	writeTestFile(t, identityPath, seal.KeyFile(identity))
	archive := filepath.Join(dir, "etcd.tar.gz.age")
	sealTestFile(t, archive, bytes.Repeat([]byte("archive-bytes"), 20000), identity)
	sealed, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, archive, sealed[:len(sealed)-100])
	out := filepath.Join(dir, "etcd.tar.gz")
	var stdout, stderr bytes.Buffer
	code := RunBackupDecrypt([]string{"--in", archive, "--out", out, "--identity", identityPath}, "/", &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(stderr.String(), "decrypt "+archive+": ") {
		t.Fatalf("stderr %q", stderr.String())
	}
	if names := listNames(t, dir); len(names) != 2 {
		t.Fatalf("files %v", names)
	}
}

func TestBackupDecryptNeverEchoesIdentityContent(t *testing.T) {
	dir := t.TempDir()
	identityPath := filepath.Join(dir, "broken.key")
	writeTestFile(t, identityPath, []byte("AGE-SECRET-KEY-1QQQQSECRETMATERIALQQQQ\n"))
	archive := filepath.Join(dir, "etcd.tar.gz.age")
	sealTestFile(t, archive, []byte("x"), newTestIdentity(t))
	var stdout, stderr bytes.Buffer
	code := RunBackupDecrypt([]string{"--in", archive, "--out", "-", "--identity", identityPath}, "/", &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(stderr.String(), "SECRETMATERIAL") || !strings.HasPrefix(stderr.String(), "decrypt "+archive+": ") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

type decryptFixture struct {
	dir, archive, identityPath string
}

func newDecryptFixture(t *testing.T, plain []byte) decryptFixture {
	t.Helper()
	dir := t.TempDir()
	identity := newTestIdentity(t)
	fixture := decryptFixture{dir: dir, archive: filepath.Join(dir, "etcd.tar.gz.age"), identityPath: filepath.Join(dir, "admin.key")}
	writeTestFile(t, fixture.identityPath, seal.KeyFile(identity))
	sealTestFile(t, fixture.archive, plain, identity)
	return fixture
}

func runDecrypt(fixture decryptFixture, out string) (int, string) {
	var stdout, stderr bytes.Buffer
	code := RunBackupDecrypt([]string{"--in", fixture.archive, "--out", out, "--identity", fixture.identityPath}, "/", &stdout, &stderr)
	return code, stderr.String()
}

func TestBackupDecryptRefusesTheInputArchiveAsOutput(t *testing.T) {
	fixture := newDecryptFixture(t, []byte("plain archive"))
	before, err := os.ReadFile(fixture.archive)
	if err != nil {
		t.Fatal(err)
	}
	hardLink := filepath.Join(fixture.dir, "same.age")
	if err := os.Link(fixture.archive, hardLink); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{fixture.archive, hardLink} {
		code, stderr := runDecrypt(fixture, out)
		if code != 1 || stderr != "decrypt "+fixture.archive+": --out is the input archive\n" {
			t.Fatalf("%s: exit %d, stderr %q", out, code, stderr)
		}
		if after, err := os.ReadFile(fixture.archive); err != nil || !bytes.Equal(after, before) {
			t.Fatalf("%s: the archive changed, error %v", out, err)
		}
	}
}

func TestBackupDecryptRefusesAFifoAsOutput(t *testing.T) {
	fixture := newDecryptFixture(t, []byte("plain archive"))
	out := filepath.Join(fixture.dir, "out.fifo")
	if err := syscall.Mkfifo(out, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stderr := runDecrypt(fixture, out)
	if code != 1 || stderr != "decrypt "+fixture.archive+": --out "+out+" is not a regular file\n" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if info, err := os.Lstat(out); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("the fifo is gone: %v, error %v", info, err)
	}
}

func TestBackupDecryptRefusesASymlinkAsOutput(t *testing.T) {
	fixture := newDecryptFixture(t, []byte("plain archive"))
	target := filepath.Join(fixture.dir, "target.tar.gz")
	writeTestFile(t, target, []byte("target"))
	out := filepath.Join(fixture.dir, "link.tar.gz")
	if err := os.Symlink(target, out); err != nil {
		t.Fatal(err)
	}
	code, stderr := runDecrypt(fixture, out)
	if code != 1 || stderr != "decrypt "+fixture.archive+": --out "+out+" is not a regular file\n" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "target" {
		t.Fatalf("the target changed: %q, error %v", got, err)
	}
	if info, err := os.Lstat(out); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink is gone: %v, error %v", info, err)
	}
}

func TestBackupDecryptReplacesARegularOutputWithMode0600(t *testing.T) {
	fixture := newDecryptFixture(t, []byte("plain archive"))
	out := filepath.Join(fixture.dir, "etcd.tar.gz")
	if err := os.WriteFile(out, []byte("older"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(out, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, stderr := runDecrypt(fixture, out); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(out); err != nil || string(got) != "plain archive" {
		t.Fatalf("out file %q, error %v", got, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

func TestBackupDecryptTruncatedArchiveToStdoutFails(t *testing.T) {
	fixture := newDecryptFixture(t, bytes.Repeat([]byte("archive-bytes"), 20000))
	sealed, err := os.ReadFile(fixture.archive)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, fixture.archive, sealed[:len(sealed)-100])
	code, stderr := runDecrypt(fixture, "-")
	if code != 1 || !strings.HasPrefix(stderr, "decrypt "+fixture.archive+": ") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestBackupDecryptUsageErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"--in", "a.age"},
		{"--out", "-"},
		{"--in", "a.age", "--out", "-", "extra"},
		{"--nope"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := RunBackupDecrypt(args, "/", &stdout, &stderr); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
		if stdout.Len() != 0 {
			t.Fatalf("%v: stdout %q", args, stdout.String())
		}
	}
}

func TestBackupDecryptRegisteredInCommands(t *testing.T) {
	if _, ok := Commands()["backup"]; !ok {
		t.Fatal("Commands() must hold backup")
	}
}
