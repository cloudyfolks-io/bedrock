package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

func TestRegistryDocNamesTheContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "registry.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, name := range []string{
		reasonReady, reasonNotAvailable, reasonNameTaken, reasonNotEmpty, reasonPurge, reasonHarbor, reasonWebhook, reasonSettings,
		reasonNameInvalid, reasonNameReserved, reasonRegistryMissing, reasonRegistryNotReady,
		AnnotationAdopt, AnnotationReservedNameApproved, AnnotationResetSecret,
		"registry.default-quota-gb", "registry.reserved-names", "registry.reserved-prefixes", "registry.robot-prefix", "registry.proxy-caches",
		"count/registries.bedrock.cloudyfolks.io", "kubernetes.io/dockerconfigjson", "<name>-docker", "bedrock-registry", "harbor-admin",
		"BucketNotOwned", "ProxyCacheTypeMismatch", "forceDestroy", "keepLastN", "retainDays", "tagPattern", "retainUntagged", "schedule", "quotaGB",
		"pull", "pushPull", "manage", "platform-events",
	} {
		if !strings.Contains(doc, name) {
			t.Errorf("docs/registry.md does not mention %q", name)
		}
	}
	for _, def := range settings.Catalog() {
		if strings.HasPrefix(def.Key, "registry.") && !strings.Contains(doc, def.Key) && !strings.Contains(addonDoc(t), def.Key) {
			t.Errorf("no doc mentions the Setting %s", def.Key)
		}
	}
}

func addonDoc(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "addons.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
