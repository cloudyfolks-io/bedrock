package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ownImage = "ghcr.io/cloudyfolks-io/bedrock:v1.0.0"
const ownDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readTree(t *testing.T, dir, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestPinOwnImageRewritesEveryReference(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"release.yaml":                 "version: v1.0.0\nimage: " + ownImage + "\nimages:\n  - " + ownImage + "\n  - quay.io/other/app:1\n",
		"images.txt":                   ownImage + "\nquay.io/other/app:1\n",
		"manifests/90-bedrock/op.yaml": "image: " + ownImage + "\nother: " + ownImage + "-suffix\n",
	})
	if err := PinOwnImage(dir, ownDigest); err != nil {
		t.Fatal(err)
	}
	pinned := ownImage + "@" + ownDigest
	for _, name := range []string{"release.yaml", "images.txt", "manifests/90-bedrock/op.yaml"} {
		if got := readTree(t, dir, name); !strings.Contains(got, pinned) || strings.Contains(got, ownImage+"\n") {
			t.Errorf("%s:\n%s", name, got)
		}
	}
	if got := readTree(t, dir, "manifests/90-bedrock/op.yaml"); !strings.Contains(got, ownImage+"-suffix") {
		t.Errorf("a longer reference must stay: %s", got)
	}
	if got := readTree(t, dir, "images.txt"); !strings.Contains(got, "quay.io/other/app:1\n") {
		t.Errorf("other images must stay: %s", got)
	}
}

func TestPinOwnImageRefusesAPinnedImageAndABadDigest(t *testing.T) {
	dir := writeTree(t, map[string]string{"release.yaml": "version: v1.0.0\nimage: " + ownImage + "@" + ownDigest + "\n"})
	if err := PinOwnImage(dir, ownDigest); err == nil {
		t.Fatal("a pinned image must be refused")
	}
	if err := PinOwnImage(dir, "latest"); err == nil {
		t.Fatal("a bad digest must be refused")
	}
}
