package release

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
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

func TestPullLayoutWritesFullIndex(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	ref, err := name.ParseReference(host + "/lib/app:1.0")
	if err != nil {
		t.Fatal(err)
	}
	idx, err := random.Index(128, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir() + "/app.tar"
	digest, err := PullLayout(context.Background(), ref.String(), dest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := idx.Digest()
	if digest != want.String() {
		t.Fatalf("digest %s want %s", digest, want)
	}
	entries := strings.Join(tarEntries(t, dest), "\n")
	for _, required := range []string{"oci-layout", "index.json", "blobs/sha256/"} {
		if !strings.Contains(entries, required) {
			t.Fatalf("layout tar lacks %s: %s", required, entries)
		}
	}
	manifests, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range manifests.Manifests {
		if !strings.Contains(entries, "blobs/sha256/"+m.Digest.Hex) {
			t.Fatalf("platform manifest %s missing from layout", m.Digest)
		}
	}
}

func TestPullLayoutNamesTheIndex(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	ref, err := name.ParseReference(host + "/lib/named-index:1.0")
	if err != nil {
		t.Fatal(err)
	}
	idx, err := random.Index(128, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir() + "/named-index.tar"
	digest, err := PullLayout(context.Background(), ref.String(), dest, t.TempDir())
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
		if got := entry.Annotations[key]; got != ref.String() {
			t.Fatalf("annotation %s = %q want %q", key, got, ref.String())
		}
	}
	entries := strings.Join(tarEntries(t, dest), "\n")
	if !strings.Contains(entries, "blobs/sha256/"+entry.Digest.Hex) {
		t.Fatalf("nested index blob %s missing from layout", entry.Digest)
	}
	manifests, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range manifests.Manifests {
		if !strings.Contains(entries, "blobs/sha256/"+m.Digest.Hex) {
			t.Fatalf("platform manifest %s missing from layout", m.Digest)
		}
	}
}

func TestPullLayoutNamesAPinnedTagTwice(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	tagged, err := name.ParseReference(host + "/lib/pinned:1.0")
	if err != nil {
		t.Fatal(err)
	}
	idx, err := random.Index(128, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(tagged, idx); err != nil {
		t.Fatal(err)
	}
	digest, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	pinned := host + "/lib/pinned:1.0@" + digest.String()
	dest := t.TempDir() + "/pinned.tar"
	if _, err := PullLayout(context.Background(), pinned, dest, t.TempDir()); err != nil {
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
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir() + "/single.tar"
	digest, err := PullLayout(context.Background(), ref.String(), dest, t.TempDir())
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
}

func TestPullLayoutFailsForMissingImage(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	dest := t.TempDir() + "/missing.tar"
	if _, err := PullLayout(context.Background(), host+"/lib/missing:1", dest, t.TempDir()); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("no partial tar must remain")
	}
}
