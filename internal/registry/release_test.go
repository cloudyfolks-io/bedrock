package registry

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sigyaml "sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/internal/objectstorage"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func readValues(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "release", "values", "registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := sigyaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	return values
}

func at(t *testing.T, tree map[string]any, path ...string) any {
	t.Helper()
	var node any = tree
	for _, key := range path {
		next, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("no %v in the values", path)
		}
		node = next[key]
	}
	return node
}

func TestTheRegistryComponentIsAnAddonInItsNamespace(t *testing.T) {
	cfg, err := release.LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, component := range cfg.Components {
		if component.Name == "registry" {
			found = true
			if !component.Addon || component.Namespace != Namespace || component.Chart != "charts/harbor" || component.Version != "1.18.4" || !component.NoHooks {
				t.Errorf("component %+v", component)
			}
		}
	}
	if !found {
		t.Fatal("no registry component")
	}
}

func TestTheDatabaseImageIsInTheBundleImageList(t *testing.T) {
	cfg, err := release.LoadBuildConfig(filepath.Join("..", "..", "release", "components.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range cfg.Components {
		if component.Name == "cnpg" && slices.Contains(component.ExtraImages, DatabaseImage) {
			return
		}
	}
	t.Fatalf("%s is not an extra image of the cnpg component", DatabaseImage)
}

func TestTheValuesPointAtTheStoreAndTheRuntimeSecrets(t *testing.T) {
	values := readValues(t)
	want := map[string]string{
		"existingSecretAdminPassword":                     adminSecret,
		"existingSecretSecretKey":                         coreKeysSecret,
		"core.existingSecret":                             coreKeysSecret,
		"core.existingXsrfSecret":                         coreKeysSecret,
		"core.secretName":                                 tokenCertificateName,
		"jobservice.existingSecret":                       jobserviceKeysSecret,
		"registry.existingSecret":                         registryKeysSecret,
		"registry.credentials.existingSecret":             registryCredentialsSecret,
		"registry.credentials.username":                   registryUsername,
		"database.external.existingSecret":                databaseCredentialsSecret,
		"database.external.username":                      databaseUser,
		"database.external.coreDatabase":                  databaseName,
		"database.external.host":                          databaseClusterName + "-rw." + Namespace + ".svc",
		"persistence.imageChartStorage.s3.existingSecret": storeIdentityName,
		"persistence.imageChartStorage.s3.regionendpoint": objectstorage.Endpoint,
		"persistence.imageChartStorage.s3.region":         objectstorage.Region,
		"expose.type":                                     "clusterIP",
		"fullnameOverride":                                "harbor",
		"persistence.imageChartStorage.type":              "s3",
		"database.type":                                   "external",
		"redis.type":                                      "internal",
	}
	for path, expected := range want {
		if got := at(t, values, strings.Split(path, ".")...); got != expected {
			t.Errorf("%s = %v, want %v", path, got, expected)
		}
	}
	if at(t, values, "trivy", "enabled") != false || at(t, values, "persistence", "imageChartStorage", "disableredirect") != true {
		t.Error("trivy must be off and the redirect must be disabled")
	}
}
