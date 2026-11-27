package config

import (
	"path/filepath"
	"testing"
)

func TestToSettingsTurnsTheDeschedulerOnFromTheAddonsMap(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "single-node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Addons["descheduler"] = true
	got := map[string]string{}
	for _, s := range ToSettings(WithDefaults(cfg)) {
		got[s.Name] = s.Spec.Value
	}
	if got["descheduler.enabled"] != "true" {
		t.Fatalf("descheduler.enabled = %q", got["descheduler.enabled"])
	}
}
