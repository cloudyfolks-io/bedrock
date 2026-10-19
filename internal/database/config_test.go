package database

import (
	"testing"
	"time"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.CapacityCPU.String() != "32" || cfg.CapacityMemory.String() != "128Gi" || cfg.CapacityPods != 150 ||
		cfg.MaxUtilization != 0.9 || !cfg.SweeperDryRun || cfg.ProvisionTimeout != 30*time.Minute || cfg.ExecTimeout != 120*time.Second {
		t.Fatalf("defaults %+v", cfg)
	}
}

func TestParseConfigReadsValues(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{
		"database.enabled": "true", "database.capacity.cpu": "8", "database.capacity.memory": "16Gi", "database.capacity.pods": "20",
		"database.max-utilization": "0.5", "database.sweeper-dry-run": "false", "database.provision-timeout": "10m", "database.exec-timeout": "45s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.CapacityCPU.String() != "8" || cfg.CapacityMemory.String() != "16Gi" || cfg.CapacityPods != 20 ||
		cfg.MaxUtilization != 0.5 || cfg.SweeperDryRun || cfg.ProvisionTimeout != 10*time.Minute || cfg.ExecTimeout != 45*time.Second {
		t.Fatalf("values %+v", cfg)
	}
}

func TestParseConfigRejectsABadValue(t *testing.T) {
	for key, value := range map[string]string{
		"database.enabled": "perhaps", "database.capacity.cpu": "-1", "database.capacity.pods": "0",
		"database.max-utilization": "2", "database.provision-timeout": "1s", "database.exec-timeout": "1s",
	} {
		if _, err := ParseConfig(map[string]string{key: value}); err == nil {
			t.Errorf("%s=%s accepted", key, value)
		}
	}
}
