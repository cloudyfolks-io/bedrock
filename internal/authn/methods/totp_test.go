package methods

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"testing"
	"time"

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
	now := time.Now()
	code := TOTPCode(seed, TOTPStep(now))

	replicaA := NewTOTP(c, rand.Reader, fixedIssuer("sso.example.test"))
	replicaB := NewTOTP(c, rand.Reader, fixedIssuer("sso.example.test"))

	resultA, err := replicaA.Complete(context.Background(), Flow{Now: now}, user, Answer{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	if resultA.Subject == nil {
		t.Fatalf("replica A must accept the first use: %+v", resultA)
	}
	resultB, err := replicaB.Complete(context.Background(), Flow{Now: now}, user, Answer{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	if resultB.Failure != FailureInvalidCode {
		t.Fatalf("replica B must refuse the code replica A already used: %+v", resultB)
	}
}
