package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/host"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func writeBundleRelease(t *testing.T, dir, k0sSum string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "manifests", "00-crds"), 0o755)
	os.WriteFile(filepath.Join(dir, "release.yaml"), []byte("version: v0.1.0\nimage: ghcr.io/cloudyfolks-io/bedrock:v0.1.0\nk0sVersion: v1.36.3+k0s.0\nk0sChecksums:\n  amd64: "+k0sSum+"\nbedrockChecksums:\n  amd64: sha256:2d7f45d7b98b427f824e0c643295583e9cf013faffdb5e7095d070ff85276bf4\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "images.txt"), []byte("quay.io/a/b:1\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "manifests", "00-crds", "a.yaml"), []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n  namespace: default\n"), 0o644)
}

func writeBedrockBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bedrock")
	if err := os.WriteFile(path, []byte("bedrock"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunBundleBuildProducesArchive(t *testing.T) {
	cache := t.TempDir()
	k0sContent := []byte("k0s")
	sum := sha256.Sum256(k0sContent)
	k0sPath := filepath.Join(cache, "k0s", "v1.36.3+k0s.0", "amd64")
	os.MkdirAll(filepath.Dir(k0sPath), 0o755)
	os.WriteFile(k0sPath, k0sContent, 0o755)
	releaseDir := t.TempDir()
	writeBundleRelease(t, releaseDir, "sha256:"+hex.EncodeToString(sum[:]))
	airgap := filepath.Join(t.TempDir(), "airgap.tar")
	os.WriteFile(airgap, []byte("airgap"), 0o644)
	var pullArgs []string
	var pullLock sync.Mutex
	deps := BundleDeps{
		Pull: func(_ context.Context, ref, dest, cacheDir, arch string) (string, error) {
			pullLock.Lock()
			pullArgs = append(pullArgs, cacheDir+" "+arch)
			pullLock.Unlock()
			return "sha256:" + strings.Repeat("f", 64), os.WriteFile(dest, []byte(ref), 0o644)
		},
		Airgap: func(_ context.Context, _, _, _, _ string) (string, error) { return airgap, nil },
	}
	out := filepath.Join(t.TempDir(), "bedrock-v0.1.0-bundle-amd64.tar.zst")
	var stdout, stderr bytes.Buffer
	code := RunBundleBuild(context.Background(), bundleBuildOptions{releaseDir: releaseDir, arch: "amd64", out: out, cacheDir: cache, k0sBaseURL: "", bedrockBinary: writeBedrockBinary(t)}, deps, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	spec, err := release.OpenBundle(out, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != "v0.1.0" || len(spec.Images) != 2 {
		t.Fatalf("spec %+v", spec)
	}
	if want := []string{cache + " amd64", cache + " amd64"}; !slices.Equal(pullArgs, want) {
		t.Fatalf("pull cache dirs and arches %v, want %v", pullArgs, want)
	}
	if !strings.Contains(stdout.String(), out) {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestRunBundleBuildDownloadsK0sWhenMissing(t *testing.T) {
	k0sContent := []byte("k0s-downloaded")
	sum := sha256.Sum256(k0sContent)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(k0sContent)
	}))
	defer server.Close()
	cache := t.TempDir()
	releaseDir := t.TempDir()
	writeBundleRelease(t, releaseDir, "sha256:"+hex.EncodeToString(sum[:]))
	airgap := filepath.Join(t.TempDir(), "airgap.tar")
	os.WriteFile(airgap, []byte("airgap"), 0o644)
	deps := BundleDeps{
		Pull: func(_ context.Context, ref, dest, _, _ string) (string, error) {
			return "sha256:" + strings.Repeat("f", 64), os.WriteFile(dest, []byte(ref), 0o644)
		},
		Airgap: func(_ context.Context, _, _, _, _ string) (string, error) { return airgap, nil },
	}
	out := filepath.Join(t.TempDir(), "bedrock-v0.1.0-bundle-amd64.tar.zst")
	var stdout, stderr bytes.Buffer
	code := RunBundleBuild(context.Background(), bundleBuildOptions{releaseDir: releaseDir, arch: "amd64", out: out, cacheDir: cache, k0sBaseURL: server.URL, bedrockBinary: writeBedrockBinary(t)}, deps, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	got, err := os.ReadFile(filepath.Join(cache, "k0s", "v1.36.3+k0s.0", "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(k0sContent) {
		t.Fatalf("k0s binary content %q", got)
	}
}

func TestRunBundleBuildFailsWithoutK0sAndBaseURL(t *testing.T) {
	cache := t.TempDir()
	releaseDir := t.TempDir()
	writeBundleRelease(t, releaseDir, "sha256:"+strings.Repeat("a", 64))
	out := filepath.Join(t.TempDir(), "bedrock-v0.1.0-bundle-amd64.tar.zst")
	var stdout, stderr bytes.Buffer
	code := RunBundleBuild(context.Background(), bundleBuildOptions{releaseDir: releaseDir, arch: "amd64", out: out, cacheDir: cache, k0sBaseURL: ""}, BundleDeps{}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected failure when k0s binary missing and no base url")
	}
	if !strings.Contains(stderr.String(), "is missing and no base url is set") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestBundleAssetName(t *testing.T) {
	if got := BundleAssetName("v0.1.0", "amd64"); got != "bedrock-v0.1.0-bundle-amd64.tar.zst" {
		t.Fatal(got)
	}
}

func TestVerifySums(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644)
	sum := sha256.Sum256([]byte("hello"))
	text := hex.EncodeToString(sum[:]) + "  a.txt\n"
	if err := VerifySums(text, dir, []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := VerifySums(strings.Repeat("0", 64)+"  a.txt\n", dir, []string{"a.txt"}); err == nil {
		t.Fatal("expected mismatch")
	}
	if err := VerifySums(text, dir, []string{"b.txt"}); err == nil {
		t.Fatal("expected missing entry error")
	}
}

func bundleServer(t *testing.T, version string, bundle []byte, sums string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	base := "/" + version + "/"
	mux.HandleFunc(base+"SHA256SUMS", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sums)) })
	mux.HandleFunc(base+"SHA256SUMS.sigstore.json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{}")) })
	mux.HandleFunc(base+BundleAssetName(version, "amd64"), func(w http.ResponseWriter, r *http.Request) { w.Write(bundle) })
	return httptest.NewServer(mux)
}

func TestRunBundlePullVerifiesWithCosign(t *testing.T) {
	bundle := []byte("bundle-bytes")
	sum := sha256.Sum256(bundle)
	sums := hex.EncodeToString(sum[:]) + "  " + BundleAssetName("v0.1.0", "amd64") + "\n"
	server := bundleServer(t, "v0.1.0", bundle, sums)
	defer server.Close()
	out := t.TempDir()
	exec := &host.FakeExec{Responses: map[string]string{"cosign version --json": `{"gitVersion":"v3.0.2"}`}, ResponsePrefixes: map[string]string{"cosign verify-blob": ""}}
	deps := BundleDeps{Exec: exec, LookPath: func(string) (string, error) { return "/usr/bin/cosign", nil }}
	var stdout, stderr bytes.Buffer
	code := RunBundlePull(context.Background(), bundlePullOptions{version: "v0.1.0", out: out, arch: "amd64", baseURL: server.URL}, deps, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	got, _ := os.ReadFile(filepath.Join(out, BundleAssetName("v0.1.0", "amd64")))
	if string(got) != "bundle-bytes" {
		t.Fatalf("bundle content %q", got)
	}
	if len(exec.Calls) != 2 || exec.Calls[0] != "cosign version --json" || !strings.HasPrefix(exec.Calls[1], "cosign verify-blob --bundle "+filepath.Join(out, ".bundle-pull-")) || !strings.Contains(exec.Calls[1], "/"+sigstoreBundleFile+" ") || !strings.Contains(exec.Calls[1], "refs/tags/"+regexp.QuoteMeta("v0.1.0")) {
		t.Fatalf("cosign call %v", exec.Calls)
	}
}

func TestRunBundlePullSplitArchive(t *testing.T) {
	version := "v0.1.0"
	asset := BundleAssetName(version, "amd64")
	bundle := []byte{0, 0xff, '\n', '\r', 0x7f}
	sum := sha256.Sum256(bundle)
	sums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	files := []string{sumsFile, sigstoreBundleFile, asset}
	cases := []struct {
		name       string
		status     int
		parts      map[string][]byte
		requests   []string
		errorText  string
		keepOutput bool
	}{
		{
			name:     "assemble raw bytes",
			status:   http.StatusNotFound,
			parts:    map[string][]byte{".part00": bundle[:3], ".part01": bundle[3:]},
			requests: []string{"", ".part00", ".part01", ".part02"},
		},
		{
			name:       "reject a gap and keep concurrent output",
			status:     http.StatusNotFound,
			parts:      map[string][]byte{".part00": bundle[:3], ".part02": bundle[3:]},
			requests:   []string{"", ".part00", ".part01"},
			errorText:  "checksum mismatch",
			keepOutput: true,
		},
		{
			name:      "do not split on server error",
			status:    http.StatusServiceUnavailable,
			parts:     map[string][]byte{".part00": bundle},
			requests:  []string{""},
			errorText: "status 503",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			var other *httptest.Server
			otherArchive := []byte("archive from another pull")
			otherSum := sha256.Sum256(otherArchive)
			otherSums := hex.EncodeToString(otherSum[:]) + "  " + asset + "\n"
			if tc.keepOutput {
				other = bundleServer(t, version, otherArchive, otherSums)
				defer other.Close()
			}
			base := "/" + version + "/"
			requests := make(chan string, bundlePartCount+1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case base + sumsFile:
					_, _ = w.Write([]byte(sums))
				case base + sigstoreBundleFile:
					_, _ = w.Write([]byte("{}"))
				default:
					suffix, ok := strings.CutPrefix(r.URL.Path, base+asset)
					if !ok {
						t.Errorf("unexpected request %s", r.URL.Path)
						http.NotFound(w, r)
						return
					}
					requests <- suffix
					if suffix == "" {
						w.WriteHeader(tc.status)
						return
					}
					if !tc.keepOutput {
						for _, name := range files {
							if _, err := os.Lstat(filepath.Join(out, name)); !os.IsNotExist(err) {
								t.Errorf("%s is visible before verification: %v", name, err)
							}
						}
					}
					if part, ok := tc.parts[suffix]; ok {
						_, _ = w.Write(part)
						return
					}
					if other != nil {
						options := bundlePullOptions{version: version, out: out, arch: "amd64", baseURL: other.URL}
						deps := BundleDeps{LookPath: func(string) (string, error) { return "", errors.New("not found") }}
						if code := RunBundlePull(r.Context(), options, deps, io.Discard, io.Discard); code != 0 {
							t.Errorf("concurrent pull failed with exit %d", code)
						}
					}
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			exec := &host.FakeExec{Responses: map[string]string{"cosign version --json": `{"gitVersion":"v3.0.2"}`}, ResponsePrefixes: map[string]string{"cosign verify-blob": ""}}
			deps := BundleDeps{Exec: exec, LookPath: func(string) (string, error) { return "/usr/bin/cosign", nil }}
			var stdout, stderr bytes.Buffer
			code := RunBundlePull(context.Background(), bundlePullOptions{version: version, out: out, arch: "amd64", baseURL: server.URL}, deps, &stdout, &stderr)
			var gotRequests []string
			for len(requests) > 0 {
				gotRequests = append(gotRequests, <-requests)
			}
			if !slices.Equal(gotRequests, tc.requests) {
				t.Fatalf("requested suffixes %q, want %q", gotRequests, tc.requests)
			}
			if tc.errorText == "" {
				got, err := os.ReadFile(filepath.Join(out, asset))
				if code != 0 || err != nil || !bytes.Equal(got, bundle) || !strings.Contains(stdout.String(), "verified at") {
					t.Fatalf("exit %d, archive %x, read error %v, stderr %q", code, got, err, stderr.String())
				}
				if len(exec.Calls) != 2 || !strings.HasPrefix(exec.Calls[1], "cosign verify-blob ") {
					t.Fatalf("signature check calls %v", exec.Calls)
				}
			} else if code == 0 || !strings.Contains(stderr.String(), tc.errorText) || strings.Contains(stdout.String(), "verified at") {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
			}
			entries, err := os.ReadDir(out)
			if err != nil {
				t.Fatal(err)
			}
			if tc.keepOutput {
				for name, want := range map[string]string{asset: string(otherArchive), sumsFile: otherSums, sigstoreBundleFile: "{}"} {
					got, err := os.ReadFile(filepath.Join(out, name))
					if err != nil || string(got) != want {
						t.Fatalf("concurrent pull's %s changed: %q, %v", name, got, err)
					}
				}
			}
			wantCount := len(files)
			if code != 0 && !tc.keepOutput {
				wantCount = 0
			}
			if len(entries) != wantCount {
				t.Fatalf("output directory has %v, want %d files", entries, wantCount)
			}
		})
	}
}

func TestRunBundlePullWarnsWithoutCosign(t *testing.T) {
	bundle := []byte("b")
	sum := sha256.Sum256(bundle)
	server := bundleServer(t, "v0.1.0", bundle, hex.EncodeToString(sum[:])+"  "+BundleAssetName("v0.1.0", "amd64")+"\n")
	defer server.Close()
	deps := BundleDeps{Exec: &host.FakeExec{}, LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	var stdout, stderr bytes.Buffer
	if code := RunBundlePull(context.Background(), bundlePullOptions{version: "v0.1.0", out: t.TempDir(), arch: "amd64", baseURL: server.URL}, deps, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cosign not found") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestRunBundlePullRejectsBadChecksum(t *testing.T) {
	server := bundleServer(t, "v0.1.0", []byte("b"), strings.Repeat("0", 64)+"  "+BundleAssetName("v0.1.0", "amd64")+"\n")
	defer server.Close()
	out := t.TempDir()
	deps := BundleDeps{Exec: &host.FakeExec{}, LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	var stdout, stderr bytes.Buffer
	if code := RunBundlePull(context.Background(), bundlePullOptions{version: "v0.1.0", out: out, arch: "amd64", baseURL: server.URL}, deps, &stdout, &stderr); code == 0 {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(filepath.Join(out, BundleAssetName("v0.1.0", "amd64"))); !os.IsNotExist(err) {
		t.Fatal("bad bundle must be removed")
	}
}

func TestRunBundlePullFailsCosignVerification(t *testing.T) {
	bundle := []byte("b")
	sum := sha256.Sum256(bundle)
	server := bundleServer(t, "v0.1.0", bundle, hex.EncodeToString(sum[:])+"  "+BundleAssetName("v0.1.0", "amd64")+"\n")
	defer server.Close()
	exec := &host.FakeExec{Responses: map[string]string{"cosign version --json": `{"gitVersion":"v3.0.2"}`}}
	exec.ErrorPrefixes = map[string]error{"cosign verify-blob": &host.ExitError{Code: 1}}
	deps := BundleDeps{Exec: exec, LookPath: func(string) (string, error) { return "/usr/bin/cosign", nil }}
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := RunBundlePull(context.Background(), bundlePullOptions{version: "v0.1.0", out: out, arch: "amd64", baseURL: server.URL}, deps, &stdout, &stderr); code == 0 {
		t.Fatal("expected failure when cosign rejects")
	}
	for _, name := range []string{sumsFile, sigstoreBundleFile, BundleAssetName("v0.1.0", "amd64")} {
		if _, err := os.Stat(filepath.Join(out, name)); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed after a failed signature check", name)
		}
	}
}

func TestRunBundlePullRejectsOldCosign(t *testing.T) {
	bundle := []byte("b")
	sum := sha256.Sum256(bundle)
	server := bundleServer(t, "v0.1.0", bundle, hex.EncodeToString(sum[:])+"  "+BundleAssetName("v0.1.0", "amd64")+"\n")
	defer server.Close()
	exec := &host.FakeExec{Responses: map[string]string{"cosign version --json": `{"gitVersion":"v2.6.1"}`}}
	deps := BundleDeps{Exec: exec, LookPath: func(string) (string, error) { return "/usr/bin/cosign", nil }}
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := RunBundlePull(context.Background(), bundlePullOptions{version: "v0.1.0", out: out, arch: "amd64", baseURL: server.URL}, deps, &stdout, &stderr); code == 0 {
		t.Fatal("an old cosign must fail the pull")
	}
	if !strings.Contains(stderr.String(), "cosign v2.6.1 is older than the minimum v3.0.0") {
		t.Fatalf("stderr %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(out, BundleAssetName("v0.1.0", "amd64"))); !os.IsNotExist(err) {
		t.Fatal("the unverified bundle must be removed")
	}
}

func TestRequireCosign(t *testing.T) {
	cases := []struct {
		output string
		want   string
	}{
		{`{"gitVersion":"v3.0.0"}`, ""},
		{`{"gitVersion":"v3.1.2"}`, ""},
		{`{"gitVersion":"v10.0.0"}`, ""},
		{`{"gitVersion":"v2.6.1"}`, "cosign v2.6.1 is older than the minimum v3.0.0"},
		{`{"gitVersion":""}`, "cosign reported no version"},
		{`not json`, "read cosign version"},
	}
	for _, tc := range cases {
		err := requireCosign(tc.output, "v3.0.0")
		if tc.want == "" && err != nil {
			t.Fatalf("%s: unexpected error %v", tc.output, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("%s: error %v, want %q", tc.output, err, tc.want)
		}
	}
}

func TestBundlePullUsageNamesMinimumCosign(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := bundlePull(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "cosign v3.0.0 or newer") {
		t.Fatalf("usage must name the minimum cosign version: %q", stderr.String())
	}
}
