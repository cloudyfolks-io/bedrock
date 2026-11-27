package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/backup/seal"
)

func rootWithHostKey(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if _, err := seal.EnsureHostIdentity(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestBackupRotateKeyPrintsTheNewRecipient(t *testing.T) {
	root := rootWithHostKey(t)
	var stdout, stderr bytes.Buffer
	if code := RunBackupRotateKey(nil, root, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	identities, err := seal.HostIdentities(root)
	if err != nil || len(identities) != 2 {
		t.Fatalf("identities %d err %v", len(identities), err)
	}
	if !strings.HasPrefix(stdout.String(), "age1") || strings.Contains(stdout.String(), "AGE-SECRET-KEY") {
		t.Fatalf("stdout %q", stdout.String())
	}
	retired, err := seal.RetiredHostKeys(root)
	if err != nil || len(retired) != 1 {
		t.Fatalf("retired %+v err %v", retired, err)
	}
}

func TestBackupRotateKeyFailsWithoutAHostKey(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := RunBackupRotateKey(nil, t.TempDir(), &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "host key") {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestBackupRotateKeyRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := RunBackupRotateKey([]string{"extra"}, rootWithHostKey(t), &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestBackupDecryptDefaultOpensAnArchiveOfARetiredHostKey(t *testing.T) {
	root := t.TempDir()
	old, err := seal.EnsureHostIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "etcd.tar.gz.age")
	sealTestFile(t, archive, []byte("before the rotation"), old)
	if _, err := seal.RotateHostIdentity(root, rotationNow()); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := RunBackupDecrypt([]string{"--in", archive, "--out", "-"}, root, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	if stdout.String() != "before the rotation" {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestBackupDecryptWithAnIdentityFileIgnoresRetiredKeys(t *testing.T) {
	root := t.TempDir()
	old, _ := seal.EnsureHostIdentity(root)
	archive := filepath.Join(t.TempDir(), "etcd.tar.gz.age")
	sealTestFile(t, archive, []byte("x"), old)
	if _, err := seal.RotateHostIdentity(root, rotationNow()); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	current := filepath.Join(root, seal.HostKeyFile)
	if code := RunBackupDecrypt([]string{"--in", archive, "--out", "-", "--identity", current}, root, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, seal.RetiredDir)); err != nil {
		t.Fatal(err)
	}
}

func rotationNow() time.Time {
	return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
}
