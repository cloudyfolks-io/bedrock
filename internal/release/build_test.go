package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-labs/bedrock/api/v1alpha1"
)

func testBuild(t *testing.T) (BuildConfig, BuildOptions) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Fatal("helm is required on PATH for the release builder tests")
	}
	cfg, err := LoadBuildConfig(filepath.Join("testdata", "build", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	opts := BuildOptions{Version: "v9.9.9", Image: "ghcr.io/cloudyfolks-labs/bedrock:v9.9.9", Out: t.TempDir(), Helm: "helm", Root: filepath.Join("testdata", "build"), K0sBaseURL: ""}
	return cfg, opts
}

func TestBuildRendersChartsAndDirs(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Fatal("helm is required on PATH for the release builder tests")
	}
	cfg, err := LoadBuildConfig(filepath.Join("testdata", "build", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("binary for " + r.URL.Path))
	}))
	defer server.Close()
	opts := BuildOptions{Version: "v9.9.9", Image: "ghcr.io/cloudyfolks-labs/bedrock:v9.9.9", Out: out, Helm: "helm", Root: filepath.Join("testdata", "build"), K0sBaseURL: server.URL, CacheDir: t.TempDir()}
	if err := Build(cfg, opts); err != nil {
		t.Fatal(err)
	}
	bundle, err := Load(os.DirFS(out))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Spec.Version != "v9.9.9" || bundle.Spec.Image != opts.Image || bundle.Spec.K0sVersion != "v1.36.3+k0s.0" {
		t.Fatalf("spec %+v", bundle.Spec)
	}
	if len(bundle.Groups) != 3 || bundle.Groups[0].Name != "crds" || bundle.Groups[1].Name != "widgets" || bundle.Groups[2].Name != "bedrock" {
		t.Fatalf("groups %+v", bundle.Groups)
	}
	if len(bundle.Groups[0].Objects) != 1 || bundle.Groups[0].Objects[0].GetKind() != "CustomResourceDefinition" {
		t.Fatalf("crds group %+v", bundle.Groups[0].Objects)
	}
	widgets := bundle.Groups[1].Objects
	if len(widgets) != 3 || widgets[0].GetKind() != "Namespace" || widgets[0].GetName() != "widgets" || widgets[1].GetKind() != "ServiceAccount" || widgets[2].GetKind() != "Deployment" {
		t.Fatalf("widgets group %+v", widgets)
	}
	if widgets[1].GetNamespace() != "widgets" {
		t.Fatalf("chart ServiceAccount namespace %q, want widgets", widgets[1].GetNamespace())
	}
	replicas, _, _ := unstructuredInt(widgets[2], "spec", "replicas")
	if replicas != 2 {
		t.Fatalf("values not applied, replicas %d", replicas)
	}
	operatorImages := ImagesOf([]Group{bundle.Groups[2]})
	if len(operatorImages) != 1 || operatorImages[0] != opts.Image {
		t.Fatalf("operator image not rewritten: %v", operatorImages)
	}
	images := bundle.Images
	if len(images) != 3 || images[0] != "ghcr.io/cloudyfolks-labs/bedrock:v9.9.9" || images[1] != "quay.io/example/extra:1" || images[2] != "quay.io/example/widgets:0.1.0" {
		t.Fatalf("images %v", images)
	}
	if len(bundle.Spec.Components) != 3 || bundle.Spec.Components[1].Version != "0.1.0" || bundle.Spec.Components[1].Image != "quay.io/example/widgets:0.1.0" {
		t.Fatalf("components %+v", bundle.Spec.Components)
	}
	if !strings.HasPrefix(bundle.Spec.K0sChecksums["amd64"], "sha256:") {
		t.Fatalf("k0s amd64 checksum %+v", bundle.Spec.K0sChecksums)
	}
	if !strings.HasPrefix(bundle.Spec.K0sChecksums["arm64"], "sha256:") {
		t.Fatalf("k0s arm64 checksum %+v", bundle.Spec.K0sChecksums)
	}
}

func unstructuredInt(obj *unstructured.Unstructured, fields ...string) (int64, bool, error) {
	return unstructured.NestedInt64(obj.Object, fields...)
}

func TestBuildMergesSameNamedOutputs(t *testing.T) {
	cfg, err := LoadBuildConfig(filepath.Join("testdata", "build", "components-merge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	opts := BuildOptions{Version: "v9.9.9", Image: "ghcr.io/cloudyfolks-labs/bedrock:v9.9.9", Out: out, Helm: "helm", Root: filepath.Join("testdata", "build")}
	if err := Build(cfg, opts); err != nil {
		t.Fatal(err)
	}
	bundle, err := Load(os.DirFS(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Groups) != 1 || bundle.Groups[0].Name != "bedrock" {
		t.Fatalf("groups %+v", bundle.Groups)
	}
	objects := bundle.Groups[0].Objects
	if len(objects) != 2 {
		t.Fatalf("expected 2 merged objects, got %+v", objects)
	}
	kinds := map[string]bool{}
	for _, obj := range objects {
		kinds[obj.GetKind()] = true
	}
	if !kinds["Deployment"] || !kinds["ConfigMap"] {
		t.Fatalf("expected both Deployment and ConfigMap, got %+v", objects)
	}
}

func TestBuildPinsDigestsWhenRequested(t *testing.T) {
	cfg, opts := testBuild(t)
	opts.PinDigests = true
	opts.Resolve = func(_ context.Context, ref string) (string, error) {
		return "sha256:" + strings.Repeat("c", 64), nil
	}
	if err := Build(cfg, opts); err != nil {
		t.Fatal(err)
	}
	bundle, err := Load(os.DirFS(opts.Out))
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range bundle.Images {
		if strings.HasPrefix(image, bedrockImagePrefix) {
			continue
		}
		if !strings.Contains(image, "@sha256:") {
			t.Fatalf("image %s is not pinned", image)
		}
	}
}

func TestBuildSkipsConfiguredImageWhenPinning(t *testing.T) {
	cfg, opts := testBuild(t)
	opts.Image = "localhost:5000/bedrock:dev"
	opts.PinDigests = true
	opts.Resolve = func(_ context.Context, ref string) (string, error) {
		return "sha256:" + strings.Repeat("c", 64), nil
	}
	if err := Build(cfg, opts); err != nil {
		t.Fatal(err)
	}
	bundle, err := Load(os.DirFS(opts.Out))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Spec.Image != opts.Image {
		t.Fatalf("spec image %s, want %s", bundle.Spec.Image, opts.Image)
	}
	found := false
	for _, image := range bundle.Images {
		if image == opts.Image {
			found = true
			continue
		}
		if !strings.Contains(image, "@sha256:") {
			t.Fatalf("image %s is not pinned", image)
		}
	}
	if !found {
		t.Fatalf("images %v do not contain the unpinned configured image %s", bundle.Images, opts.Image)
	}
}

func TestBuildMovesDirCRDsAndReportsImage(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Fatal("helm is required on PATH for the release builder tests")
	}
	cfg, err := LoadBuildConfig(filepath.Join("testdata", "build", "components-dircrds.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	opts := BuildOptions{Version: "v9.9.9", Image: "ghcr.io/cloudyfolks-labs/bedrock:v9.9.9", Out: out, Helm: "helm", Root: filepath.Join("testdata", "build"), CacheDir: t.TempDir()}
	if err := Build(cfg, opts); err != nil {
		t.Fatal(err)
	}
	bundle, err := Load(os.DirFS(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Groups) != 3 || bundle.Groups[0].Name != "crds" || bundle.Groups[1].Name != "gadgets" {
		t.Fatalf("groups %+v", bundle.Groups)
	}
	if len(bundle.Groups[0].Objects) != 1 || bundle.Groups[0].Objects[0].GetName() != "gadgets.example.com" {
		t.Fatalf("crds group %+v", bundle.Groups[0].Objects)
	}
	if len(bundle.Groups[1].Objects) != 1 || bundle.Groups[1].Objects[0].GetKind() != "Deployment" {
		t.Fatalf("gadgets group %+v", bundle.Groups[1].Objects)
	}
	if _, err := os.Stat(filepath.Join(out, "manifests", "00-crds", "gadgets-crds.yaml")); err != nil {
		t.Fatal(err)
	}
	var gadgets v1alpha1.ReleaseComponent
	for _, component := range bundle.Spec.Components {
		if component.Name == "gadgets" {
			gadgets = component
		}
	}
	if gadgets.Image != "quay.io/example/primary:2" {
		t.Fatalf("component image %q", gadgets.Image)
	}
	if !slices.Contains(bundle.Images, "quay.io/example/primary:2") {
		t.Fatalf("images %v", bundle.Images)
	}
}

func TestBuildKeepsTagsForMarkedComponents(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Fatal("helm is required on PATH for the release builder tests")
	}
	cfg, err := LoadBuildConfig(filepath.Join("testdata", "build", "components-dircrds.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	resolve := func(_ context.Context, _ string) (string, error) {
		return "sha256:" + strings.Repeat("a", 64), nil
	}
	opts := BuildOptions{Version: "v9.9.9", Image: "ghcr.io/cloudyfolks-labs/bedrock:v9.9.9", Out: out, Helm: "helm", Root: filepath.Join("testdata", "build"), CacheDir: t.TempDir(), PinDigests: true, Resolve: resolve}
	if err := Build(cfg, opts); err != nil {
		t.Fatal(err)
	}
	bundle, err := Load(os.DirFS(out))
	if err != nil {
		t.Fatal(err)
	}
	containers, _, _ := unstructured.NestedSlice(bundle.Groups[1].Objects[0].Object, "spec", "template", "spec", "containers")
	image, _, _ := unstructured.NestedString(containers[0].(map[string]any), "image")
	if image != "quay.io/example/gadget-operator:2" {
		t.Fatalf("keepTags component must keep tag references, got %q", image)
	}
	for _, component := range bundle.Spec.Components {
		if component.Name == "gadgets" && !strings.Contains(component.Image, "@sha256:") {
			t.Fatalf("primary image must be pinned: %q", component.Image)
		}
	}
}

func TestBuildAppliesOverridesAndBinaryChecksums(t *testing.T) {
	cfg, opts := testBuild(t)
	binaries := t.TempDir()
	for _, arch := range []string{"amd64", "arm64"} {
		if err := os.WriteFile(filepath.Join(binaries, "bedrock-v9.9.9-linux-"+arch), []byte("bedrock "+arch), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	opts.K0sVersion = "v1.36.2+k0s.0"
	opts.UpgradeFrom = []string{"v9.9.8"}
	opts.Binaries = binaries
	if err := Build(cfg, opts); err != nil {
		t.Fatal(err)
	}
	bundle, err := Load(os.DirFS(opts.Out))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Spec.K0sVersion != "v1.36.2+k0s.0" || !slices.Equal(bundle.Spec.UpgradeFrom, []string{"v9.9.8"}) {
		t.Fatalf("overrides not applied: %+v", bundle.Spec)
	}
	want, err := FileSHA256(filepath.Join(binaries, "bedrock-v9.9.9-linux-arm64"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Spec.BedrockChecksums) != 2 || bundle.Spec.BedrockChecksums["arm64"] != want {
		t.Fatalf("bedrockChecksums %v, want arm64 %s", bundle.Spec.BedrockChecksums, want)
	}
	if len(bundle.Spec.Images) == 0 || !slices.Equal(bundle.Spec.Images, bundle.Images) {
		t.Fatalf("release images %v must equal images.txt %v", bundle.Spec.Images, bundle.Images)
	}
}

func TestBuildFailsForMissingBinary(t *testing.T) {
	cfg, opts := testBuild(t)
	opts.Binaries = t.TempDir()
	if err := Build(cfg, opts); err == nil || !strings.Contains(err.Error(), "bedrock-v9.9.9-linux-amd64") {
		t.Fatalf("expected an error naming the missing binary, got %v", err)
	}
}

func TestValidateOutRejectsDangerousPaths(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"", "/", ".", filepath.Join("..", filepath.Base(cwd))}
	for _, path := range paths {
		if err := validateOut(path); err == nil {
			t.Fatalf("validateOut(%q) = nil, want error", path)
		}
	}
	if err := validateOut(t.TempDir()); err != nil {
		t.Fatalf("validateOut(tempdir) = %v, want nil", err)
	}
}
