package saxess

import (
	"testing"
	"time"
)

func TestChallengeLifecycle(t *testing.T) {
	now := time.Unix(100, 0)
	fresh := func() Challenge {
		t.Helper()
		c, err := NewChallenge("operator", "browser", now, time.Minute)
		if err != nil || c.State != ChallengePending || len(c.Nonce) != 64 || !c.ExpiresAt.Equal(now.Add(time.Minute)) {
			t.Fatalf("creation: %+v, %v", c, err)
		}
		return c
	}
	pending := fresh()
	if pending.Nonce == fresh().Nonce {
		t.Fatal("reused nonce")
	}
	if _, err := pending.Consume("operator", "browser", now); err != ErrInvalidChallenge {
		t.Fatalf("consume pending: %v", err)
	}
	for _, decision := range []ChallengeState{ChallengeApproved, ChallengeDenied} {
		decided, err := pending.Decide("operator", "browser", pending.Nonce, now, decision)
		want := pending
		want.State = decision
		if err != nil || decided != want {
			t.Fatalf("decide %s: %+v, %v", decision, decided, err)
		}
		if _, err := decided.Decide("operator", "browser", pending.Nonce, now, decision); err != ErrInvalidChallenge {
			t.Fatalf("repeat decision: %v", err)
		}
		consumed, err := decided.Consume("operator", "browser", now)
		if decision == ChallengeDenied {
			if err != ErrInvalidChallenge {
				t.Fatalf("consume denial: %v", err)
			}
			continue
		}
		want.State = ChallengeConsumed
		if err != nil || consumed != want {
			t.Fatalf("consume %s: %+v, %v", decision, consumed, err)
		}
		if _, err := consumed.Consume("operator", "browser", now); err != ErrInvalidChallenge {
			t.Fatalf("repeat consumption: %v", err)
		}
	}
}

func TestChallengeRejectsInvalidTransitions(t *testing.T) {
	now := time.Unix(100, 0)
	if _, err := NewChallenge("", "browser", now, time.Minute); err != ErrInvalidChallenge {
		t.Fatalf("empty operator: %v", err)
	}
	if _, err := NewChallenge("operator", "browser", now, 0); err != ErrInvalidChallenge {
		t.Fatalf("zero TTL: %v", err)
	}
	pending, err := NewChallenge("operator", "browser", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	decided, err := pending.Decide("operator", "browser", pending.Nonce, now, ChallengeApproved)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, operator, browser string
		at                      time.Time
	}{
		{"wrong operator", "other", "browser", now},
		{"wrong browser", "operator", "other", now},
		{"expiry boundary", "operator", "browser", pending.ExpiresAt},
		{"zero clock", "operator", "browser", time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pending.Decide(tc.operator, tc.browser, pending.Nonce, tc.at, ChallengeApproved); err != ErrInvalidChallenge {
				t.Fatalf("decide: %v", err)
			}
			if _, err := decided.Consume(tc.operator, tc.browser, tc.at); err != ErrInvalidChallenge {
				t.Fatalf("consume: %v", err)
			}
		})
	}
	if _, err := pending.Decide("operator", "browser", pending.Nonce, now, ChallengeConsumed); err != ErrInvalidChallenge {
		t.Fatalf("invalid decision: %v", err)
	}
	if _, err := pending.Decide("operator", "browser", "different-nonce", now, ChallengeApproved); err != ErrInvalidChallenge {
		t.Fatalf("wrong proof nonce: %v", err)
	}
	decided.Nonce = "invalid"
	if _, err := decided.Consume("operator", "browser", now); err != ErrInvalidChallenge {
		t.Fatalf("invalid stored nonce: %v", err)
	}
}
