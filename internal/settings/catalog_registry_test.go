package settings

import "testing"

func TestRegistryCatalogKeysAndDefaults(t *testing.T) {
	want := map[string]string{
		"registry.enabled":               "false",
		"registry.host":                  "",
		"registry.aliases":               "",
		"registry.admin-host":            "",
		"registry.admin-allow-cidrs":     "",
		"registry.replicas":              "2",
		"registry.db-instances":          "2",
		"registry.storage-class":         "block",
		"registry.bucket":                "bedrock-registry",
		"registry.http-proxy":            "",
		"registry.robot-prefix":          "robot$",
		"registry.gc-cron":               "0 0 3 * * *",
		"registry.proxy-caches":          "",
		"registry.default-quota-gb":      "5",
		"registry.reserved-names":        "library,admin,registry,api,docs,system,internal,platform,builders",
		"registry.reserved-prefixes":     "mirror-",
		"registry.webhook-blocked-cidrs": "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,100.64.0.0/10,169.254.0.0/16,127.0.0.0/8",
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

func TestRegistrySettingsValidate(t *testing.T) {
	cases := []struct {
		key   string
		value string
		valid bool
	}{
		{"registry.host", "registry.example.com", true},
		{"registry.host", "registry", false},
		{"registry.host", "Registry.example.com", false},
		{"registry.aliases", "a.example.com,b.example.com", true},
		{"registry.aliases", "a.example.com,", false},
		{"registry.admin-host", "harbor.example.com", true},
		{"registry.admin-host", "10.0.0.1", false},
		{"registry.admin-allow-cidrs", "10.0.0.0/8,192.168.1.0/24", true},
		{"registry.admin-allow-cidrs", "10.0.0.1", false},
		{"registry.admin-allow-cidrs", "nonsense", false},
		{"registry.replicas", "1", true},
		{"registry.replicas", "5", true},
		{"registry.replicas", "0", false},
		{"registry.replicas", "6", false},
		{"registry.replicas", "two", false},
		{"registry.db-instances", "3", true},
		{"registry.db-instances", "4", false},
		{"registry.db-instances", "0", false},
		{"registry.storage-class", "block", true},
		{"registry.storage-class", "Bad_Name", false},
		{"registry.bucket", "bedrock-registry", true},
		{"registry.bucket", "Bad", false},
		{"registry.http-proxy", "http://proxy.kube-system.svc:10808", true},
		{"registry.http-proxy", "socks5://proxy:1080", false},
		{"registry.robot-prefix", "key_", true},
		{"registry.robot-prefix", "robot$", true},
		{"registry.robot-prefix", "bad prefix", false},
		{"registry.gc-cron", "0 0 3 * * *", true},
		{"registry.gc-cron", "0 3 * * *", false},
		{"registry.proxy-caches", `[{"name":"mirror-docker","type":"docker-hub","url":"https://hub.docker.com"}]`, true},
		{"registry.proxy-caches", `[]`, true},
		{"registry.proxy-caches", `[{"name":"docker","type":"docker-hub","url":"https://hub.docker.com"}]`, false},
		{"registry.proxy-caches", `[{"name":"mirror-docker","type":"nothing","url":"https://hub.docker.com"}]`, false},
		{"registry.proxy-caches", `[{"name":"mirror-docker","type":"docker-hub","url":"http://hub.docker.com"}]`, false},
		{"registry.proxy-caches", `[{"name":"mirror-docker","type":"docker-hub","url":"https://"}]`, false},
		{"registry.proxy-caches", `[{"name":"mirror-a","type":"quay","url":"https://quay.io"},{"name":"mirror-a","type":"quay","url":"https://quay.io"}]`, false},
		{"registry.proxy-caches", `not json`, false},
		{"registry.webhook-blocked-cidrs", "10.0.0.0/8,169.254.0.0/16", true},
		{"registry.webhook-blocked-cidrs", "10.0.0.1", false},
		{"registry.default-quota-gb", "0", true},
		{"registry.default-quota-gb", "20", true},
		{"registry.default-quota-gb", "-1", false},
		{"registry.default-quota-gb", "x", false},
		{"registry.reserved-names", "library,admin", true},
		{"registry.reserved-names", "Library", false},
		{"registry.reserved-names", "admin,", false},
		{"registry.reserved-prefixes", "mirror-,x-", true},
		{"registry.reserved-prefixes", "-mirror", false},
		{"registry.reserved-prefixes", "mirror_", false},
	}
	for _, c := range cases {
		def, ok := Lookup(c.key)
		if !ok {
			t.Fatalf("missing %s", c.key)
		}
		if err := def.Validate(c.value); (err == nil) != c.valid {
			t.Errorf("%s=%q: valid=%v err=%v", c.key, c.value, c.valid, err)
		}
	}
}

func TestDatabaseFoundationSettings(t *testing.T) {
	for key, want := range map[string]string{"database.enabled": "false", "database.backup.bucket": "bedrock-database-backups"} {
		def, ok := Lookup(key)
		if !ok || def.Default != want || def.Validate(want) != nil {
			t.Errorf("%s: %+v", key, def)
		}
	}
	def, _ := Lookup("database.backup.bucket")
	if def.Validate("Bad") == nil {
		t.Error("bucket accepted an upper case name")
	}
}
