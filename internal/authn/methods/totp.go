package methods

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const totpStepSeconds = 30

const totpSeedLen = 20

func TOTPStep(now time.Time) int64 {
	return now.Unix() / totpStepSeconds
}

func TOTPCode(seed []byte, step int64) string {
	code, err := hotp.GenerateCodeCustom(base32Seed(seed), uint64(step), hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		panic(err)
	}
	return code
}

func base32Seed(seed []byte) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed)
}

func MatchTOTP(seed []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	current := TOTPStep(now)
	for _, step := range []int64{current - 1, current, current + 1} {
		if step <= lastStep {
			continue
		}
		if secret.Equal(TOTPCode(seed, step), code) {
			return step, true
		}
	}
	return 0, false
}

func OTPAuthURL(issuer, username string, seed []byte) string {
	values := url.Values{}
	values.Set("secret", base32Seed(seed))
	values.Set("issuer", issuer)
	values.Set("algorithm", "SHA1")
	values.Set("digits", "6")
	values.Set("period", "30")
	return fmt.Sprintf("otpauth://totp/%s?%s", url.PathEscape(issuer+":"+username), values.Encode())
}

type totpMethod struct {
	client client.Client
	random io.Reader
	issuer func(context.Context) (string, error)
}

func NewTOTP(c client.Client, random io.Reader, issuer func(context.Context) (string, error)) Method {
	return totpMethod{client: c, random: random, issuer: issuer}
}

func (m totpMethod) Name() string { return v1alpha1.MethodTOTP }

func (m totpMethod) Kind() Kind { return Second }

func (m totpMethod) Begin(ctx context.Context, flow Flow, user v1alpha1.User) (Challenge, error) {
	return Challenge{Type: ChallengeTOTP, Username: user.Spec.Username}, nil
}

func (m totpMethod) Complete(ctx context.Context, flow Flow, user v1alpha1.User, answer Answer) (Result, error) {
	name := v1alpha1.CredentialName(user.Name, v1alpha1.MethodTOTP)
	var cred v1alpha1.Credential
	if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: name}, &cred); err != nil {
		if apierrors.IsNotFound(err) {
			return Result{Failure: FailureInvalidCode}, nil
		}
		return Result{}, err
	}
	seed, err := m.readSeed(ctx, cred)
	if err != nil {
		return Result{}, err
	}
	step, ok := MatchTOTP(seed, answer.Code, flow.Now, cred.Status.LastStep)
	if !ok {
		return Result{Failure: FailureInvalidCode}, nil
	}
	cred.Status.LastStep = step
	cred.Status.LastUsed = &metav1.Time{Time: flow.Now}
	if cred.Status.EnrolledAt == nil {
		cred.Status.EnrolledAt = &metav1.Time{Time: flow.Now}
	}
	if err := m.client.Status().Update(ctx, &cred, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
		if apierrors.IsConflict(err) {
			return Result{Failure: FailureInvalidCode}, nil
		}
		return Result{}, err
	}
	return Result{Subject: &Subject{User: user, AMR: []string{"otp"}}}, nil
}

func (m totpMethod) Enroll(ctx context.Context, user v1alpha1.User, input Answer) (Enrollment, error) {
	name := v1alpha1.CredentialName(user.Name, v1alpha1.MethodTOTP)
	seed := make([]byte, totpSeedLen)
	if _, err := io.ReadFull(m.random, seed); err != nil {
		return Enrollment{}, err
	}
	cred := v1alpha1.Credential{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Credential"},
		ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: name, Labels: map[string]string{v1alpha1.LabelKind: "Credential", v1alpha1.LabelName: name}},
		Spec:       v1alpha1.CredentialSpec{UserRef: user.Name, Method: v1alpha1.MethodTOTP, SecretRef: name},
	}
	if err := m.client.Create(ctx, &cred); err != nil {
		return Enrollment{}, err
	}
	cred.TypeMeta = metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Credential"}
	if err := m.client.Create(ctx, secret.Object(&cred, name, map[string][]byte{"seed": []byte(base32Seed(seed))})); err != nil {
		return Enrollment{}, err
	}
	issuer, err := m.issuer(ctx)
	if err != nil {
		return Enrollment{}, err
	}
	return Enrollment{
		Credential: cred,
		TOTP:       &TOTPEnrollment{OTPAuthURL: OTPAuthURL(issuer, user.Spec.Username, seed), Secret: base32Seed(seed)},
	}, nil
}

func (m totpMethod) readSeed(ctx context.Context, cred v1alpha1.Credential) ([]byte, error) {
	var sec corev1.Secret
	if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: cred.Spec.SecretRef}, &sec); err != nil {
		return nil, err
	}
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(string(sec.Data["seed"]))
	if err != nil {
		return nil, errors.New("methods: malformed totp seed")
	}
	return seed, nil
}
