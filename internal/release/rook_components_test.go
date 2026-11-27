package release

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"sigs.k8s.io/yaml"
)

func componentNamed(t *testing.T, name string) ComponentConfig {
	t.Helper()
	cfg, err := LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range cfg.Components {
		if component.Name == name {
			return component
		}
	}
	t.Fatalf("no %s component", name)
	return ComponentConfig{}
}

func chartVersion(t *testing.T, chart string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", chart, "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Version string `json:"version"`
	}
	if err := yaml.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	return meta.Version
}

func TestRookComponentMatchesTheVendoredChartAndItsImages(t *testing.T) {
	rook := componentNamed(t, "rook")
	if rook.Version != "1.21.0" || chartVersion(t, rook.Chart) != rook.Version {
		t.Errorf("rook version %q, chart %q", rook.Version, chartVersion(t, rook.Chart))
	}
	if rook.Image != "quay.io/ceph/ceph:v20.2.4" {
		t.Errorf("ceph image %q", rook.Image)
	}
	want := []string{
		"quay.io/cephcsi/cephcsi:v3.18.1",
		"registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.18.0",
		"registry.k8s.io/sig-storage/csi-provisioner:v6.3.0",
		"registry.k8s.io/sig-storage/csi-snapshotter:v8.6.0",
		"registry.k8s.io/sig-storage/csi-attacher:v4.13.0",
		"registry.k8s.io/sig-storage/csi-resizer:v2.2.1",
	}
	if !reflect.DeepEqual(rook.ExtraImages, want) {
		t.Errorf("extra images %v", rook.ExtraImages)
	}
}

func TestRookCSIComponentMatchesTheVendoredChart(t *testing.T) {
	csi := componentNamed(t, "rook-csi")
	if csi.Version != "1.1.0" || chartVersion(t, csi.Chart) != csi.Version {
		t.Errorf("rook-csi version %q, chart %q", csi.Version, chartVersion(t, csi.Chart))
	}
}
