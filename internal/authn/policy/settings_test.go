package policy

import (
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

func defaultValues() map[string]string {
	values := map[string]string{}
	for _, def := range settings.Catalog() {
		values[def.Key] = def.Default
	}
	return values
}

func TestParseSettingsDefaults(t *testing.T) {
	got, err := ParseSettings(defaultValues(), "10.0.0.250")
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{RequireSecondFactor: false, SessionTTL: 12 * time.Hour, RefreshTTL: 720 * time.Hour, LockoutThreshold: 5, Host: "10-0-0-250.sslip.io", TLSMode: "SelfSigned"}
	if got != want {
		t.Fatalf("ParseSettings defaults = %+v, want %+v", got, want)
	}
}

func TestParseSettingsOverrides(t *testing.T) {
	values := defaultValues()
	values["authn.require-second-factor"] = "true"
	values["authn.session-ttl"] = "1h"
	values["authn.refresh-ttl"] = "48h"
	values["authn.lockout-threshold"] = "3"
	values["platform.host"] = "sso.example.com"
	values["platform.tls-mode"] = "Custom"
	got, err := ParseSettings(values, "10.0.0.250")
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{RequireSecondFactor: true, SessionTTL: time.Hour, RefreshTTL: 48 * time.Hour, LockoutThreshold: 3, Host: "sso.example.com", TLSMode: "Custom"}
	if got != want {
		t.Fatalf("ParseSettings overrides = %+v, want %+v", got, want)
	}
}

func TestIssuer(t *testing.T) {
	if got := Issuer(Settings{Host: "10-0-0-250.sslip.io"}); got != "https://sso.10-0-0-250.sslip.io" {
		t.Fatalf("Issuer = %q", got)
	}
	if got := Issuer(Settings{Host: "cloud.example.com"}); got != "https://sso.cloud.example.com" {
		t.Fatalf("Issuer = %q", got)
	}
}

func TestParseSettingsEmptyValuesUseDefaults(t *testing.T) {
	values := map[string]string{}
	for key := range defaultValues() {
		values[key] = ""
	}
	for _, input := range []map[string]string{values, {}} {
		got, err := ParseSettings(input, "10.0.0.250")
		if err != nil {
			t.Fatal(err)
		}
		want := Settings{RequireSecondFactor: false, SessionTTL: 12 * time.Hour, RefreshTTL: 720 * time.Hour, LockoutThreshold: 5, Host: "10-0-0-250.sslip.io", TLSMode: "SelfSigned"}
		if got != want {
			t.Fatalf("ParseSettings empty values = %+v, want %+v", got, want)
		}
	}
}
