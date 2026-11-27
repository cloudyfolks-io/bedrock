package release

import (
	"os"
	"path/filepath"
	"reflect"
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
	dir = writeTree(t, map[string]string{"release.yaml": "version: v1.0.0\nimage: " + ownImage + "\n"})
	if err := PinOwnImage(dir, "sha256:1234"); err == nil {
		t.Fatal("a short digest must be refused")
	}
}

func TestRunningImageMatchesThePublishedBundle(t *testing.T) {
	other := ownImage + "-other"
	workload := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: workload\nspec:\n  containers:\n    - name: own\n      image: " + ownImage + "\n    - name: other\n      image: " + other + "\n"
	dir := writeTree(t, map[string]string{
		"release.yaml":                  "version: v1.0.0\nimage: " + ownImage + "\ncomponents:\n- name: proxy\n  image: " + ownImage + "\n- name: other\n  image: " + other + "\nimages:\n  - " + ownImage + "\n  - " + other + "\n",
		"images.txt":                    ownImage + "\n" + other + "\n",
		"manifests/90-bedrock/pod.yaml": workload,
		"manifests/addons/app/pod.yaml": workload,
	})
	embedded, err := Load(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := WithRunningImage(embedded, ownImage+"@"+ownDigest)
	if err != nil {
		t.Fatal(err)
	}
	original, err := Load(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(embedded, original) {
		t.Fatal("the embedded bundle changed")
	}
	if err := PinOwnImage(dir, ownDigest); err != nil {
		t.Fatal(err)
	}
	published, err := Load(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pinned, published) {
		t.Fatalf("the running bundle differs from the published bundle: pinned image %s, published image %s", pinned.Spec.Image, published.Spec.Image)
	}
}

func TestRunningImageRefusesADifferentImageOrInvalidDigest(t *testing.T) {
	var bundle Bundle
	bundle.Spec.Image = ownImage
	for _, image := range []string{ownImage + "-other@" + ownDigest, ownImage + "@sha256:1234", ownImage + "@sha256:" + strings.Repeat("g", 64)} {
		if _, err := WithRunningImage(bundle, image); err == nil {
			t.Errorf("accepted %s", image)
		}
	}
	for _, image := range []string{ownImage, ownImage + "@" + ownDigest} {
		bundle.Spec.Image = image
		if got, err := WithRunningImage(bundle, image); err != nil || !reflect.DeepEqual(got, bundle) {
			t.Errorf("unchanged image %s returned %s, %v", image, got.Spec.Image, err)
		}
	}
}
