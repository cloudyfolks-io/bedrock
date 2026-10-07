package settings

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestCatalogHasPhaseOneKeys(t *testing.T) {
	required := []string{
		"platform.host", "platform.tls-mode", "platform.additional-ca", "platform.custom-tls",
		"letsencrypt.email", "letsencrypt.solver",
		"storage.replicas", "storage.network", "migration.network", "migration.parallel",
		"overcommit.cpu", "overcommit.memory",
		"backup.target", "backup.credentials-secret", "backup.etcd-schedule",
		"audit.level", "loki.retention-days", "kata.enabled", "loki.enabled",
		"images.refresh-schedule", "images.keep", "authn.require-second-factor",
		"virt.emulation", "authn.session-ttl", "authn.refresh-ttl", "authn.lockout-threshold",
	}
	for _, key := range required {
		if _, ok := Lookup(key); !ok {
			t.Fatalf("missing setting %q", key)
		}
	}
}

func TestCatalogKeysUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, def := range Catalog() {
		if seen[def.Key] {
			t.Fatalf("duplicate key %q", def.Key)
		}
		seen[def.Key] = true
	}
}

func TestValidateTLSMode(t *testing.T) {
	def, _ := Lookup("platform.tls-mode")
	if err := def.Validate("LetsEncrypt"); err != nil {
		t.Fatal(err)
	}
	if err := def.Validate("Plain"); err == nil {
		t.Fatal("Plain must be invalid")
	}
}

func TestValidateReplicas(t *testing.T) {
	def, _ := Lookup("storage.replicas")
	if err := def.Validate("3"); err != nil {
		t.Fatal(err)
	}
	if err := def.Validate("0"); err == nil {
		t.Fatal("0 must be invalid")
	}
}

func TestCatalogKeysAreValidObjectNames(t *testing.T) {
	for _, def := range Catalog() {
		if errs := validation.IsDNS1123Subdomain(def.Key); len(errs) != 0 {
			t.Fatalf("key %q is not a valid object name: %v", def.Key, errs)
		}
	}
}

func TestValidateEmulation(t *testing.T) {
	def, ok := Lookup("virt.emulation")
	if !ok || def.Default != "false" {
		t.Fatalf("definition %+v %v", def, ok)
	}
	if err := def.Validate("true"); err != nil {
		t.Fatal(err)
	}
	if err := def.Validate("maybe"); err == nil {
		t.Fatal("maybe must be invalid")
	}
}

func TestCatalogAuthnKeys(t *testing.T) {
	cases := []struct {
		key     string
		def     string
		valid   string
		invalid string
	}{
		{"authn.session-ttl", "12h", "1h", "1m"},
		{"authn.refresh-ttl", "720h", "48h", "30m"},
		{"authn.lockout-threshold", "5", "3", "0"},
	}
	for _, c := range cases {
		def, ok := Lookup(c.key)
		if !ok || def.Default != c.def {
			t.Fatalf("%s: definition %+v %v", c.key, def, ok)
		}
		if err := def.Validate(c.valid); err != nil {
			t.Fatalf("%s: %q must be valid: %v", c.key, c.valid, err)
		}
		if err := def.Validate(c.invalid); err == nil {
			t.Fatalf("%s: %q must be invalid", c.key, c.invalid)
		}
	}
}

func TestDurationValidator(t *testing.T) {
	validate := duration(5 * time.Minute)
	if err := validate(""); err != nil {
		t.Fatalf("an empty value must be valid: %v", err)
	}
	if err := validate("10m"); err != nil {
		t.Fatalf("10m must be valid: %v", err)
	}
	if err := validate("1m"); err == nil {
		t.Fatal("1m is below the 5m minimum")
	}
	if err := validate("not-a-duration"); err == nil {
		t.Fatal("an unparsable duration must be invalid")
	}
}

func TestPlatformHostHasNoDefault(t *testing.T) {
	def, ok := Lookup("platform.host")
	if !ok || def.Default != "" || def.Description != "Public domain of the platform. Required." {
		t.Fatalf("platform.host = %+v", def)
	}
}
