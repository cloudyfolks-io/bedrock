package depot

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestURLs(t *testing.T) {
	base := BaseURL("10.0.0.11")
	if base != "http://10.0.0.11:9480" {
		t.Fatalf("base %s", base)
	}
	if got := BundleURL(base, "v0.3.0", "amd64"); got != "http://10.0.0.11:9480/v0.3.0/amd64" {
		t.Fatalf("bundle url %s", got)
	}
	if got := BundleDir("/", "v0.3.0", "amd64"); got != "/var/lib/bedrock/depot/v0.3.0/amd64" {
		t.Fatalf("bundle dir %s", got)
	}
}

func TestHandlerServesOnlyBundleFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(BundleDir(root, "v0.3.0", "amd64"), "k0s", "k0s"), "k0s binary")
	writeFile(t, filepath.Join(root, "etc", "shadow"), "secret")
	server := httptest.NewServer(Handler(root))
	defer server.Close()
	cases := []struct {
		method string
		path   string
		status int
		body   string
	}{
		{http.MethodGet, "/v0.3.0/amd64/k0s/k0s", http.StatusOK, "k0s binary"},
		{http.MethodHead, "/v0.3.0/amd64/k0s/k0s", http.StatusOK, ""},
		{http.MethodGet, "/v0.3.0/amd64/", http.StatusNotFound, ""},
		{http.MethodGet, "/../../../etc/shadow", http.StatusNotFound, ""},
		{http.MethodGet, "/v0.3.0/amd64/missing", http.StatusNotFound, ""},
		{http.MethodPost, "/v0.3.0/amd64/k0s/k0s", http.StatusMethodNotAllowed, ""},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.URL.Path = tc.path
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("%s %s: status %d, want %d", tc.method, tc.path, resp.StatusCode, tc.status)
		}
		if tc.body != "" && string(body) != tc.body {
			t.Fatalf("%s %s: body %q", tc.method, tc.path, body)
		}
		if strings.Contains(string(body), "secret") {
			t.Fatalf("%s %s leaked a file outside the depot", tc.method, tc.path)
		}
	}
}

func TestScanListsBundles(t *testing.T) {
	root := t.TempDir()
	for _, bundle := range []struct{ version, arch string }{{"v0.3.0", "arm64"}, {"v0.3.0", "amd64"}, {"v0.2.0", "amd64"}} {
		dir := BundleDir(root, bundle.version, bundle.arch)
		writeFile(t, filepath.Join(dir, release.BundleFileName), "version: "+bundle.version+"\n")
		writeFile(t, filepath.Join(dir, "k0s", "k0s"), "12345")
	}
	writeFile(t, filepath.Join(root, Dir, "v0.4.0", "amd64", "k0s", "k0s"), "no bundle.yaml yet")
	bundles, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []v1alpha1.DepotBundle{
		{Version: "v0.2.0", Arch: "amd64", Bytes: 21},
		{Version: "v0.3.0", Arch: "amd64", Bytes: 21},
		{Version: "v0.3.0", Arch: "arm64", Bytes: 21},
	}
	if len(bundles) != len(want) {
		t.Fatalf("bundles %+v", bundles)
	}
	for i := range want {
		if bundles[i] != want[i] {
			t.Fatalf("bundle %d: %+v, want %+v", i, bundles[i], want[i])
		}
	}
}

func TestScanWithoutDepot(t *testing.T) {
	bundles, err := Scan(t.TempDir())
	if err != nil || len(bundles) != 0 {
		t.Fatalf("bundles %v err %v", bundles, err)
	}
}

func TestDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte("payload"))
		case "/cut":
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("short"))
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "staged", "file")
	if err := Download(context.Background(), http.DefaultClient, server.URL+"/ok", dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "payload" {
		t.Fatalf("content %q", got)
	}
	for _, path := range []string{"/missing", "/cut"} {
		target := filepath.Join(dir, "other"+strings.ReplaceAll(path, "/", "-"))
		err := Download(context.Background(), http.DefaultClient, server.URL+path, target)
		if err == nil || !strings.Contains(err.Error(), server.URL+path) {
			t.Fatalf("%s: error %v must name the url", path, err)
		}
		for _, leftover := range []string{target, target + ".part"} {
			if _, err := os.Stat(leftover); !os.IsNotExist(err) {
				t.Fatalf("%s: %s must not exist after a failed download", path, leftover)
			}
		}
	}
}

func TestVerify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k0s")
	writeFile(t, path, "k0s")
	good, err := release.FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(path, good); err != nil {
		t.Fatal(err)
	}
	wrong := "sha256:" + strings.Repeat("0", 64)
	err = Verify(path, wrong)
	if err == nil || !strings.Contains(err.Error(), wrong) || !strings.Contains(err.Error(), good) {
		t.Fatalf("mismatch error %v must name both digests", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a file with a wrong checksum must be removed")
	}
	writeFile(t, path, "k0s")
	if err := Verify(path, ""); err == nil {
		t.Fatal("a missing checksum must fail")
	}
}

func TestServeStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listener.Addr().String(), t.TempDir()) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve must stop cleanly, got %v", err)
	}
}
