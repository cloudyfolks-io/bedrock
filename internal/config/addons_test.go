package config

import (
	"path/filepath"
	"testing"
)

func TestToSettingsCarriesEveryCatalogAddon(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cnpg", "percona-ps", "registry"} {
		cfg.Spec.Addons[name] = true
	}
	cfg.Spec.Addons["unknown"] = true
	got := map[string]string{}
	for _, s := range ToSettings(WithDefaults(cfg)) {
		got[s.Name] = s.Spec.Value
	}
	for _, key := range []string{"cnpg.enabled", "percona-ps.enabled", "registry.enabled"} {
		if got[key] != "true" {
			t.Fatalf("%s = %q", key, got[key])
		}
	}
	if _, found := got["unknown.enabled"]; found {
		t.Fatal("unknown.enabled must not become a Setting")
	}
}
