package seal

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
)

var rotationTime = time.Date(2026, 10, 19, 12, 30, 45, 0, time.UTC)

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestRotateHostIdentityRetiresTheOldKey(t *testing.T) {
	root := t.TempDir()
	old, err := EnsureHostIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	next, err := RotateHostIdentity(root, rotationTime)
	if err != nil {
		t.Fatal(err)
	}
	if next.Recipient().String() == old.Recipient().String() {
		t.Fatal("the rotation must make a new key")
	}
	current, err := readIdentity(filepath.Join(root, HostKeyFile))
	if err != nil || current.Recipient().String() != next.Recipient().String() {
		t.Fatalf("host.key must hold the new key: %v", err)
	}
	retiredPath := filepath.Join(root, RetiredDir, "20261019T123045Z.key")
	retired, err := readIdentity(retiredPath)
	if err != nil || retired.Recipient().String() != old.Recipient().String() {
		t.Fatalf("retired key: %v", err)
	}
	for _, path := range []string{filepath.Join(root, HostKeyFile), retiredPath} {
		if mode := modeOf(t, path); mode != 0o600 {
			t.Errorf("%s mode %v", path, mode)
		}
	}
	if mode := modeOf(t, filepath.Join(root, RetiredDir)); mode != 0o700 {
		t.Errorf("retired dir mode %v", mode)
	}
}

func TestRotatedHostKeysOpenOldAndNewArchives(t *testing.T) {
	root := t.TempDir()
	old, _ := EnsureHostIdentity(root)
	oldArchive := sealBytes(t, []byte("old"), old.Recipient())
	next, err := RotateHostIdentity(root, rotationTime)
	if err != nil {
		t.Fatal(err)
	}
	newArchive := sealBytes(t, []byte("new"), next.Recipient())
	if _, err := openBytes(oldArchive, next); err == nil {
		t.Fatal("the new key must not open an old archive")
	}
	identities, err := HostIdentities(root)
	if err != nil || len(identities) != 2 {
		t.Fatalf("identities %d err %v", len(identities), err)
	}
	for name, archive := range map[string][]byte{"old": oldArchive, "new": newArchive} {
		reader, err := Open(bytes.NewReader(archive), identities)
		if err != nil {
			t.Fatalf("%s archive: %v", name, err)
		}
		plain := make([]byte, 3)
		if _, err := reader.Read(plain); err != nil || string(plain) != name {
			t.Fatalf("%s archive read %q err %v", name, plain, err)
		}
	}
}

func TestRotateHostIdentityNeedsAHostKey(t *testing.T) {
	root := t.TempDir()
	if _, err := RotateHostIdentity(root, rotationTime); err == nil {
		t.Fatal("rotation without a host key must fail")
	}
	if _, err := os.Stat(filepath.Join(root, HostKeyFile)); err == nil {
		t.Fatal("a failed rotation must not create a host key")
	}
}

func TestRotateHostIdentityRefusesTheSameSecond(t *testing.T) {
	root := t.TempDir()
	if _, err := EnsureHostIdentity(root); err != nil {
		t.Fatal(err)
	}
	first, err := RotateHostIdentity(root, rotationTime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RotateHostIdentity(root, rotationTime); err == nil {
		t.Fatal("a second rotation in the same second must fail")
	}
	current, err := readIdentity(filepath.Join(root, HostKeyFile))
	if err != nil || current.Recipient().String() != first.Recipient().String() {
		t.Fatalf("a failed rotation must keep host.key: %v", err)
	}
}

func TestHostIdentitiesListTheCurrentKeyFirstThenRetiredNewestFirst(t *testing.T) {
	root := t.TempDir()
	first, _ := EnsureHostIdentity(root)
	second, err := RotateHostIdentity(root, rotationTime)
	if err != nil {
		t.Fatal(err)
	}
	third, err := RotateHostIdentity(root, rotationTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	identities, err := HostIdentities(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []*age.X25519Identity{third, second, first}
	if len(identities) != 3 {
		t.Fatalf("identities %d", len(identities))
	}
	for index, identity := range identities {
		if identity.(*age.X25519Identity).Recipient().String() != want[index].Recipient().String() {
			t.Errorf("identity %d is out of order", index)
		}
	}
}

func TestRetiredHostKeysReadTheirTimeAndSkipStrangers(t *testing.T) {
	root := t.TempDir()
	if _, err := EnsureHostIdentity(root); err != nil {
		t.Fatal(err)
	}
	if _, err := RotateHostIdentity(root, rotationTime); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, RetiredDir)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "garbage.key"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := RetiredHostKeys(root)
	if err != nil || len(keys) != 1 || !keys[0].At.Equal(rotationTime) || filepath.Base(keys[0].Path) != "20261019T123045Z.key" {
		t.Fatalf("keys %+v err %v", keys, err)
	}
}

func TestRetiredHostKeysOfAHostWithoutRotationAreEmpty(t *testing.T) {
	keys, err := RetiredHostKeys(t.TempDir())
	if err != nil || len(keys) != 0 {
		t.Fatalf("keys %+v err %v", keys, err)
	}
}
