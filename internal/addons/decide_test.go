package addons

import (
	"errors"
	"testing"
)

func TestDecide(t *testing.T) {
	passed := Verdict{State: VerdictPassed}
	cases := []struct {
		name   string
		in     DecisionInput
		action Action
		reason string
	}{
		{"unknown addon", DecisionInput{Known: false, Enabled: true, ClusterSettled: true}, ActionHold, "UnknownAddon"},
		{"cluster not settled", DecisionInput{Known: true, Enabled: true}, ActionHold, "WaitingForCluster"},
		{"invalid values", DecisionInput{Known: true, Enabled: true, ClusterSettled: true, ValuesErr: errors.New("bad")}, ActionHold, "InvalidValues"},
		{"disabled", DecisionInput{Known: true, Enabled: false, ClusterSettled: true}, ActionPrune, "Disabled"},
		{"preflight failed", DecisionInput{Known: true, Enabled: true, ClusterSettled: true, Verdict: Verdict{State: VerdictFailed, Message: "no kvm"}}, ActionPrune, "PreflightFailed"},
		{"preflight unknown", DecisionInput{Known: true, Enabled: true, ClusterSettled: true, Verdict: Verdict{State: VerdictUnknown}}, ActionHold, "PreflightPending"},
		{"apply", DecisionInput{Known: true, Enabled: true, ClusterSettled: true, Verdict: passed}, ActionApply, ""},
		{"apply without preflight", DecisionInput{Known: true, Enabled: true, ClusterSettled: true}, ActionApply, ""},
		{"unknown addon beats values error", DecisionInput{Known: false, ClusterSettled: true, ValuesErr: errors.New("bad")}, ActionHold, "UnknownAddon"},
		{"cluster beats values error", DecisionInput{Known: true, ValuesErr: errors.New("bad")}, ActionHold, "WaitingForCluster"},
		{"values error beats disabled", DecisionInput{Known: true, ClusterSettled: true, ValuesErr: errors.New("bad")}, ActionHold, "InvalidValues"},
		{"disabled beats failed preflight", DecisionInput{Known: true, ClusterSettled: true, Verdict: Verdict{State: VerdictFailed}}, ActionPrune, "Disabled"},
	}
	for _, c := range cases {
		got := Decide(c.in)
		if got.Action != c.action || got.Reason != c.reason {
			t.Errorf("%s: got %s %q, want %s %q", c.name, got.Action, got.Reason, c.action, c.reason)
		}
	}
}
