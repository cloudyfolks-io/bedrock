package settings

import "testing"

func TestDatabaseCatalogKeysAndDefaults(t *testing.T) {
	want := map[string]string{
		"cnpg.enabled":                           "false",
		"database.enabled":                       "false",
		"database.capacity.cpu":                  "32",
		"database.capacity.memory":               "128Gi",
		"database.capacity.pods":                 "150",
		"database.max-utilization":               "0.9",
		"database.sweeper-dry-run":               "true",
		"database.provision-timeout":             "30m",
		"database.exec-timeout":                  "120s",
		"database.backup.bucket":                 "bedrock-database-backups",
		"database.backup.retention-days":         "7",
		"database.backup.retention-after-delete": "168h",
	}
	for key, def := range want {
		got, ok := Lookup(key)
		if !ok || got.Default != def || got.Description == "" {
			t.Fatalf("setting %q: %+v", key, got)
		}
		if err := got.Validate(got.Default); err != nil {
			t.Fatalf("the default of %q is invalid: %v", key, err)
		}
	}
}

func TestDatabaseSettingsValidate(t *testing.T) {
	cases := []struct {
		key   string
		value string
		valid bool
	}{
		{"database.enabled", "true", true},
		{"database.enabled", "maybe", false},
		{"database.capacity.cpu", "500m", true},
		{"database.capacity.cpu", "0", false},
		{"database.capacity.cpu", "-1", false},
		{"database.capacity.cpu", "many", false},
		{"database.capacity.memory", "64Gi", true},
		{"database.capacity.memory", "0Gi", false},
		{"database.capacity.pods", "1", true},
		{"database.capacity.pods", "0", false},
		{"database.capacity.pods", "100001", false},
		{"database.capacity.pods", "x", false},
		{"database.max-utilization", "1", true},
		{"database.max-utilization", "0.5", true},
		{"database.max-utilization", "0", false},
		{"database.max-utilization", "1.1", false},
		{"database.max-utilization", "NaN", false},
		{"database.sweeper-dry-run", "false", true},
		{"database.sweeper-dry-run", "no way", false},
		{"database.provision-timeout", "45m", true},
		{"database.provision-timeout", "30s", false},
		{"database.provision-timeout", "soon", false},
		{"database.backup.bucket", "my-backups", true},
		{"database.backup.bucket", "ab", false},
		{"database.backup.bucket", "Upper", false},
		{"database.backup.bucket", "under_score", false},
		{"database.backup.bucket", "-lead", false},
		{"database.backup.retention-days", "1", true},
		{"database.backup.retention-days", "365", true},
		{"database.backup.retention-days", "0", false},
		{"database.backup.retention-days", "366", false},
		{"database.backup.retention-days", "week", false},
		{"database.backup.retention-after-delete", "0s", true},
		{"database.backup.retention-after-delete", "72h", true},
		{"database.backup.retention-after-delete", "-1h", false},
		{"database.backup.retention-after-delete", "soon", false},
		{"database.exec-timeout", "30s", true},
		{"database.exec-timeout", "1s", false},
		{"database.exec-timeout", "1500ms", false},
		{"database.exec-timeout", "11m", false},
	}
	for _, c := range cases {
		def, ok := Lookup(c.key)
		if !ok {
			t.Fatalf("missing setting %q", c.key)
		}
		if err := def.Validate(c.value); (err == nil) != c.valid {
			t.Errorf("%s=%q: valid=%v, err=%v", c.key, c.value, c.valid, err)
		}
	}
}
