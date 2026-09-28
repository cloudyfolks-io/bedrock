package agent

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/depot"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

type fakeContainerd struct {
	calls    []string
	images   map[string][]string
	present  []string
	targets  map[string]string
	failures map[string]error
}

func imageListing(names []string, targets map[string]string) string {
	lines := []string{"REF TYPE DIGEST SIZE PLATFORMS LABELS"}
	for _, name := range names {
		target, overridden := targets[name]
		if _, digest, pinned := strings.Cut(name, "@"); !overridden && pinned {
			target = digest
		}
		if target == "" {
			target = "sha256:" + strings.Repeat("0", 64)
		}
		lines = append(lines, name+" application/vnd.oci.image.index.v1+json "+target+" 1.2 MiB linux/amd64 -")
	}
	return strings.Join(lines, "\n") + "\n"
}

func (f *fakeContainerd) Run(_ context.Context, name string, args ...string) (string, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, call)
	if err, ok := f.failures[call]; ok {
		return "", err
	}
	switch {
	case call == "/usr/local/bin/k0s ctr --namespace k8s.io images ls --quiet":
		return strings.Join(f.present, "\n") + "\n", nil
	case call == "/usr/local/bin/k0s ctr --namespace k8s.io images ls":
		return imageListing(f.present, f.targets), nil
	case strings.HasPrefix(call, "/usr/local/bin/k0s ctr --namespace k8s.io images import "):
		f.present = append(f.present, f.images[filepath.Base(strings.TrimPrefix(call, "/usr/local/bin/k0s ctr --namespace k8s.io images import "))]...)
		return "", nil
	}
	return "", fmt.Errorf("no fake response for %q", call)
}

type preloadFixture struct {
	env       StepEnv
	nodeRoot  string
	server    *httptest.Server
	container *fakeContainerd
}

const pinnedImage = "quay.io/a/b:1@sha256:1111111111111111111111111111111111111111111111111111111111111111"

func newPreloadFixture(t *testing.T) preloadFixture {
	t.Helper()
	depotRoot := t.TempDir()
	bundle := depot.BundleDir(depotRoot, "v0.3.0", runtime.GOARCH)
	imageFile := release.ImageFileName(pinnedImage)
	for path, content := range map[string]string{"k0s/k0s": "new k0s", "bedrock/bedrock": "new bedrock", "images/" + imageFile: "layout", "images/k0s-airgap.tar": "airgap"} {
		writeFixtureFile(t, filepath.Join(bundle, path), content)
	}
	spec, err := sigyaml.Marshal(release.BundleSpec{Version: "v0.3.0", Arch: runtime.GOARCH, Images: []release.BundleImage{{Ref: pinnedImage, File: imageFile}}})
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(bundle, release.BundleFileName), string(spec))
	server := httptest.NewServer(depot.Handler(depotRoot))
	t.Cleanup(server.Close)
	k0sSum, _ := release.FileSHA256(filepath.Join(bundle, "k0s/k0s"))
	bedrockSum, _ := release.FileSHA256(filepath.Join(bundle, "bedrock/bedrock"))
	nodeRoot := t.TempDir()
	writeFixtureFile(t, filepath.Join(nodeRoot, "usr/local/bin/k0s"), "old k0s")
	names, err := release.ContainerdNames(pinnedImage)
	if err != nil {
		t.Fatal(err)
	}
	container := &fakeContainerd{images: map[string][]string{imageFile: names, "k0s-airgap.tar": {"docker.io/k0sproject/pause:3.10"}}}
	deps := newDeps(nil, metav1.Now().Time)
	deps.Exec = container
	deps.Root = nodeRoot
	deps.HTTP = server.Client()
	target := v1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "v0.3.0"}, Spec: v1alpha1.ReleaseSpec{Version: "v0.3.0", K0sChecksums: map[string]string{runtime.GOARCH: k0sSum}, BedrockChecksums: map[string]string{runtime.GOARCH: bedrockSum}, Images: []string{pinnedImage}}}
	upgrade := v1alpha1.NodeUpgrade{Spec: v1alpha1.NodeUpgradeSpec{Node: "node-a", Version: "v0.3.0", From: "v0.2.0", Depot: depot.BundleURL(server.URL, "v0.3.0", runtime.GOARCH), Attempt: 1}}
	return preloadFixture{env: StepEnv{Deps: deps, Upgrade: upgrade, Target: target}, nodeRoot: nodeRoot, server: server, container: container}
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFixtureFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPreloadStagesBinariesAndImages(t *testing.T) {
	fixture := newPreloadFixture(t)
	writeFixtureFile(t, filepath.Join(fixture.nodeRoot, "run/k0s/containerd.sock"), "")
	outcome, err := preload(context.Background(), fixture.env)
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(fixture.nodeRoot, "var/lib/bedrock/staged/v0.3.0")
	if readFixtureFile(t, filepath.Join(staged, "k0s")) != "new k0s" || readFixtureFile(t, filepath.Join(staged, "bedrock")) != "new bedrock" {
		t.Fatal("binaries not staged")
	}
	info, err := os.Stat(filepath.Join(staged, "bedrock"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("staged bedrock must be executable: %v", err)
	}
	if readFixtureFile(t, filepath.Join(fixture.nodeRoot, "var/lib/bedrock/previous/k0s")) != "old k0s" {
		t.Fatal("the running k0s must be kept as previous/k0s")
	}
	imports := 0
	for _, call := range fixture.container.calls {
		if strings.Contains(call, " images import ") {
			imports++
		}
	}
	if imports != 2 {
		t.Fatalf("expected the image and the k0s airgap tarball to be imported: %v", fixture.container.calls)
	}
	if entries, _ := os.ReadDir(filepath.Join(staged, "images")); len(entries) != 0 {
		t.Fatalf("imported tarballs must be removed, left %v", entries)
	}
	if outcome.Message != "k0s, bedrock and 1 images staged" {
		t.Fatalf("message %q", outcome.Message)
	}
}

func TestPreloadSkipsImagesAlreadyPresent(t *testing.T) {
	fixture := newPreloadFixture(t)
	writeFixtureFile(t, filepath.Join(fixture.nodeRoot, "run/k0s/containerd.sock"), "")
	names, _ := release.ContainerdNames(pinnedImage)
	fixture.container.present = names
	if _, err := preload(context.Background(), fixture.env); err != nil {
		t.Fatal(err)
	}
	for _, call := range fixture.container.calls {
		if strings.HasSuffix(call, release.ImageFileName(pinnedImage)) {
			t.Fatalf("a present image must not be imported again: %v", fixture.container.calls)
		}
	}
}

func TestPreloadKeepsTheFirstPrevious(t *testing.T) {
	fixture := newPreloadFixture(t)
	writeFixtureFile(t, filepath.Join(fixture.nodeRoot, "var/lib/bedrock/previous/k0s"), "k0s before the upgrade")
	if _, err := preload(context.Background(), fixture.env); err != nil {
		t.Fatal(err)
	}
	if got := readFixtureFile(t, filepath.Join(fixture.nodeRoot, "var/lib/bedrock/previous/k0s")); got != "k0s before the upgrade" {
		t.Fatalf("previous/k0s must never be overwritten, got %q", got)
	}
}

func TestPreloadWithoutContainersStagesBinariesOnly(t *testing.T) {
	fixture := newPreloadFixture(t)
	outcome, err := preload(context.Background(), fixture.env)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.container.calls) != 0 || outcome.Message != "k0s and bedrock staged" {
		t.Fatalf("calls %v message %q", fixture.container.calls, outcome.Message)
	}
}

func TestPreloadRejectsABadChecksum(t *testing.T) {
	fixture := newPreloadFixture(t)
	fixture.env.Target.Spec.K0sChecksums[runtime.GOARCH] = "sha256:" + strings.Repeat("0", 64)
	_, err := preload(context.Background(), fixture.env)
	if err == nil || !strings.Contains(err.Error(), "sha256:"+strings.Repeat("0", 64)) {
		t.Fatalf("error %v must name the wanted digest", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.nodeRoot, "var/lib/bedrock/staged/v0.3.0/k0s")); !os.IsNotExist(err) {
		t.Fatal("a k0s binary with a wrong checksum must not stay staged")
	}
}

func TestPreloadFailsWhenAnImageStaysMissing(t *testing.T) {
	fixture := newPreloadFixture(t)
	writeFixtureFile(t, filepath.Join(fixture.nodeRoot, "run/k0s/containerd.sock"), "")
	fixture.container.images[release.ImageFileName(pinnedImage)] = nil
	_, err := preload(context.Background(), fixture.env)
	if err == nil || !strings.Contains(err.Error(), "quay.io/a/b@sha256:1111") {
		t.Fatalf("error %v must name the missing image", err)
	}
}

func TestPreloadFailsWhenAnImageHasAnotherDigest(t *testing.T) {
	fixture := newPreloadFixture(t)
	writeFixtureFile(t, filepath.Join(fixture.nodeRoot, "run/k0s/containerd.sock"), "")
	other := "sha256:" + strings.Repeat("9", 64)
	fixture.container.targets = map[string]string{"quay.io/a/b@sha256:" + strings.Repeat("1", 64): other}
	_, err := preload(context.Background(), fixture.env)
	if err == nil || !strings.Contains(err.Error(), pinnedImage) || !strings.Contains(err.Error(), other) {
		t.Fatalf("error %v must name the image and the digest containerd holds", err)
	}
}

func TestPreloadLinksTargetTarballsForK0sReimport(t *testing.T) {
	fixture := newPreloadFixture(t)
	writeFixtureFile(t, filepath.Join(fixture.nodeRoot, "run/k0s/containerd.sock"), "")
	if _, err := preload(context.Background(), fixture.env); err != nil {
		t.Fatal(err)
	}
	imagesDir := filepath.Join(fixture.nodeRoot, k0sImagesDir)
	imageFile := release.ImageFileName(pinnedImage)
	if readFixtureFile(t, filepath.Join(imagesDir, imageFile)) != "layout" {
		t.Fatal("the target image tarball must be kept for k0s to re-import")
	}
	if readFixtureFile(t, filepath.Join(imagesDir, "k0s-airgap-v0.3.0.tar")) != "airgap" {
		t.Fatal("the target airgap tarball must be kept under its versioned name")
	}
	if _, err := os.Stat(filepath.Join(imagesDir, "k0s-airgap.tar")); !os.IsNotExist(err) {
		t.Fatal("k0s-airgap.tar must never be written by Preload")
	}
}

func TestPreloadRelinkingTarballsIsANoOp(t *testing.T) {
	fixture := newPreloadFixture(t)
	writeFixtureFile(t, filepath.Join(fixture.nodeRoot, "run/k0s/containerd.sock"), "")
	if _, err := preload(context.Background(), fixture.env); err != nil {
		t.Fatal(err)
	}
	if _, err := preload(context.Background(), fixture.env); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(fixture.nodeRoot, k0sImagesDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("images dir has %d entries, want 2 (the image and the versioned airgap file)", len(entries))
	}
}

func TestPreloadKeepsAnExistingImageTarball(t *testing.T) {
	fixture := newPreloadFixture(t)
	writeFixtureFile(t, filepath.Join(fixture.nodeRoot, "run/k0s/containerd.sock"), "")
	imageFile := release.ImageFileName(pinnedImage)
	writeFixtureFile(t, filepath.Join(fixture.nodeRoot, k0sImagesDir, imageFile), "already there")
	if _, err := preload(context.Background(), fixture.env); err != nil {
		t.Fatal(err)
	}
	if got := readFixtureFile(t, filepath.Join(fixture.nodeRoot, k0sImagesDir, imageFile)); got != "already there" {
		t.Fatalf("an existing tarball with the same name must be kept, got %q", got)
	}
}

func TestPreloadIsRegistered(t *testing.T) {
	if _, ok := Steps()[v1alpha1.StepPreload]; !ok {
		t.Fatal("Steps must include Preload")
	}
	if !slices.Contains([]string{"amd64", "arm64"}, runtime.GOARCH) {
		t.Skip("bundles exist for amd64 and arm64 only")
	}
}
