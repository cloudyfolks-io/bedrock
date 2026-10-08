package tenancy

import (
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
)

func TestQuotaStep(t *testing.T) {
	allowed := Decision{Admitted: true, QuotaAllowed: true}
	denied := Decision{Admitted: true, Reason: ReasonTenantQuotaExceeded, Message: "no cpu left"}
	cases := []struct {
		name     string
		decision Decision
		applyErr error
		want     Step
	}{
		{"applied", allowed, nil, Step{Ready: true, Reason: ReasonApplied}},
		{"denied", denied, nil, Step{Reason: ReasonTenantQuotaExceeded, Message: "no cpu left"}},
		{"denied beats a failed apply", denied, errors.New("boom"), Step{Reason: ReasonTenantQuotaExceeded, Message: "no cpu left"}},
		{"missing crd", allowed, &meta.NoKindMatchError{}, Step{Reason: ReasonQuotaUnavailable, Message: "the ApplicationAwareResourceQuota CRD is not installed yet"}},
		{"apply error", allowed, errors.New("boom"), Step{Reason: ReasonApplyFailed, Message: "boom"}},
	}
	for _, c := range cases {
		if got := quotaStep(c.decision, c.applyErr); got != c.want {
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
