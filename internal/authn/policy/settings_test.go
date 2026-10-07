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

func valuesWithHost() map[string]string {
	values := defaultValues()
	values["platform.host"] = "cloud.example.com"
	return values
}

func TestParseSettingsRefusesWithoutHost(t *testing.T) {
	for _, input := range []map[string]string{defaultValues(), {}, {"platform.host": ""}} {
		if _, err := ParseSettings(input); err == nil || err.Error() != "platform.host is not set" {
			t.Fatalf("ParseSettings = %v", err)
		}
	}
}

func TestParseSettingsDefaults(t *testing.T) {
	got, err := ParseSettings(valuesWithHost())
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{RequireSecondFactor: false, SessionTTL: 12 * time.Hour, RefreshTTL: 720 * time.Hour, LockoutThreshold: 5, Host: "cloud.example.com", TLSMode: "SelfSigned"}
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
	got, err := ParseSettings(values)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{RequireSecondFactor: true, SessionTTL: time.Hour, RefreshTTL: 48 * time.Hour, LockoutThreshold: 3, Host: "sso.example.com", TLSMode: "Custom"}
	if got != want {
		t.Fatalf("ParseSettings overrides = %+v, want %+v", got, want)
	}
}

func TestIssuer(t *testing.T) {
	if got := Issuer(Settings{Host: "cloud.example.com"}); got != "https://sso.cloud.example.com" {
		t.Fatalf("Issuer = %q", got)
	}
}

func TestParseSettingsEmptyValuesUseDefaults(t *testing.T) {
	values := map[string]string{"platform.host": "cloud.example.com"}
	for key := range defaultValues() {
		if key != "platform.host" {
			values[key] = ""
		}
	}
	for _, input := range []map[string]string{values, {"platform.host": "cloud.example.com"}} {
		got, err := ParseSettings(input)
		if err != nil {
			t.Fatal(err)
		}
		want := Settings{RequireSecondFactor: false, SessionTTL: 12 * time.Hour, RefreshTTL: 720 * time.Hour, LockoutThreshold: 5, Host: "cloud.example.com", TLSMode: "SelfSigned"}
		if got != want {
			t.Fatalf("ParseSettings empty values = %+v, want %+v", got, want)
		}
	}
}
