package methods

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func fixedIssuer(v string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return v, nil }
}

func TestTOTPCodeMatchesRFC6238Vectors(t *testing.T) {
	seed := []byte("12345678901234567890")
	cases := map[int64]string{
		59:         "287082",
		1111111109: "081804",
		1111111111: "050471",
		1234567890: "005924",
		2000000000: "279037",
	}
	for unix, want := range cases {
		step := TOTPStep(time.Unix(unix, 0).UTC())
		if got := TOTPCode(seed, step); got != want {
			t.Fatalf("TOTPCode at %d (step %d) = %q, want %q", unix, step, got, want)
		}
	}
}

func TestTOTPAcceptsOneStepOfSkew(t *testing.T) {
	seed := []byte("01234567890123456789")
	now := time.Unix(1700000000, 0).UTC()
	current := TOTPStep(now)
	for _, delta := range []int64{-1, 0, 1} {
		code := TOTPCode(seed, current+delta)
		step, ok := MatchTOTP(seed, code, now, -1)
		if !ok || step != current+delta {
			t.Fatalf("delta %d: MatchTOTP = %d, %v", delta, step, ok)
		}
	}
}

func TestTOTPRefusesTwoStepsAway(t *testing.T) {
	seed := []byte("01234567890123456789")
	now := time.Unix(1700000000, 0).UTC()
	current := TOTPStep(now)
	if _, ok := MatchTOTP(seed, TOTPCode(seed, current+2), now, -1); ok {
		t.Fatal("a code two steps ahead must be refused")
	}
	if _, ok := MatchTOTP(seed, TOTPCode(seed, current-2), now, -1); ok {
		t.Fatal("a code two steps behind must be refused")
	}
}

func TestTOTPRefusesAReplayedStep(t *testing.T) {
	seed := []byte("01234567890123456789")
	now := time.Unix(1700000000, 0).UTC()
	current := TOTPStep(now)
	code := TOTPCode(seed, current)
	step, ok := MatchTOTP(seed, code, now, current-1)
	if !ok || step != current {
		t.Fatalf("first use must succeed: %d, %v", step, ok)
	}
	if _, ok := MatchTOTP(seed, code, now, step); ok {
		t.Fatal("the same step must not be accepted twice")
	}
}

func TestTOTPEnrollNeedsOneValidCode(t *testing.T) {
	c, _ := startTestEnv(t)
	user := createUser(t, c, "erin")
	method := NewTOTP(c, rand.Reader, fixedIssuer("sso.example.test"))

	enrollment, err := method.Enroll(context.Background(), user, Answer{})
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.TOTP == nil || enrollment.TOTP.OTPAuthURL == "" || enrollment.Credential.Status.EnrolledAt != nil {
		t.Fatalf("a fresh enrollment is pending: %+v", enrollment)
	}
	name := v1alpha1.CredentialName(user.Name, v1alpha1.MethodTOTP)
	var cred v1alpha1.Credential
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "bedrock-system", Name: name}, &cred); err != nil {
		t.Fatal(err)
	}
	if cred.Status.EnrolledAt != nil {
		t.Fatal("Enroll alone must not mark the credential enrolled")
	}

	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.TOTP.Secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	result, err := method.Complete(context.Background(), Flow{Now: now}, user, Answer{Code: TOTPCode(seed, TOTPStep(now))})
	if err != nil {
		t.Fatal(err)
	}
	if result.Subject == nil {
		t.Fatalf("the first valid code must complete: %+v", result)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "bedrock-system", Name: name}, &cred); err != nil {
		t.Fatal(err)
	}
	if cred.Status.EnrolledAt == nil {
		t.Fatal("the first valid code must set status.enrolledAt")
	}
}

func TestTOTPReplayAcrossReplicas(t *testing.T) {
	c, _ := startTestEnv(t)
	user := createUser(t, c, "dana")
	enrolling := NewTOTP(c, rand.Reader, fixedIssuer("sso.example.test"))
	enrollment, err := enrolling.Enroll(context.Background(), user, Answer{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.TOTP.Secret)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Unix(1700000000, 0).UTC()
	for round := int64(0); round < 5; round++ {
		now := base.Add(time.Duration(round*totpStepSeconds) * time.Second)
		code := TOTPCode(seed, TOTPStep(now))

		start := make(chan struct{})
		results := make(chan Result, 2)
		var wg sync.WaitGroup
		for _, replica := range []Method{
			NewTOTP(c, rand.Reader, fixedIssuer("sso.example.test")),
			NewTOTP(c, rand.Reader, fixedIssuer("sso.example.test")),
		} {
			wg.Add(1)
			go func(replica Method) {
				defer wg.Done()
				<-start
				result, err := replica.Complete(context.Background(), Flow{Now: now}, user, Answer{Code: code})
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

func TestTOTPEnrollReplacesUnconfirmedCredential(t *testing.T) {
	c, _ := startTestEnv(t)
	user := createUser(t, c, "frank")
	method := NewTOTP(c, rand.Reader, fixedIssuer("sso.example.test"))

	first, err := method.Enroll(context.Background(), user, Answer{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := method.Enroll(context.Background(), user, Answer{})
	if err != nil {
		t.Fatal(err)
	}
	if second.TOTP.Secret == first.TOTP.Secret {
		t.Fatal("a second enrollment attempt must generate a fresh seed")
	}

	firstSeed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(first.TOTP.Secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	stale, err := method.Complete(context.Background(), Flow{Now: now}, user, Answer{Code: TOTPCode(firstSeed, TOTPStep(now))})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Subject != nil {
		t.Fatalf("the replaced seed must no longer validate: %+v", stale)
	}

	secondSeed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(second.TOTP.Secret)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := method.Complete(context.Background(), Flow{Now: now}, user, Answer{Code: TOTPCode(secondSeed, TOTPStep(now))})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Subject == nil {
		t.Fatalf("the replacement seed must validate: %+v", fresh)
	}
}

func TestTOTPEnrollRefusesToReplaceConfirmedCredential(t *testing.T) {
	c, _ := startTestEnv(t)
	user := createUser(t, c, "grace")
	method := NewTOTP(c, rand.Reader, fixedIssuer("sso.example.test"))

	enrollment, err := method.Enroll(context.Background(), user, Answer{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.TOTP.Secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	confirmed, err := method.Complete(context.Background(), Flow{Now: now}, user, Answer{Code: TOTPCode(seed, TOTPStep(now))})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Subject == nil {
		t.Fatalf("setup: the first valid code must confirm enrollment: %+v", confirmed)
	}

	if _, err := method.Enroll(context.Background(), user, Answer{}); err == nil {
		t.Fatal("enrolling again over a confirmed credential must be refused")
	}
}

func TestTOTPCompleteRejectsAMalformedStoredSeed(t *testing.T) {
	c, _ := startTestEnv(t)
	user := createUser(t, c, "harper")
	name := v1alpha1.CredentialName(user.Name, v1alpha1.MethodTOTP)
	cred := v1alpha1.Credential{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Credential"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "bedrock-system", Name: name},
		Spec:       v1alpha1.CredentialSpec{UserRef: user.Name, Method: v1alpha1.MethodTOTP, SecretRef: name},
	}
	if err := c.Create(context.Background(), &cred); err != nil {
		t.Fatal(err)
	}
	shortSeed := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("short"))
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "bedrock-system", Name: name},
		Data:       map[string][]byte{"seed": []byte(shortSeed)},
	}
	if err := c.Create(context.Background(), sec); err != nil {
		t.Fatal(err)
	}

	method := NewTOTP(c, rand.Reader, fixedIssuer("sso.example.test"))
	if _, err := method.Complete(context.Background(), Flow{Now: time.Now()}, user, Answer{Code: "000000"}); err == nil {
		t.Fatal("a malformed stored seed must return an error, not a match result")
	}
}
