package host

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureDeviceOwnershipWritesTheDropIn(t *testing.T) {
	root := t.TempDir()
	if err := EnsureDeviceOwnership(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "etc/k0s/containerd.d/cri-devices.toml")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "[plugins.\"io.containerd.cri.v1.runtime\"]\ndevice_ownership_from_security_context = true\n" {
		t.Fatalf("cri-devices.toml %q", got)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDeviceOwnership(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Fatal("an unchanged drop-in was rewritten; k0s reloads containerd on every change in containerd.d")
	}
}
