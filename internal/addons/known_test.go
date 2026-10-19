package addons

import (
	"strings"
	"testing"
)

func TestKnownAddonsAreKataLokiCNPGAndPercona(t *testing.T) {
	known := Known()
	if len(known) != 4 || known[0].Name != "kata" || known[1].Name != "loki" || known[2].Name != "cnpg" || known[3].Name != "percona-ps" {
		t.Fatalf("known %+v", known)
	}
	if known[0].EnabledSetting != "kata.enabled" || len(known[0].Values) != 0 || known[0].Preflight == nil || known[0].Vars != nil {
		t.Errorf("kata %+v", known[0])
	}
	loki := known[1]
	if loki.EnabledSetting != "loki.enabled" || loki.Preflight != nil || loki.Vars == nil {
		t.Errorf("loki %+v", loki)
	}
	defaults := map[string]string{}
	for _, value := range loki.Values {
		defaults[value.Key] = value.Default
	}
	if defaults["persistence.size"] != "20Gi" || defaults["persistence.storage-class"] != "block" || len(defaults) != 2 {
		t.Errorf("defaults %v", defaults)
	}
	cnpg := known[2]
	if cnpg.EnabledSetting != "cnpg.enabled" || len(cnpg.Values) != 0 || cnpg.Vars != nil {
		t.Errorf("cnpg %+v", cnpg)
	}
	percona := known[3]
	if percona.EnabledSetting != "percona-ps.enabled" || len(percona.Values) != 0 || percona.Vars != nil || percona.Preflight != nil {
		t.Errorf("percona-ps %+v", percona)
	}
	if _, ok := Lookup("percona-ps"); !ok {
		t.Error("Lookup(percona-ps)")
	}
	if _, ok := Lookup("cnpg"); !ok {
		t.Error("Lookup(cnpg)")
	}
	if _, ok := Lookup("loki"); !ok {
		t.Error("Lookup(loki)")
	}
	if _, ok := Lookup("nothing"); ok {
		t.Error("Lookup(nothing)")
	}
}

func TestResolveValuesFillsDefaults(t *testing.T) {
	def, _ := Lookup(Loki)
	got, err := ResolveValues(def, map[string]string{"persistence.size": "5Gi"})
	if err != nil {
		t.Fatal(err)
	}
	if got["persistence.size"] != "5Gi" || got["persistence.storage-class"] != "block" {
		t.Fatalf("got %v", got)
	}
}

func TestResolveValuesRejectsUnknownKeys(t *testing.T) {
	def, _ := Lookup(Loki)
	_, err := ResolveValues(def, map[string]string{"persistence.nothing": "x"})
	if err == nil || !strings.Contains(err.Error(), "persistence.size") {
		t.Fatalf("error %v", err)
	}
	kata, _ := Lookup(Kata)
	if _, err := ResolveValues(kata, map[string]string{"x": "y"}); err == nil {
		t.Fatal("kata accepted a value")
	}
}

func TestResolveValuesValidates(t *testing.T) {
	def, _ := Lookup(Loki)
	cases := []struct{ key, value string }{
		{"persistence.size", "abc"},
		{"persistence.size", "-1Gi"},
		{"persistence.size", "0"},
		{"persistence.storage-class", "Bad_Name"},
	}
	for _, c := range cases {
		_, err := ResolveValues(def, map[string]string{c.key: c.value})
		if err == nil || !strings.Contains(err.Error(), c.key) {
			t.Errorf("%s=%s: %v", c.key, c.value, err)
		}
	}
}

func TestVarName(t *testing.T) {
	if got := VarName("loki", "persistence.storage-class"); got != "BEDROCK_LOKI_PERSISTENCE_STORAGE_CLASS" {
		t.Fatalf("got %s", got)
	}
}

func TestLokiVars(t *testing.T) {
	def, _ := Lookup(Loki)
	values := map[string]string{"persistence.size": "20Gi", "persistence.storage-class": "block"}
	for days, want := range map[string]string{"14": "336h", "1": "24h"} {
		vars, err := def.Vars(values, map[string]string{"loki.retention-days": days})
		if err != nil || vars["BEDROCK_LOKI_RETENTION"] != want || vars["BEDROCK_LOKI_PERSISTENCE_SIZE"] != "20Gi" || vars["BEDROCK_LOKI_PERSISTENCE_STORAGE_CLASS"] != "block" {
			t.Errorf("%s days: %v %v", days, vars, err)
		}
	}
	for _, days := range []string{"0", "x", ""} {
		if _, err := def.Vars(values, map[string]string{"loki.retention-days": days}); err == nil {
			t.Errorf("%q accepted", days)
		}
	}
}
