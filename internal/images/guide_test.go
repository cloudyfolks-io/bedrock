package images

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var yamlFence = regexp.MustCompile("(?s)```yaml\n(.*?)```")

func TestGuideExamplesDecode(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "docs", "images.md"))
	if err != nil {
		t.Fatal(err)
	}
	decoded := 0
	for _, match := range yamlFence.FindAllStringSubmatch(string(content), -1) {
		block := match[1]
		if !strings.HasPrefix(block, "apiVersion: bedrock.cloudyfolks.io/v1alpha1") {
			continue
		}
		var meta struct {
			Kind string `json:"kind"`
		}
		if err := sigyaml.Unmarshal([]byte(block), &meta); err != nil {
			t.Fatal(err)
		}
		switch meta.Kind {
		case "Image":
			err = sigyaml.UnmarshalStrict([]byte(block), &v1alpha1.Image{})
		case "ClusterImage":
			err = sigyaml.UnmarshalStrict([]byte(block), &v1alpha1.ClusterImage{})
		default:
			t.Fatalf("unexpected kind %q in the guide", meta.Kind)
		}
		if err != nil {
			t.Errorf("example %q does not decode: %v", block, err)
		}
		decoded++
	}
	if decoded == 0 {
		t.Fatal("the guide must hold at least one example")
	}
}
