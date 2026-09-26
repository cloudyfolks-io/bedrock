package release

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

type cdnHit struct {
	hex  string
	rng  string
	auth string
}

type serveBlob func(w http.ResponseWriter, r *http.Request, hex string, blob []byte, hit int)

type breakBodies struct {
	next   http.RoundTripper
	hex    string
	after  int64
	breaks *atomic.Int64
}

func (b breakBodies) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := b.next.RoundTrip(req)
	if err != nil || !strings.HasSuffix(req.URL.Path, b.hex) || resp.StatusCode >= http.StatusMultipleChoices || b.breaks.Add(-1) < 0 {
		return resp, err
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(io.LimitReader(resp.Body, b.after), iotest.ErrReader(io.ErrUnexpectedEOF)), resp.Body}
	return resp, nil
}

func serveRanges(w http.ResponseWriter, r *http.Request, _ string, blob []byte, _ int) {
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(blob))
}

func startCDN(t *testing.T, serve serveBlob) (string, func(hex string) []cdnHit) {
	t.Helper()
	reg := registry.New()
	var mu sync.Mutex
	var hits []cdnHit
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/cdn")
		hex := path[strings.LastIndex(path, ":")+1:]
		mu.Lock()
		hit := len(slices.DeleteFunc(slices.Clone(hits), func(h cdnHit) bool { return h.hex != hex }))
		hits = append(hits, cdnHit{hex: hex, rng: r.Header.Get("Range"), auth: r.Header.Get("Authorization")})
		mu.Unlock()
		stored := httptest.NewRecorder()
		reg.ServeHTTP(stored, httptest.NewRequest(http.MethodGet, path, nil))
		serve(w, r, hex, stored.Body.Bytes(), hit)
	}))
	t.Cleanup(cdn.Close)
	cdnURL := "http://localhost:" + cdn.URL[strings.LastIndex(cdn.URL, ":")+1:]
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && blobPath.MatchString(r.URL.Path) {
			http.Redirect(w, r, cdnURL+"/cdn"+r.URL.Path, http.StatusTemporaryRedirect)
			return
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(origin.Close)
	return strings.TrimPrefix(origin.URL, "http://"), func(hex string) []cdnHit {
		mu.Lock()
		defer mu.Unlock()
		return slices.DeleteFunc(slices.Clone(hits), func(h cdnHit) bool { return h.hex != hex })
	}
}

func pushLayer(t *testing.T, host, repository string) (string, string, []byte) {
	t.Helper()
	ref, err := name.ParseReference(host + "/" + repository)
	if err != nil {
		t.Fatal(err)
	}
	img, err := random.Image(4096, 1)
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
	return ref.String(), digest.Hex, want
}

func writePartial(t *testing.T, dir, hex string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, "blobs", "sha256", hex+".partial")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func ranges(hits []cdnHit) []string {
	out := make([]string, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.rng)
	}
	return out
}

func testCache(dir string, next http.RoundTripper) blobCache {
	return blobCache{dir: dir, next: next, idle: 200 * time.Millisecond, pause: time.Millisecond}
}

func pullLayer(t *testing.T, cache blobCache, ref, hex string) []byte {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "layer.tar")
	if _, err := pullLayout(context.Background(), cache, ref, dest, linuxARM64); err != nil {
		t.Fatal(err)
	}
	files := layoutFiles(t, dest)
	requireBlobsMatchNames(t, files)
	return []byte(files["blobs/sha256/"+hex])
}

func TestBlobCacheResumesABodyThatBreaksAgain(t *testing.T) {
	host, hits := startCDN(t, serveRanges)
	ref, hex, want := pushLayer(t, host, "lib/breaking:1.0")
	var breaks atomic.Int64
	breaks.Store(5)
	got := pullLayer(t, testCache(t.TempDir(), breakBodies{next: http.DefaultTransport, hex: hex, after: 100, breaks: &breaks}), ref, hex)
	if !bytes.Equal(got, want) {
		t.Fatal("the layout holds the wrong layer content")
	}
	wantRanges := []string{"", "bytes=100-", "bytes=200-", "bytes=300-", "bytes=400-", "bytes=500-"}
	if got := ranges(hits(hex)); !slices.Equal(got, wantRanges) {
		t.Fatalf("ranges %q, want %q", got, wantRanges)
	}
}

func TestBlobCacheStartsOverWhenTheServerIgnoresRange(t *testing.T) {
	host, hits := startCDN(t, func(w http.ResponseWriter, _ *http.Request, _ string, blob []byte, _ int) {
		w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
		w.Write(blob)
	})
	ref, hex, want := pushLayer(t, host, "lib/norange:1.0")
	dir := t.TempDir()
	writePartial(t, dir, hex, want[:100])
	got := pullLayer(t, testCache(dir, http.DefaultTransport), ref, hex)
	if !bytes.Equal(got, want) {
		t.Fatal("the layout holds the wrong layer content")
	}
	if got := ranges(hits(hex)); !slices.Equal(got, []string{"bytes=100-"}) {
		t.Fatalf("ranges %q, want one request that resumes at 100 and gets the whole blob", got)
	}
}

func TestBlobCacheResumesAStalledBody(t *testing.T) {
	host, hits := startCDN(t, func(w http.ResponseWriter, r *http.Request, hex string, blob []byte, hit int) {
		if hit > 0 || len(blob) < 1000 {
			serveRanges(w, r, hex, blob, hit)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
		w.WriteHeader(http.StatusOK)
		w.Write(blob[:100])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	ref, hex, want := pushLayer(t, host, "lib/stalled:1.0")
	start := time.Now()
	got := pullLayer(t, testCache(t.TempDir(), http.DefaultTransport), ref, hex)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the pull took %s, the stalled body was not cut", elapsed)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("the layout holds the wrong layer content")
	}
	if got := ranges(hits(hex)); !slices.Equal(got, []string{"", "bytes=100-"}) {
		t.Fatalf("ranges %q, want a resume at 100", got)
	}
}

func TestBlobCacheReplacesACorruptPartial(t *testing.T) {
	host, hits := startCDN(t, serveRanges)
	ref, hex, want := pushLayer(t, host, "lib/badpartial:1.0")
	dir := t.TempDir()
	partial := writePartial(t, dir, hex, bytes.Repeat([]byte("x"), 100))
	got := pullLayer(t, testCache(dir, http.DefaultTransport), ref, hex)
	if !bytes.Equal(got, want) {
		t.Fatal("the layout holds the wrong layer content")
	}
	if got := ranges(hits(hex)); !slices.Equal(got, []string{"bytes=100-", ""}) {
		t.Fatalf("ranges %q, want a resume at 100 and then a new download", got)
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatal("the corrupt partial is still there")
	}
	cached, err := os.ReadFile(filepath.Join(dir, "blobs", "sha256", hex))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cached, want) {
		t.Fatal("the cache holds the wrong layer content")
	}
}

func TestBlobCacheGivesUpAfterThreeAttemptsWithoutProgress(t *testing.T) {
	host, hits := startCDN(t, func(w http.ResponseWriter, r *http.Request, hex string, blob []byte, hit int) {
		if len(blob) < 1000 {
			serveRanges(w, r, hex, blob, hit)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	ref, hex, _ := pushLayer(t, host, "lib/down:1.0")
	_, err := pullLayout(context.Background(), testCache(t.TempDir(), http.DefaultTransport), ref, filepath.Join(t.TempDir(), "down.tar"), linuxARM64)
	if err == nil || !strings.Contains(err.Error(), "sha256:"+hex) || !strings.Contains(err.Error(), "3 attempts") {
		t.Fatalf("error %v, want one that names sha256:%s and 3 attempts", err, hex)
	}
	if got := len(hits(hex)); got != 3 {
		t.Fatalf("%d requests for the blob, want 3", got)
	}
}

func TestBlobCacheGivesUpAtTheAttemptLimit(t *testing.T) {
	host, hits := startCDN(t, serveRanges)
	ref, hex, _ := pushLayer(t, host, "lib/slow:1.0")
	var breaks atomic.Int64
	breaks.Store(blobAttempts * 2)
	_, err := pullLayout(context.Background(), testCache(t.TempDir(), breakBodies{next: http.DefaultTransport, hex: hex, after: 1, breaks: &breaks}), ref, filepath.Join(t.TempDir(), "slow.tar"), linuxARM64)
	want := strconv.Itoa(blobAttempts) + " attempts"
	if err == nil || !strings.Contains(err.Error(), "sha256:"+hex) || !strings.Contains(err.Error(), want) {
		t.Fatalf("error %v, want one that names sha256:%s and %s", err, hex, want)
	}
	if got := len(hits(hex)); got != blobAttempts {
		t.Fatalf("%d requests for the blob, want %d", got, blobAttempts)
	}
}

func TestBlobCacheDownloadsABlobOnceForTwoCallers(t *testing.T) {
	host, hits := startCDN(t, func(w http.ResponseWriter, r *http.Request, hex string, blob []byte, hit int) {
		if hit == 0 {
			time.Sleep(100 * time.Millisecond)
		}
		serveRanges(w, r, hex, blob, hit)
	})
	_, hex, want := pushLayer(t, host, "lib/shared:1.0")
	cache := testCache(t.TempDir(), http.DefaultTransport)
	requests := []*http.Request{blobRequest(t, host, "lib/shared", hex), blobRequest(t, host, "lib/shared", hex)}
	bodies := make([][]byte, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() {
			bodies[i], errs[i] = getBlob(cache, requests[i])
		})
	}
	wg.Wait()
	for i := range 2 {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if !bytes.Equal(bodies[i], want) {
			t.Fatalf("caller %d got the wrong blob", i)
		}
	}
	if got := len(hits(hex)); got != 1 {
		t.Fatalf("%d downloads of the blob, want 1", got)
	}
}

func TestBlobCacheSendsRangeButNoAuthorizationToTheCDN(t *testing.T) {
	host, hits := startCDN(t, serveRanges)
	_, hex, want := pushLayer(t, host, "lib/private:1.0")
	dir := t.TempDir()
	writePartial(t, dir, hex, want[:100])
	req := blobRequest(t, host, "lib/private", hex)
	req.Header.Set("Authorization", "Bearer registry-token")
	got, err := getBlob(testCache(dir, http.DefaultTransport), req)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("wrong blob")
	}
	if got := hits(hex); !slices.Equal(got, []cdnHit{{hex: hex, rng: "bytes=100-"}}) {
		t.Fatalf("cdn requests %+v, want one Range request without Authorization", got)
	}
}

func blobRequest(t *testing.T, host, repository, hex string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+host+"/v2/"+repository+"/blobs/sha256:"+hex, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func getBlob(cache blobCache, req *http.Request) ([]byte, error) {
	resp, err := cache.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
