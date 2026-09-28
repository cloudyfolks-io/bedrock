package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestMirrorFiles(t *testing.T) {
	files := MirrorFiles("https://mirror.example.com")
	if !strings.Contains(files["cri-registry.toml"], `config_path = "/etc/k0s/containerd.d/certs.d"`) {
		t.Fatalf("cri-registry.toml %q", files["cri-registry.toml"])
	}
	hosts := files["certs.d/_default/hosts.toml"]
	if !strings.Contains(hosts, `[host."https://mirror.example.com"]`) || !strings.Contains(hosts, `capabilities = ["pull", "resolve"]`) {
		t.Fatalf("hosts.toml %q", hosts)
	}
	if strings.Contains(files["cri-registry.toml"], "version = 2") {
		t.Fatal("drop-in must not pin the v2 format")
	}
}

func TestEnsureMirrorWritesFiles(t *testing.T) {
	root := t.TempDir()
	if err := EnsureMirror(root, "https://m.example"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"etc/k0s/containerd.d/cri-registry.toml", "etc/k0s/containerd.d/certs.d/_default/hosts.toml"} {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("%s mode %o", rel, info.Mode().Perm())
		}
	}
	if err := EnsureMirror(root, "https://m.example"); err != nil {
		t.Fatal("second run must succeed")
	}
}

func TestMirrorFilesForWritesOnePerRegistry(t *testing.T) {
	files := MirrorFilesFor([]v1alpha1.MirrorSpec{{Registry: "_default", Endpoint: "https://m.example"}, {Registry: "quay.io", Endpoint: "https://q.example"}})
	if len(files) != 3 {
		t.Fatalf("files %v", files)
	}
	if !strings.Contains(files["certs.d/quay.io/hosts.toml"], `[host."https://q.example"]`) {
		t.Fatalf("quay hosts %q", files["certs.d/quay.io/hosts.toml"])
	}
	if MirrorFiles("https://m.example")["certs.d/_default/hosts.toml"] != files["certs.d/_default/hosts.toml"] {
		t.Fatal("single-mirror form must match the _default entry")
	}
}

func TestMirrorFilesForNoMirrors(t *testing.T) {
	if files := MirrorFilesFor(nil); len(files) != 0 {
		t.Fatalf("no mirrors must give no files, got %v", files)
	}
}

func TestEnsureMirrorsLeavesUnchangedFilesAlone(t *testing.T) {
	root := t.TempDir()
	mirrors := []v1alpha1.MirrorSpec{{Registry: "_default", Endpoint: "https://m.example"}}
	if err := EnsureMirrors(root, mirrors); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	paths := []string{filepath.Join(root, "etc/k0s/containerd.d/cri-registry.toml"), filepath.Join(root, "etc/k0s/containerd.d/certs.d/_default/hosts.toml")}
	for _, path := range paths {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := EnsureMirrors(root, mirrors); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(old) {
			t.Fatalf("%s was rewritten; k0s reloads containerd on every change in containerd.d", path)
		}
	}
}

func TestEnsureMirrorsRemovesStaleRegistries(t *testing.T) {
	root := t.TempDir()
	if err := EnsureMirrors(root, []v1alpha1.MirrorSpec{{Registry: "_default", Endpoint: "https://m.example"}, {Registry: "quay.io", Endpoint: "https://q.example"}}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureMirrors(root, []v1alpha1.MirrorSpec{{Registry: "_default", Endpoint: "https://m.example"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/k0s/containerd.d/certs.d/quay.io")); !os.IsNotExist(err) {
		t.Fatalf("a removed mirror must be deleted, stat error %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/k0s/containerd.d/certs.d/_default/hosts.toml")); err != nil {
		t.Fatalf("the kept mirror must stay: %v", err)
	}
}
