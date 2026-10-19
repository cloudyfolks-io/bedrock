package database

import (
	"testing"
	"time"
)

func TestParseConfigBackupMaxAge(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{})
	if err != nil || cfg.BackupMaxAge != 26*time.Hour {
		t.Fatalf("default %v %v", cfg.BackupMaxAge, err)
	}
	cfg, err = ParseConfig(map[string]string{"database.upgrade.backup-max-age": "48h"})
	if err != nil || cfg.BackupMaxAge != 48*time.Hour {
		t.Fatalf("value %v %v", cfg.BackupMaxAge, err)
	}
	if _, err := ParseConfig(map[string]string{"database.upgrade.backup-max-age": "1m"}); err == nil {
		t.Fatal("one minute accepted")
	}
}
