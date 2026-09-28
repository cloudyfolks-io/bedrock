package release

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"sigs.k8s.io/yaml"
)

func writeReleaseFixture(t *testing.T, dir string, images []string) {
	t.Helper()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "manifests", "00-crds"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "release.yaml"), []byte("version: v0.1.0\nimage: ghcr.io/cloudyfolks-io/bedrock:v0.1.0\nk0sVersion: v1.36.3+k0s.0\nk0sChecksums:\n  amd64: sha256:2b7bb4d64d416013eb5b4015dabe1d7cad590fd3ce7ce411f3a4489ae32f49b2\nbedrockChecksums:\n  amd64: sha256:2d7f45d7b98b427f824e0c643295583e9cf013faffdb5e7095d070ff85276bf4\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "images.txt"), []byte(strings.Join(images, "\n")+"\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "manifests", "00-crds", "a.yaml"), []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n  namespace: default\n"), 0o644))
}

func writeBedrockBinary(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bedrock-bin")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakePull(calls *[]string) func(context.Context, string, string) (string, error) {
	return func(_ context.Context, ref, dest string) (string, error) {
		*calls = append(*calls, ref)
		return "sha256:" + strings.Repeat("e", 64), os.WriteFile(dest, []byte("layout:"+ref), 0o644)
	}
}

func TestBuildBundleLaysOutEverything(t *testing.T) {
	releaseDir := t.TempDir()
	writeReleaseFixture(t, releaseDir, []string{"quay.io/a/b@sha256:" + strings.Repeat("1", 64), "ghcr.io/cloudyfolks-io/bedrock:v0.1.0"})
	k0s := filepath.Join(t.TempDir(), "k0s-bin")
	airgap := filepath.Join(t.TempDir(), "airgap.tar")
	os.WriteFile(k0s, []byte("k0s"), 0o755)
	os.WriteFile(airgap, []byte("airgap"), 0o644)
	var calls []string
	work := t.TempDir()
	spec, err := BuildBundle(context.Background(), BundleInputs{ReleaseDir: releaseDir, Arch: "amd64", K0sBinary: k0s, BedrockBinary: writeBedrockBinary(t, "bedrock"), K0sAirgap: airgap, Pull: fakePull(&calls)}, work)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != "v0.1.0" || spec.Arch != "amd64" || spec.K0sVersion != "v1.36.3+k0s.0" || spec.Image != "ghcr.io/cloudyfolks-io/bedrock:v0.1.0" || spec.ImageDigest == "" {
		t.Fatalf("spec %+v", spec)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 pulls, got %v", calls)
	}
	for _, path := range []string{"bundle.yaml", "release/release.yaml", "release/images.txt", "release/manifests/00-crds/a.yaml", "k0s/k0s", "bedrock/bedrock", "images/k0s-airgap.tar"} {
		if _, err := os.Stat(filepath.Join(work, path)); err != nil {
			t.Fatalf("missing %s", path)
		}
	}
	for _, image := range spec.Images {
		if _, err := os.Stat(filepath.Join(work, "images", image.File)); err != nil {
			t.Fatalf("missing image file %s", image.File)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(work, "bundle.yaml"))
	var reread BundleSpec
	if err := yaml.Unmarshal(raw, &reread); err != nil || len(reread.Images) != 2 {
		t.Fatalf("bundle.yaml %s err %v", raw, err)
	}
	for _, binary := range []string{"k0s/k0s", "bedrock/bedrock"} {
		info, _ := os.Stat(filepath.Join(work, binary))
		if info.Mode()&0o111 == 0 {
			t.Fatalf("%s must be executable", binary)
		}
	}
}

func TestBuildBundleRejectsBadK0sChecksum(t *testing.T) {
	releaseDir := t.TempDir()
	writeReleaseFixture(t, releaseDir, []string{"quay.io/a/b:1"})
	os.WriteFile(filepath.Join(releaseDir, "release.yaml"), []byte("version: v0.1.0\nimage: ghcr.io/cloudyfolks-io/bedrock:v0.1.0\nk0sVersion: v1\nk0sChecksums:\n  amd64: sha256:"+strings.Repeat("0", 64)+"\n"), 0o644)
	k0s := filepath.Join(t.TempDir(), "k0s-bin")
	os.WriteFile(k0s, []byte("k0s"), 0o755)
	airgap := filepath.Join(t.TempDir(), "airgap.tar")
	os.WriteFile(airgap, []byte("airgap"), 0o644)
	var calls []string
	if _, err := BuildBundle(context.Background(), BundleInputs{ReleaseDir: releaseDir, Arch: "amd64", K0sBinary: k0s, BedrockBinary: writeBedrockBinary(t, "bedrock"), K0sAirgap: airgap, Pull: fakePull(&calls)}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "k0s") {
		t.Fatalf("expected k0s checksum error, got %v", err)
	}
}

func TestBuildBundleRejectsBadBedrockChecksum(t *testing.T) {
	releaseDir := t.TempDir()
	writeReleaseFixture(t, releaseDir, []string{"quay.io/a/b:1"})
	k0s := filepath.Join(t.TempDir(), "k0s-bin")
	os.WriteFile(k0s, []byte("k0s"), 0o755)
	airgap := filepath.Join(t.TempDir(), "airgap.tar")
	os.WriteFile(airgap, []byte("airgap"), 0o644)
	var calls []string
	_, err := BuildBundle(context.Background(), BundleInputs{ReleaseDir: releaseDir, Arch: "amd64", K0sBinary: k0s, BedrockBinary: writeBedrockBinary(t, "tampered"), K0sAirgap: airgap, Pull: fakePull(&calls)}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "bedrock: checksum mismatch") {
		t.Fatalf("expected a bedrock checksum error, got %v", err)
	}
}

func TestPackAndOpenBundleRoundTrip(t *testing.T) {
	releaseDir := t.TempDir()
	writeReleaseFixture(t, releaseDir, []string{"quay.io/a/b:1"})
	k0s := filepath.Join(t.TempDir(), "k0s-bin")
	airgap := filepath.Join(t.TempDir(), "airgap.tar")
	os.WriteFile(k0s, []byte("k0s"), 0o755)
	os.WriteFile(airgap, []byte("airgap"), 0o644)
	sum := "sha256:" + fileSHA256OrFail(t, k0s)
	os.WriteFile(filepath.Join(releaseDir, "release.yaml"), []byte("version: v0.1.0\nimage: ghcr.io/cloudyfolks-io/bedrock:v0.1.0\nk0sVersion: v1\nk0sChecksums:\n  amd64: "+sum+"\nbedrockChecksums:\n  amd64: sha256:2d7f45d7b98b427f824e0c643295583e9cf013faffdb5e7095d070ff85276bf4\n"), 0o644)
	var calls []string
	work := t.TempDir()
	if _, err := BuildBundle(context.Background(), BundleInputs{ReleaseDir: releaseDir, Arch: "amd64", K0sBinary: k0s, BedrockBinary: writeBedrockBinary(t, "bedrock"), K0sAirgap: airgap, Pull: fakePull(&calls)}, work); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle.tar.zst")
	if err := PackBundle(work, out); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	spec, err := OpenBundle(out, dest)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != "v0.1.0" {
		t.Fatalf("spec %+v", spec)
	}
	got, _ := os.ReadFile(filepath.Join(dest, "k0s", "k0s"))
	if string(got) != "k0s" {
		t.Fatalf("k0s content %q", got)
	}
	if _, err := Load(os.DirFS(filepath.Join(dest, "release"))); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(dest, "images"))
	if len(entries) != 3 {
		t.Fatalf("expected 2 image archives plus the k0s airgap tar, got %d", len(entries))
	}
}

func TestOpenBundleRejectsEscapingPaths(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bundle.yaml"), []byte("version: v1\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "release"), 0o755)
	out := filepath.Join(t.TempDir(), "b.tar.zst")
	if err := PackBundle(dir, out); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(t.TempDir(), "evil.tar.zst")
	if err := writeEvilBundle(evil); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBundle(evil, t.TempDir()); err == nil {
		t.Fatal("expected error for ../ entry")
	}
}

type tarEntry struct {
	header *tar.Header
	body   []byte
}

func writeTarZst(t *testing.T, path string, entries []tarEntry) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zw, err := zstd.NewWriter(file)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for _, entry := range entries {
		entry.header.Size = int64(len(entry.body))
		if err := tw.WriteHeader(entry.header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractAllRejectsUnsafeEntries(t *testing.T) {
	unsafe := []struct {
		name  string
		entry tarEntry
	}{
		{"symlink", tarEntry{header: &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777}}},
		{"hardlink", tarEntry{header: &tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "target", Mode: 0o644}}},
		{"absolute path", tarEntry{header: &tar.Header{Name: "/etc/x", Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte("x")}},
	}
	for _, c := range unsafe {
		t.Run(c.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "evil.tar.zst")
			writeTarZst(t, archive, []tarEntry{c.entry})
			if _, err := OpenBundle(archive, t.TempDir()); err == nil {
				t.Fatalf("expected error for %s entry", c.name)
			}
		})
	}
	t.Run("setuid regular file", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "setuid.tar.zst")
		writeTarZst(t, archive, []tarEntry{
			{header: &tar.Header{Name: BundleFileName, Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte("version: v1\n")},
			{header: &tar.Header{Name: "setuid", Typeflag: tar.TypeReg, Mode: 0o4755}, body: []byte("x")},
		})
		dest := t.TempDir()
		if _, err := OpenBundle(archive, dest); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(dest, "setuid"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSetuid == 0 {
			return
		}
		t.Fatalf("setuid bit not stripped: %v", info.Mode())
	})
}

func fileSHA256OrFail(t *testing.T, path string) string {
	t.Helper()
	sum, err := FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(sum, "sha256:")
}

func writeEvilBundle(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	zw, err := zstd.NewWriter(file)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	body := []byte("x")
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: int64(len(body))}); err != nil {
		return err
	}
	if _, err := tw.Write(body); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}
