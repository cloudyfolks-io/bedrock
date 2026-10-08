package tenancy

import (
	"errors"
	"testing"
)

func TestQuotaStep(t *testing.T) {
	allowed := Decision{Admitted: true, QuotaAllowed: true}
	denied := Decision{Admitted: true, Reason: ReasonTenantQuotaExceeded, Message: "no cpu left"}
	cases := []struct {
		name       string
		decision   Decision
		applyErr   error
		crdMissing bool
		want       Step
	}{
		{"applied", allowed, nil, false, Step{Ready: true, Reason: ReasonApplied}},
		{"denied", denied, nil, false, Step{Reason: ReasonTenantQuotaExceeded, Message: "no cpu left"}},
		{"denied beats a missing crd", denied, nil, true, Step{Reason: ReasonTenantQuotaExceeded, Message: "no cpu left"}},
		{"missing crd", allowed, errors.New("no match"), true, Step{Reason: ReasonQuotaCRDMissing, Message: "the ApplicationAwareResourceQuota CRD is not installed yet"}},
		{"apply error", allowed, errors.New("boom"), false, Step{Reason: ReasonApplyFailed, Message: "boom"}},
	}
	for _, c := range cases {
		if got := quotaStep(c.decision, c.applyErr, c.crdMissing); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestAccessStep(t *testing.T) {
	if got := accessStep(nil); got != (Step{Ready: true, Reason: ReasonBound}) {
		t.Fatalf("got %+v", got)
	}
	if got := accessStep(errors.New("boom")); got != (Step{Reason: ReasonApplyFailed, Message: "boom"}) {
		t.Fatalf("got %+v", got)
	}
}
