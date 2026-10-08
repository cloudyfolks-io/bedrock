package images

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

var catalogNames = []string{
	"almalinux-10", "almalinux-9", "debian-12", "debian-13", "fedora-43",
	"rocky-10", "rocky-9", "ubuntu-24.04", "ubuntu-26.04", "virtio-win",
}

var catalogContainerImages = []string{
	"quay.io/containerdisks/almalinux:10",
	"quay.io/containerdisks/almalinux:9",
	"quay.io/containerdisks/debian:12",
	"quay.io/containerdisks/debian:13",
	"quay.io/containerdisks/fedora:43",
	"quay.io/containerdisks/ubuntu:24.04",
	"quay.io/containerdisks/ubuntu:26.04",
	"quay.io/kubevirt/virtio-container-disk:v1.9.0",
}

func catalogDir() string {
	return filepath.Join("..", "..", "manifests", "92-catalog")
}

func catalogFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(catalogDir(), "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("catalog files: %v %v", files, err)
	}
	return files
}

func catalogImages(t *testing.T) map[string]v1alpha1.ClusterImage {
	t.Helper()
	images := map[string]v1alpha1.ClusterImage{}
	for _, file := range catalogFiles(t) {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var img v1alpha1.ClusterImage
		if err := sigyaml.UnmarshalStrict(content, &img); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if img.Name != strings.TrimSuffix(filepath.Base(file), ".yaml") {
			t.Fatalf("%s holds the image %s", file, img.Name)
		}
		images[img.Name] = img
	}
	return images
}

func TestCatalogManifestsAreValidClusterImages(t *testing.T) {
	for name, img := range catalogImages(t) {
		source := img.Spec.Source
		sources := 0
		for _, set := range []bool{source.Registry != nil, source.HTTP != nil, source.Upload != nil, source.VolumeRef != nil} {
			if set {
				sources++
			}
		}
		if sources != 1 || img.Spec.Refresh != nil || img.Spec.SizeGi < 1 {
			t.Errorf("%s: sources %d refresh %v size %d", name, sources, img.Spec.Refresh, img.Spec.SizeGi)
		}
		want := "ghcr.io/cloudyfolks-io/images/" + img.Spec.OS.Family + ":" + img.Spec.OS.Version
		if name == "virtio-win" {
			want = "ghcr.io/cloudyfolks-io/images/virtio-win:v1.9.0"
		}
		if img.Annotations[AnnotationTarget] != want {
			t.Errorf("%s: target %q, want %q", name, img.Annotations[AnnotationTarget], want)
		}
		if img.Labels[release.ComponentLabel] != "catalog" {
			t.Errorf("%s: labels %v", name, img.Labels)
		}
	}
}

func TestCatalogFamilies(t *testing.T) {
	images := catalogImages(t)
	names := make([]string, 0, len(images))
	families := map[string]bool{}
	for name, img := range images {
		names = append(names, name)
		families[img.Spec.OS.Family] = true
	}
	slices.Sort(names)
	if !slices.Equal(names, catalogNames) {
		t.Fatalf("names %v", names)
	}
	for _, family := range []string{"ubuntu", "debian", "almalinux", "rocky", "fedora", "virtio-win"} {
		if !families[family] {
			t.Errorf("family %s is missing", family)
		}
	}
	if len(families) != 6 {
		t.Fatalf("families %v", families)
	}
}

func TestCatalogShipsNoWindowsDisk(t *testing.T) {
	for _, file := range catalogFiles(t) {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(content)), "windows") {
			t.Errorf("%s names Windows", file)
		}
	}
	for name, img := range catalogImages(t) {
		if (img.Spec.Type == v1alpha1.ImageTypeISO) != (name == "virtio-win") {
			t.Errorf("%s: type %s", name, img.Spec.Type)
		}
	}
}

func TestCatalogRegistryImagesAreTagged(t *testing.T) {
	for name, img := range catalogImages(t) {
		registry := img.Spec.Source.Registry
		if registry == nil {
			continue
		}
		_, tag, tagged := strings.Cut(registry.Image[strings.LastIndex(registry.Image, "/")+1:], ":")
		if !tagged || tag == "latest" || strings.Contains(registry.Image, "@") {
			t.Errorf("%s: image %s", name, registry.Image)
		}
	}
}

func TestCatalogHTTPSourcesHaveSHA256(t *testing.T) {
	checksum := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	for name, img := range catalogImages(t) {
		http := img.Spec.Source.HTTP
		if http == nil {
			continue
		}
		if !strings.HasPrefix(http.URL, "https://") || !checksum.MatchString(http.Checksum) {
			t.Errorf("%s: %s %s", name, http.URL, http.Checksum)
		}
	}
}

func catalogObjects(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	var objects []*unstructured.Unstructured
	for _, file := range catalogFiles(t) {
		objects = append(objects, manifestObjects(t, "manifests", "92-catalog", filepath.Base(file))...)
	}
	return objects
}

func TestCatalogImagesReachTheBundle(t *testing.T) {
	got := release.ImagesOf([]release.Group{{Name: "catalog", Objects: catalogObjects(t)}})
	if !slices.Equal(got, catalogContainerImages) {
		t.Fatalf("images %v", got)
	}
}

func TestReleaseBuildPinsTheCatalog(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "manifests", "92-catalog"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range catalogFiles(t) {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "manifests", "92-catalog", filepath.Base(file)), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := "k0sVersion: v1.36.3+k0s.0\nsupportedOS: [ubuntu-24.04]\nupgradeFrom: []\ncomponents:\n  - name: catalog\n    order: 92\n    dirs: [manifests/92-catalog]\n"
	if err := os.WriteFile(filepath.Join(root, "components.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := release.LoadBuildConfig(filepath.Join(root, "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	zero := "sha256:" + strings.Repeat("0", 64)
	out := t.TempDir()
	opts := release.BuildOptions{
		Version: "v9.9.9", Image: "ghcr.io/cloudyfolks-io/bedrock:v9.9.9", Out: out, Root: root, PinDigests: true,
		Resolve: func(context.Context, string) (string, error) { return zero, nil },
	}
	if err := release.Build(cfg, opts); err != nil {
		t.Fatal(err)
	}

	listed, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(out, "manifests", "92-catalog", "catalog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range catalogContainerImages {
		pinned := image + "@" + zero
		if !strings.Contains(string(listed), pinned+"\n") || !strings.Contains(string(manifest), "image: "+pinned+"\n") {
			t.Errorf("%s is not pinned in images.txt and the manifest", image)
		}
	}
	if strings.Contains(string(listed), "rockylinux") {
		t.Error("Rocky is an http source and must not be listed")
	}
}

func TestComponentsConfigListsTheCatalog(t *testing.T) {
	cfg, err := release.LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	orders := map[int]string{}
	var catalog *release.ComponentConfig
	for i, component := range cfg.Components {
		if other, taken := orders[component.Order]; taken {
			t.Errorf("order %d is used by %s and %s", component.Order, other, component.Name)
		}
		orders[component.Order] = component.Name
		if component.Name == "catalog" {
			catalog = &cfg.Components[i]
		}
	}
	if catalog == nil || catalog.Order != 92 || !slices.Equal(catalog.Dirs, []string{"manifests/92-catalog"}) {
		t.Fatalf("catalog component %+v", catalog)
	}
}
