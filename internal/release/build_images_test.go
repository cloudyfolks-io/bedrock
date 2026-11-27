package release

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

func TestDefaultImageSettingsAreBundledOrNamedAsNotBundled(t *testing.T) {
	notBundled := []string{
		settings.DefaultBuildBuilderImage,
		settings.DefaultBuildRunImage,
	}
	cfg, err := LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	bundled := extraImagesOf(cfg)
	checked := 0
	for _, def := range settings.Catalog() {
		if !strings.HasSuffix(def.Key, "-image") {
			continue
		}
		checked++
		if slices.Contains(notBundled, def.Default) {
			if slices.Contains(bundled, def.Default) {
				t.Errorf("%s is named as not bundled and is in extraImages", def.Key)
			}
			continue
		}
		if !slices.Contains(bundled, def.Default) {
			t.Errorf("%s default %q is not in extraImages", def.Key, def.Default)
		}
	}
	if checked != 4 {
		t.Fatalf("checked %d image settings, want 4", checked)
	}
}
