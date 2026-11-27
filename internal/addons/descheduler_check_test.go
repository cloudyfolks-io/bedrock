package addons

import (
	"strings"
	"testing"
)

func TestDeschedulerCheckPassesOnlyWithLiveMigrate(t *testing.T) {
	cases := map[string]VerdictState{
		"LiveMigrate":           VerdictPassed,
		"LiveMigrateIfPossible": VerdictFailed,
		"None":                  VerdictFailed,
		"":                      VerdictFailed,
	}
	for value, want := range cases {
		got := DeschedulerCheck(Facts{Settings: map[string]string{"virt.eviction-strategy": value}})
		if got.State != want {
			t.Errorf("%q: %+v, want %s", value, got, want)
		}
		if want == VerdictFailed && (got.Reason != "EvictionStrategyNotLiveMigrate" || !strings.Contains(got.Message, "virt.eviction-strategy") || !strings.Contains(got.Message, "LiveMigrate")) {
			t.Errorf("%q: %+v", value, got)
		}
	}
}

func TestDeschedulerDefinitionUsesTheCheck(t *testing.T) {
	def, _ := Lookup(Descheduler)
	if def.Check == nil {
		t.Fatal("descheduler has no Check")
	}
	if !strings.Contains(strings.Join(readSettings(), ","), "virt.eviction-strategy") {
		t.Error("a change of virt.eviction-strategy must wake the addons")
	}
}

func TestDecideRemovesTheDeschedulerWhenTheCheckFails(t *testing.T) {
	verdict := DeschedulerCheck(Facts{Settings: map[string]string{"virt.eviction-strategy": "None"}})
	got := Decide(DecisionInput{Known: true, Enabled: true, ClusterSettled: true, Verdict: verdict})
	if got.Action != ActionPrune || got.Reason != "PreflightFailed" || !strings.Contains(got.Message, "virt.eviction-strategy") {
		t.Fatalf("decision %+v", got)
	}
}
