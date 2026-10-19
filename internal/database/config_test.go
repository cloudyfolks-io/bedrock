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
		cfg.MaxUtilization != 0.9 || !cfg.SweeperDryRun || cfg.ProvisionTimeout != 30*time.Minute || cfg.ExecTimeout != 120*time.Second ||
		cfg.BackupBucket != "bedrock-database-backups" || cfg.RetentionDays != 7 || cfg.RetentionAfterDelete != 168*time.Hour {
		t.Fatalf("defaults %+v", cfg)
	}
}

func TestParseConfigReadsValues(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{
		"database.enabled": "true", "database.capacity.cpu": "8", "database.capacity.memory": "16Gi", "database.capacity.pods": "20",
		"database.max-utilization": "0.5", "database.sweeper-dry-run": "false", "database.provision-timeout": "10m", "database.exec-timeout": "45s",
		"database.backup.bucket": "my-backups", "database.backup.retention-days": "30", "database.backup.retention-after-delete": "1h",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.CapacityCPU.String() != "8" || cfg.CapacityMemory.String() != "16Gi" || cfg.CapacityPods != 20 ||
		cfg.MaxUtilization != 0.5 || cfg.SweeperDryRun || cfg.ProvisionTimeout != 10*time.Minute || cfg.ExecTimeout != 45*time.Second ||
		cfg.BackupBucket != "my-backups" || cfg.RetentionDays != 30 || cfg.RetentionAfterDelete != time.Hour {
		t.Fatalf("values %+v", cfg)
	}
}

func TestParseConfigRejectsABadValue(t *testing.T) {
	for key, value := range map[string]string{
		"database.enabled": "perhaps", "database.capacity.cpu": "-1", "database.capacity.pods": "0",
		"database.max-utilization": "2", "database.provision-timeout": "1s", "database.exec-timeout": "1s", "database.backup.bucket": "A_B", "database.backup.retention-days": "0",
	} {
		if _, err := ParseConfig(map[string]string{key: value}); err == nil {
			t.Errorf("%s=%s accepted", key, value)
		}
	}
}
