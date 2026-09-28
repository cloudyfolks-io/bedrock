package methods

import (
	"context"
	"crypto/rand"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecoveryCodesFormat(t *testing.T) {
	codes, err := NewRecoveryCodes(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 {
		t.Fatalf("len(codes) = %d, want 10", len(codes))
	}
	seen := map[string]bool{}
	for _, code := range codes {
		if len(code) != 10 {
			t.Fatalf("code %q must be 10 characters", code)
		}
		for _, r := range code {
			if !strings.ContainsRune(recoveryAlphabet, r) {
				t.Fatalf("code %q holds a character outside the alphabet", code)
			}
		}
		if seen[code] {
			t.Fatalf("code %q generated twice", code)
		}
		seen[code] = true
		formatted := FormatRecoveryCode(code)
		if len(formatted) != 11 || formatted[5] != '-' {
			t.Fatalf("FormatRecoveryCode(%q) = %q", code, formatted)
		}
	}
}

func TestRecoveryCodeNormalizes(t *testing.T) {
	cases := map[string]string{
		"abcde-fghjk":   "abcdefghjk",
		"ABCDE-FGHJK":   "abcdefghjk",
		" abcde-fghjk ": "abcdefghjk",
		"abcdefghjk":    "abcdefghjk",
	}
	for input, want := range cases {
		if got := NormalizeRecoveryCode(input); got != want {
			t.Fatalf("NormalizeRecoveryCode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRecoveryCodeWorksOnce(t *testing.T) {
	c, _ := startTestEnv(t)
	user := createUser(t, c, "frank")
	method := NewRecovery(c, rand.Reader)
	enrollment, err := method.Enroll(context.Background(), user, Answer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(enrollment.RecoveryCodes) != 10 {
		t.Fatalf("len(RecoveryCodes) = %d", len(enrollment.RecoveryCodes))
	}
	flow := Flow{Now: time.Now()}
	code := NormalizeRecoveryCode(enrollment.RecoveryCodes[0])

	first, err := method.Complete(context.Background(), flow, user, Answer{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	if first.Subject == nil {
		t.Fatalf("the first use must succeed: %+v", first)
	}
	second, err := method.Complete(context.Background(), flow, user, Answer{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	if second.Failure != FailureInvalidCode {
		t.Fatalf("a used code must not work twice: %+v", second)
	}
	other := NormalizeRecoveryCode(enrollment.RecoveryCodes[1])
	third, err := method.Complete(context.Background(), flow, user, Answer{Code: other})
	if err != nil {
		t.Fatal(err)
	}
	if third.Subject == nil {
		t.Fatalf("another unused code must still work: %+v", third)
	}
}

func TestRecoveryEnrollReplacesCodes(t *testing.T) {
	c, _ := startTestEnv(t)
	user := createUser(t, c, "grace")
	method := NewRecovery(c, rand.Reader)
	first, err := method.Enroll(context.Background(), user, Answer{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := method.Enroll(context.Background(), user, Answer{})
	if err != nil {
		t.Fatal(err)
	}
	flow := Flow{Now: time.Now()}
	oldResult, err := method.Complete(context.Background(), flow, user, Answer{Code: NormalizeRecoveryCode(first.RecoveryCodes[0])})
	if err != nil {
		t.Fatal(err)
	}
	if oldResult.Failure != FailureInvalidCode {
		t.Fatalf("a code from a replaced enrollment must not work: %+v", oldResult)
	}
	newResult, err := method.Complete(context.Background(), flow, user, Answer{Code: NormalizeRecoveryCode(second.RecoveryCodes[0])})
	if err != nil {
		t.Fatal(err)
	}
	if newResult.Subject == nil {
		t.Fatalf("a code from the new enrollment must work: %+v", newResult)
	}
}

func TestRecoveryReplayAcrossReplicas(t *testing.T) {
	c, _ := startTestEnv(t)
	user := createUser(t, c, "ivan")
	enrolling := NewRecovery(c, rand.Reader)
	enrollment, err := enrolling.Enroll(context.Background(), user, Answer{})
	if err != nil {
		t.Fatal(err)
	}
	flow := Flow{Now: time.Now()}

	for round := 0; round < 5; round++ {
		code := NormalizeRecoveryCode(enrollment.RecoveryCodes[round])

		start := make(chan struct{})
		results := make(chan Result, 2)
		var wg sync.WaitGroup
		for _, replica := range []Method{
			NewRecovery(c, rand.Reader),
			NewRecovery(c, rand.Reader),
		} {
			wg.Add(1)
			go func(replica Method) {
				defer wg.Done()
				<-start
				result, err := replica.Complete(context.Background(), flow, user, Answer{Code: code})
				if err != nil {
					t.Error(err)
					return
				}
				results <- result
			}(replica)
		}
		close(start)
		wg.Wait()
		close(results)

		accepted := 0
		for result := range results {
			switch {
			case result.Subject != nil:
				accepted++
			case result.Failure != FailureInvalidCode:
				t.Fatalf("round %d: unexpected failure %+v", round, result)
			}
		}
		if accepted != 1 {
			t.Fatalf("round %d: expected exactly one Complete to succeed, got %d", round, accepted)
		}
	}
}

func TestRecoveryCompleteRejectsAStaleConflictOnConsume(t *testing.T) {
	c, _ := startTestEnv(t)
	user := createUser(t, c, "kim")
	enrolling := NewRecovery(c, rand.Reader)
	enrollment, err := enrolling.Enroll(context.Background(), user, Answer{})
	if err != nil {
		t.Fatal(err)
	}
	flow := Flow{Now: time.Now()}
	code := NormalizeRecoveryCode(enrollment.RecoveryCodes[0])

	getCount := 0
	hook := &hookClient{Client: c}
	var onGet func()
	onGet = func() {
		getCount++
		if getCount == 1 {
			hook.afterGet = onGet
			return
		}
		concurrent, err := enrolling.Complete(context.Background(), flow, user, Answer{Code: code})
		if err != nil {
			t.Fatal(err)
		}
		if concurrent.Subject == nil {
			t.Fatalf("setup: the concurrent consumption via the first copy must succeed: %+v", concurrent)
		}
	}
	hook.afterGet = onGet
	racing := NewRecovery(hook, rand.Reader)

	result, err := racing.Complete(context.Background(), flow, user, Answer{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure != FailureInvalidCode {
		t.Fatalf("a stale conditional write racing a concurrent consumption must be refused: %+v", result)
	}

	again, err := enrolling.Complete(context.Background(), flow, user, Answer{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	if again.Failure != FailureInvalidCode {
		t.Fatalf("the code must stay consumed once, not revert after the conflict: %+v", again)
	}
}
