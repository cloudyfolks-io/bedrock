package release

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

const frrImage = "quay.io/frrouting/frr:10.7.0"

func TestFabricRendersTheFRRAgentOnGatewayNodes(t *testing.T) {
	bundle, _ := buildReal(t, "fabric")
	agent := objectNamed(groupObjects(bundle, "fabric"), "DaemonSet", "fabric-frr")
	if agent == nil {
		t.Fatal("missing DaemonSet fabric-frr")
	}
	if agent.GetNamespace() != "kube-system" {
		t.Errorf("namespace %q", agent.GetNamespace())
	}
	selector, _, _ := unstructured.NestedStringMap(agent.Object, "spec", "template", "spec", "nodeSelector")
	if selector[roles.FabricGWLabel] != "true" {
		t.Errorf("nodeSelector %v", selector)
	}
}

func TestFabricFRRImageIsInTheImageList(t *testing.T) {
	bundle, out := buildReal(t, "fabric")
	raw, err := os.ReadFile(filepath.Join(out, "images.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(raw), frrImage) || !slices.Contains(bundle.Images, frrImage) || !slices.Contains(ImagesOf(bundle.Groups), frrImage) {
		t.Fatalf("images %v", bundle.Images)
	}
}
