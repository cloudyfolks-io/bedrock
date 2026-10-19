package settings

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
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

func TestValidatePlatformHost(t *testing.T) {
	def, _ := Lookup("platform.host")
	cases := []struct {
		value string
		valid bool
	}{
		{"", true},
		{"cloud.example.com", true},
		{"e2e.bedrock.test", true},
		{"a-b.c", true},
		{"localhost", false},
		{"192.168.5.250", false},
		{"UPPER.example.com", false},
		{"-a.example.com", false},
		{"a..example.com", false},
		{"https://cloud.example.com", false},
		{"cloud.example.com:443", false},
		{strings.Repeat("a", 64) + ".example.com", false},
	}
	for _, c := range cases {
		err := def.Validate(c.value)
		if c.valid && err != nil {
			t.Fatalf("%q must be valid: %v", c.value, err)
		}
		if !c.valid && (err == nil || err.Error() != fmt.Sprintf("value %q is not a valid DNS name", c.value)) {
			t.Fatalf("%q: %v", c.value, err)
		}
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

func TestOvercommitMemoryValidator(t *testing.T) {
	def, _ := Lookup("overcommit.memory")
	for _, value := range []string{"", "0.1", "1.0", "2.5"} {
		if err := def.Validate(value); err != nil {
			t.Errorf("%q must be valid: %v", value, err)
		}
	}
	for _, value := range []string{"0.05", "-1", "x", "NaN", "Inf"} {
		if err := def.Validate(value); err == nil {
			t.Errorf("%q must be invalid", value)
		}
	}
}

func TestBackupTargetSettingRefusesNFS(t *testing.T) {
	def, ok := Lookup("backup.target")
	if !ok {
		t.Fatal("backup.target missing")
	}
	if err := def.Validate("nfs://nas/export"); err == nil || !strings.Contains(err.Error(), "nfs targets are not supported") {
		t.Fatalf("nfs: got %v", err)
	}
	for _, ok := range []string{"", "s3://s3.example.com/bkt"} {
		if err := def.Validate(ok); err != nil {
			t.Fatalf("%q: %v", ok, err)
		}
	}
	if err := def.Validate("s3://u:p@h/b"); err == nil {
		t.Fatal("credentials in URL must fail")
	}
}

func TestEtcdScheduleSetting(t *testing.T) {
	def, ok := Lookup("backup.etcd-schedule")
	if !ok {
		t.Fatal("backup.etcd-schedule missing")
	}
	for _, valid := range []string{"", "0 2 * * *"} {
		if err := def.Validate(valid); err != nil {
			t.Fatalf("%q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"@daily", "every day"} {
		if err := def.Validate(invalid); err == nil {
			t.Fatalf("%q must fail", invalid)
		}
	}
}

func TestArchiveRecipientsSetting(t *testing.T) {
	def, ok := Lookup("backup.archive-recipients")
	if !ok {
		t.Fatal("backup.archive-recipients missing")
	}
	if def.Default != "" {
		t.Fatalf("default %q", def.Default)
	}
	const description = "age recipients (age1...) that can open every backup archive, separated by spaces or new lines. Keep their identities off the cluster."
	if def.Description != description {
		t.Fatalf("description %q", def.Description)
	}
	first, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	second, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	for _, valid := range []string{"", first.Recipient().String(), first.Recipient().String() + " " + second.Recipient().String(), first.Recipient().String() + "\n" + second.Recipient().String() + "\n"} {
		if err := def.Validate(valid); err != nil {
			t.Fatalf("%q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"age1bad", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample", first.Recipient().String() + " age1bad"} {
		err := def.Validate(invalid)
		if err == nil {
			t.Fatalf("%q must fail", invalid)
		}
		for _, field := range strings.Fields(invalid) {
			if strings.Contains(err.Error(), field) {
				t.Fatalf("%q: the error echoes %q: %v", invalid, field, err)
			}
		}
	}
}

func TestBackupTargetSettingHidesCredentials(t *testing.T) {
	def, _ := Lookup("backup.target")
	err := def.Validate("s3://AKIAIOSFODNN7EXAMPLE:wJalrXUtnFEMI/K7MDENG@host/bucket")
	if err == nil || strings.Contains(err.Error(), "AKIAIOSFODNN7EXAMPLE") || strings.Contains(err.Error(), "wJalrXUtnFEMI") {
		t.Fatalf("got %v", err)
	}
}

func TestAlertmanagerWebhookURLValidation(t *testing.T) {
	def, ok := Lookup("alertmanager.webhook-url")
	if !ok {
		t.Fatal("missing alertmanager.webhook-url")
	}
	for _, valid := range []string{"", "https://hooks.example.test/alert", "http://10.0.0.5:9000/x"} {
		if err := def.Validate(valid); err != nil {
			t.Errorf("%q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"ftp://hooks.example.test", "https://", "hooks.example.test", "not a url"} {
		if err := def.Validate(invalid); err == nil {
			t.Errorf("%q passed", invalid)
		}
	}
}

func TestCatalogHasTheObservabilityKeys(t *testing.T) {
	want := map[string]string{"audit.level": "Metadata", "loki.retention-days": "14", "kata.enabled": "true", "loki.enabled": "false", "alertmanager.webhook-url": ""}
	for key, value := range want {
		def, ok := Lookup(key)
		if !ok || def.Default != value {
			t.Errorf("%s: got %q, want %q", key, def.Default, value)
		}
	}
}

func TestCatalogDescriptionsNameTheAddonObject(t *testing.T) {
	for _, key := range []string{"kata.enabled", "loki.enabled"} {
		def, _ := Lookup(key)
		if !strings.Contains(def.Description, "Addon") {
			t.Errorf("%s: %q", key, def.Description)
		}
	}
	def, _ := Lookup("audit.level")
	if !strings.Contains(def.Description, "RequestResponse") {
		t.Errorf("audit.level: %q", def.Description)
	}
}

func TestHTTPURLErrorsDoNotRepeatTheValue(t *testing.T) {
	def, _ := Lookup("alertmanager.webhook-url")
	for _, value := range []string{
		"ftp://hooks.example.test/alert?token=s3cr3t-token",
		"hooks.example.test/alert?token=s3cr3t-token",
		"https:///alert?token=s3cr3t-token",
		"https://hooks.example.test/al\x07ert?token=s3cr3t-token",
		"https://hooks.example.test/%zz?token=s3cr3t-token",
	} {
		err := def.Validate(value)
		if err == nil {
			t.Errorf("%q accepted", value)
			continue
		}
		if strings.Contains(err.Error(), "s3cr3t-token") || strings.Contains(err.Error(), "hooks.example.test") {
			t.Errorf("the error repeats the value: %v", err)
		}
	}
}

func TestContainerRuntimeClassSetting(t *testing.T) {
	def, ok := Lookup("container.runtime-class")
	if !ok || def.Default != "kata" {
		t.Fatalf("setting %+v", def)
	}
	for _, value := range []string{"", "kata", "gvisor-1"} {
		if err := def.Validate(value); err != nil {
			t.Errorf("%q: %v", value, err)
		}
	}
	for _, value := range []string{"Kata", "kata_x", "kata x", "kata.x", strings.Repeat("a", 64)} {
		if err := def.Validate(value); err == nil {
			t.Errorf("%q passed", value)
		}
	}
}
