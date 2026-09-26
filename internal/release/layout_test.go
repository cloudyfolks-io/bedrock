package release

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func tarEntries(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
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

func indexManifestFromTar(t *testing.T, path string) v1.IndexManifest {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			t.Fatal("index.json not found in layout tar")
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != "index.json" {
			continue
		}
		var index v1.IndexManifest
		if err := json.NewDecoder(reader).Decode(&index); err != nil {
			t.Fatal(err)
		}
		return index
	}
}

var (
	linuxAMD64 = v1.Platform{OS: "linux", Architecture: "amd64"}
	linuxARM64 = v1.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}
)

func imageFor(t *testing.T, platform v1.Platform) v1.Image {
	t.Helper()
	img, err := random.Image(256, 2)
	if err != nil {
		t.Fatal(err)
	}
	config, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	config = config.DeepCopy()
	config.OS = platform.OS
	config.Architecture = platform.Architecture
	config.Variant = platform.Variant
	out, err := mutate.ConfigFile(img, config)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func indexOf(adds ...mutate.IndexAddendum) v1.ImageIndex {
	return mutate.AppendManifests(empty.Index, adds...)
}

func platformEntry(img v1.Image, platform v1.Platform) mutate.IndexAddendum {
	return mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &platform}}
}

func twoPlatformIndex(t *testing.T) v1.ImageIndex {
	t.Helper()
	return indexOf(platformEntry(imageFor(t, linuxAMD64), linuxAMD64), platformEntry(imageFor(t, linuxARM64), linuxARM64))
}

func imageBlobs(t *testing.T, img v1.Image) []string {
	t.Helper()
	digest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	config, err := img.ConfigName()
	if err != nil {
		t.Fatal(err)
	}
	blobs := []string{"blobs/sha256/" + digest.Hex, "blobs/sha256/" + config.Hex}
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for _, layer := range layers {
		layerDigest, err := layer.Digest()
		if err != nil {
			t.Fatal(err)
		}
		blobs = append(blobs, "blobs/sha256/"+layerDigest.Hex)
	}
	return blobs
}

func pushedRef(t *testing.T, repository string) (string, func(v1.ImageIndex)) {
	t.Helper()
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	ref, err := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/" + repository)
	if err != nil {
		t.Fatal(err)
	}
	return ref.String(), func(idx v1.ImageIndex) {
		if err := remote.WriteIndex(ref, idx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPullLayoutWritesTheIndexAndOnlyTheTargetPlatform(t *testing.T) {
	amd := imageFor(t, linuxAMD64)
	arm := imageFor(t, linuxARM64)
	idx := indexOf(platformEntry(amd, linuxAMD64), platformEntry(arm, linuxARM64))
	ref, push := pushedRef(t, "lib/app:1.0")
	push(idx)
	dest := filepath.Join(t.TempDir(), "app.tar")
	digest, err := PullLayout(context.Background(), ref, dest, t.TempDir(), "arm64")
	if err != nil {
		t.Fatal(err)
	}
	want, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digest != want.String() {
		t.Fatalf("digest %s want %s", digest, want)
	}
	written := indexManifestFromTar(t, dest)
	if len(written.Manifests) != 1 || written.Manifests[0].Digest != want || !written.Manifests[0].MediaType.IsIndex() {
		t.Fatalf("index.json %+v, want one entry for the index %s", written.Manifests, want)
	}
	for _, key := range []string{annotationRefName, annotationImageName} {
		if got := written.Manifests[0].Annotations[key]; got != ref {
			t.Fatalf("annotation %s = %q want %q", key, got, ref)
		}
	}
	files := layoutFiles(t, dest)
	requireBlobsMatchNames(t, files)
	raw, err := idx.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	if files["blobs/sha256/"+want.Hex] != string(raw) {
		t.Fatal("the index blob differs from the remote index")
	}
	for _, blob := range imageBlobs(t, arm) {
		if _, ok := files[blob]; !ok {
			t.Fatalf("arm64 blob %s missing from the layout", blob)
		}
	}
	for _, blob := range imageBlobs(t, amd) {
		if _, ok := files[blob]; ok {
			t.Fatalf("amd64 blob %s is in an arm64 layout", blob)
		}
	}
	wantFiles := slices.Sorted(slices.Values(append([]string{"oci-layout", "index.json", "blobs/sha256/" + want.Hex}, imageBlobs(t, arm)...)))
	if gotFiles := slices.Sorted(maps.Keys(files)); !slices.Equal(gotFiles, wantFiles) {
		t.Fatalf("layout files %v, want %v", gotFiles, wantFiles)
	}
}

func TestPullLayoutFailsWithoutTheTargetPlatform(t *testing.T) {
	ref, push := pushedRef(t, "lib/amdonly:1.0")
	windows := v1.Platform{OS: "windows", Architecture: "amd64"}
	push(indexOf(platformEntry(imageFor(t, linuxAMD64), linuxAMD64), platformEntry(imageFor(t, windows), windows)))
	dest := filepath.Join(t.TempDir(), "amdonly.tar")
	_, err := PullLayout(context.Background(), ref, dest, t.TempDir(), "arm64")
	if err == nil || !strings.Contains(err.Error(), ref) || !strings.Contains(err.Error(), "no linux/arm64 manifest") || !strings.Contains(err.Error(), "linux/amd64, windows/amd64") {
		t.Fatalf("error %v, want one that names %s, linux/arm64 and the platforms the index has", err, ref)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("no layout tar must remain")
	}
}

func TestPullLayoutRejectsAnImageOfAnotherPlatform(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	ref, err := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/lib/amd:1.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, imageFor(t, linuxAMD64)); err != nil {
		t.Fatal(err)
	}
	_, err = PullLayout(context.Background(), ref.String(), filepath.Join(t.TempDir(), "amd.tar"), t.TempDir(), "arm64")
	if err == nil || !strings.Contains(err.Error(), ref.String()) || !strings.Contains(err.Error(), "linux/amd64") || !strings.Contains(err.Error(), "linux/arm64") {
		t.Fatalf("error %v, want one that names %s, linux/amd64 and linux/arm64", err, ref)
	}
}

func TestPullLayoutRejectsANestedIndex(t *testing.T) {
	ref, push := pushedRef(t, "lib/nested:1.0")
	push(indexOf(mutate.IndexAddendum{Add: twoPlatformIndex(t)}))
	_, err := PullLayout(context.Background(), ref, filepath.Join(t.TempDir(), "nested.tar"), t.TempDir(), "arm64")
	if err == nil || !strings.Contains(err.Error(), ref) || !strings.Contains(err.Error(), "nested index") {
		t.Fatalf("error %v, want one that names %s and the nested index", err, ref)
	}
}

func TestPullLayoutNamesTheIndex(t *testing.T) {
	idx := indexOf(platformEntry(imageFor(t, linuxAMD64), linuxAMD64), platformEntry(imageFor(t, v1.Platform{OS: "linux", Architecture: "arm64"}), v1.Platform{OS: "linux", Architecture: "arm64"}))
	ref, push := pushedRef(t, "lib/named-index:1.0")
	push(idx)
	dest := filepath.Join(t.TempDir(), "named-index.tar")
	digest, err := PullLayout(context.Background(), ref, dest, t.TempDir(), "arm64")
	if err != nil {
		t.Fatal(err)
	}
	written := indexManifestFromTar(t, dest)
	if len(written.Manifests) != 1 {
		t.Fatalf("index.json has %d manifests, want 1", len(written.Manifests))
	}
	entry := written.Manifests[0]
	if !entry.MediaType.IsIndex() {
		t.Fatalf("entry media type %s is not an index", entry.MediaType)
	}
	if entry.Digest.String() != digest {
		t.Fatalf("entry digest %s want %s", entry.Digest, digest)
	}
	for _, key := range []string{annotationRefName, annotationImageName} {
		if got := entry.Annotations[key]; got != ref {
			t.Fatalf("annotation %s = %q want %q", key, got, ref)
		}
	}
	entries := strings.Join(tarEntries(t, dest), "\n")
	if !strings.Contains(entries, "blobs/sha256/"+entry.Digest.Hex) {
		t.Fatalf("index blob %s missing from layout", entry.Digest)
	}
}

func TestPullLayoutNamesAPinnedTagTwice(t *testing.T) {
	idx := twoPlatformIndex(t)
	tagged, push := pushedRef(t, "lib/pinned:1.0")
	push(idx)
	digest, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	host := strings.Split(tagged, "/")[0]
	pinned := host + "/lib/pinned:1.0@" + digest.String()
	dest := filepath.Join(t.TempDir(), "pinned.tar")
	if _, err := PullLayout(context.Background(), pinned, dest, t.TempDir(), "arm64"); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range indexManifestFromTar(t, dest).Manifests {
		if entry.Digest != digest {
			t.Fatalf("entry digest %s want %s", entry.Digest, digest)
		}
		names = append(names, entry.Annotations[annotationImageName])
	}
	want := []string{host + "/lib/pinned:1.0", host + "/lib/pinned@" + digest.String()}
	if !slices.Equal(names, want) {
		t.Fatalf("image names %v, want %v: runtime tag references need the tag name", names, want)
	}
}

func TestPullLayoutNamesTheSingleImage(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	ref, err := name.ParseReference(host + "/lib/single:1.0")
	if err != nil {
		t.Fatal(err)
	}
	img := imageFor(t, linuxARM64)
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir() + "/single.tar"
	digest, err := PullLayout(context.Background(), ref.String(), dest, t.TempDir(), "arm64")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := img.Digest()
	if digest != want.String() {
		t.Fatalf("digest %s want %s", digest, want)
	}
	written := indexManifestFromTar(t, dest)
	if len(written.Manifests) != 1 {
		t.Fatalf("index.json has %d manifests, want 1", len(written.Manifests))
	}
	entry := written.Manifests[0]
	if entry.Digest.String() != want.String() {
		t.Fatalf("entry digest %s want %s", entry.Digest, want)
	}
	for _, key := range []string{annotationRefName, annotationImageName} {
		if got := entry.Annotations[key]; got != ref.String() {
			t.Fatalf("annotation %s = %q want %q", key, got, ref.String())
		}
	}
	files := layoutFiles(t, dest)
	for _, blob := range imageBlobs(t, img) {
		if _, ok := files[blob]; !ok {
			t.Fatalf("blob %s missing from the layout", blob)
		}
	}
}

func TestPullLayoutFailsForMissingImage(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	dest := t.TempDir() + "/missing.tar"
	if _, err := PullLayout(context.Background(), host+"/lib/missing:1", dest, t.TempDir(), "arm64"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("no partial tar must remain")
	}
}
