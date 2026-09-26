package release

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

type breakBlobOnce struct {
	next   http.RoundTripper
	broken *atomic.Bool
}

func (b breakBlobOnce) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := b.next.RoundTrip(req)
	if err != nil || !isBlobGet(req) || resp.StatusCode != http.StatusOK || b.broken.Swap(true) {
		return resp, err
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(io.LimitReader(resp.Body, 1), iotest.ErrReader(io.ErrUnexpectedEOF)), resp.Body}
	return resp, nil
}

func isBlobGet(req *http.Request) bool {
	return req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/blobs/")
}

func countBlobGets(next http.Handler, gets *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isBlobGet(r) {
			gets.Add(1)
		}
		next.ServeHTTP(w, r)
	})
}

func layoutFiles(t *testing.T, path string) map[string]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	files := map[string]string{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = string(content)
	}
}

func requireBlobsMatchNames(t *testing.T, files map[string]string) {
	t.Helper()
	for path, content := range files {
		hexName, ok := strings.CutPrefix(path, "blobs/sha256/")
		if !ok {
			continue
		}
		sum := sha256.Sum256([]byte(content))
		if hex.EncodeToString(sum[:]) != hexName {
			t.Fatalf("blob %s does not match its digest", path)
		}
	}
}

func pushIndex(t *testing.T, host, repository string) string {
	t.Helper()
	ref, err := name.ParseReference(host + "/" + repository)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := random.Index(256, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}
	return ref.String()
}

func TestPullLayoutReadsBlobsFromTheCache(t *testing.T) {
	var gets atomic.Int64
	server := httptest.NewServer(countBlobGets(registry.New(), &gets))
	defer server.Close()
	ref := pushIndex(t, strings.TrimPrefix(server.URL, "http://"), "lib/cached:1.0")
	cache := t.TempDir()
	first := filepath.Join(t.TempDir(), "first.tar")
	start := gets.Load()
	if _, err := PullLayout(context.Background(), ref, first, cache); err != nil {
		t.Fatal(err)
	}
	if gets.Load() == start {
		t.Fatal("the first pull made no blob requests")
	}
	second := filepath.Join(t.TempDir(), "second.tar")
	start = gets.Load()
	if _, err := PullLayout(context.Background(), ref, second, cache); err != nil {
		t.Fatal(err)
	}
	if got := gets.Load() - start; got != 0 {
		t.Fatalf("the second pull made %d blob GET requests, want 0", got)
	}
	firstFiles := layoutFiles(t, first)
	requireBlobsMatchNames(t, firstFiles)
	if !maps.Equal(firstFiles, layoutFiles(t, second)) {
		t.Fatal("the two layouts differ")
	}
}

func TestPullLayoutRetriesABrokenBlobBody(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	ref := pushIndex(t, strings.TrimPrefix(server.URL, "http://"), "lib/flaky:1.0")
	var broken atomic.Bool
	dest := filepath.Join(t.TempDir(), "flaky.tar")
	if _, err := pullLayout(context.Background(), breakBlobOnce{next: http.DefaultTransport, broken: &broken}, ref, dest, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if !broken.Load() {
		t.Fatal("no blob body was broken")
	}
	clean := filepath.Join(t.TempDir(), "clean.tar")
	if _, err := PullLayout(context.Background(), ref, clean, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	files := layoutFiles(t, dest)
	requireBlobsMatchNames(t, files)
	if !maps.Equal(files, layoutFiles(t, clean)) {
		t.Fatal("the layout after a retry differs from a clean pull")
	}
}

func TestPullLayoutReplacesACorruptCachedBlob(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	ref, err := name.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/lib/corrupt:1.0")
	if err != nil {
		t.Fatal(err)
	}
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := layers[0].Digest()
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := layers[0].Compressed()
	if err != nil {
		t.Fatal(err)
	}
	want, err := io.ReadAll(compressed)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	cached := filepath.Join(cache, "blobs", "sha256", digest.Hex)
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, bytes.Repeat([]byte("x"), len(want)), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "corrupt.tar")
	if _, err := PullLayout(context.Background(), ref.String(), dest, cache); err != nil {
		t.Fatal(err)
	}
	files := layoutFiles(t, dest)
	requireBlobsMatchNames(t, files)
	if files["blobs/sha256/"+digest.Hex] != string(want) {
		t.Fatal("the layout holds the wrong layer content")
	}
	got, err := os.ReadFile(cached)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("the corrupt cache file was not replaced")
	}
}
