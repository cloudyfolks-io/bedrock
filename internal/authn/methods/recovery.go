package methods

import (
	"context"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

const recoveryCodeLen = 10

const recoveryCodeCount = 10

func NewRecoveryCodes(random io.Reader) ([]string, error) {
	codes := make([]string, recoveryCodeCount)
	for i := range codes {
		code, err := secret.FromAlphabet(random, recoveryAlphabet, recoveryCodeLen)
		if err != nil {
			return nil, err
		}
		codes[i] = code
	}
	return codes, nil
}

func FormatRecoveryCode(code string) string {
	return code[:5] + "-" + code[5:]
}

func NormalizeRecoveryCode(input string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(input), "-", ""))
}

type recoveryMethod struct {
	client client.Client
	random io.Reader
}

func NewRecovery(c client.Client, random io.Reader) Method {
	return recoveryMethod{client: c, random: random}
}

func (m recoveryMethod) Name() string { return v1alpha1.MethodRecovery }

func (m recoveryMethod) Kind() Kind { return Second }

func (m recoveryMethod) Begin(ctx context.Context, flow Flow, user v1alpha1.User) (Challenge, error) {
	return Challenge{Type: ChallengeRecovery, Username: user.Spec.Username}, nil
}

func (m recoveryMethod) Complete(ctx context.Context, flow Flow, user v1alpha1.User, answer Answer) (Result, error) {
	name := v1alpha1.CredentialName(user.Name, v1alpha1.MethodRecovery)
	var cred v1alpha1.Credential
	if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: name}, &cred); err != nil {
		if apierrors.IsNotFound(err) {
			return Result{Failure: FailureInvalidCode}, nil
		}
		return Result{}, err
	}
	var sec corev1.Secret
	if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: cred.Spec.SecretRef}, &sec); err != nil {
		return Result{}, err
	}
	normalized := NormalizeRecoveryCode(answer.Code)
	remaining, matched, err := consumeRecoveryCode(sec.Data["codes"], normalized)
	if err != nil {
		return Result{}, err
	}
	if !matched {
		return Result{Failure: FailureInvalidCode}, nil
	}
	sec.Data["codes"] = remaining
	if err := m.client.Update(ctx, &sec, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
		if apierrors.IsConflict(err) {
			return Result{Failure: FailureInvalidCode}, nil
		}
		return Result{}, err
	}
	cred.Status.LastUsed = &metav1.Time{Time: flow.Now}
	_ = m.client.Status().Update(ctx, &cred, client.FieldOwner(v1alpha1.AuthnFieldManager))
	return Result{Subject: &Subject{User: user, AMR: []string{"otp"}}}, nil
}

func consumeRecoveryCode(stored []byte, normalized string) ([]byte, bool, error) {
	hashes := strings.Split(string(stored), "\n")
	remaining := make([]string, 0, len(hashes))
	matched := false
	for _, hash := range hashes {
		if hash == "" {
			continue
		}
		if !matched {
			ok, err := secret.Verify(hash, normalized)
			if err != nil {
				return nil, false, err
			}
			if ok {
				matched = true
				continue
			}
		}
		remaining = append(remaining, hash)
	}
	return []byte(strings.Join(remaining, "\n")), matched, nil
}

func (m recoveryMethod) Enroll(ctx context.Context, user v1alpha1.User, input Answer) (Enrollment, error) {
	codes, err := NewRecoveryCodes(m.random)
	if err != nil {
		return Enrollment{}, err
	}
	hashes := make([]string, len(codes))
	for i, code := range codes {
		hash, err := secret.Hash(m.random, secret.DefaultParams, code)
		if err != nil {
			return Enrollment{}, err
		}
		hashes[i] = hash
	}
	name := v1alpha1.CredentialName(user.Name, v1alpha1.MethodRecovery)
	var cred v1alpha1.Credential
	err = m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: name}, &cred)
	switch {
	case apierrors.IsNotFound(err):
		cred = v1alpha1.Credential{
			TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Credential"},
			ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: name, Labels: map[string]string{v1alpha1.LabelKind: "Credential", v1alpha1.LabelName: v1alpha1.LabelValue(name)}},
			Spec:       v1alpha1.CredentialSpec{UserRef: user.Name, Method: v1alpha1.MethodRecovery, SecretRef: name},
		}
		if err := m.client.Create(ctx, &cred, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
			return Enrollment{}, err
		}
		cred.TypeMeta = metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Credential"}
		if err := m.client.Create(ctx, secret.Object(&cred, name, map[string][]byte{"codes": []byte(strings.Join(hashes, "\n"))}), client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
			return Enrollment{}, err
		}
	case err != nil:
		return Enrollment{}, err
	default:
		var sec corev1.Secret
		if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: cred.Spec.SecretRef}, &sec); err != nil {
			return Enrollment{}, err
		}
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		sec.Data["codes"] = []byte(strings.Join(hashes, "\n"))
		if err := m.client.Update(ctx, &sec, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
			return Enrollment{}, err
		}
	}
	cred.Status.EnrolledAt = &metav1.Time{Time: time.Now()}
	if err := m.client.Status().Update(ctx, &cred, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
		return Enrollment{}, err
	}
	formatted := make([]string, len(codes))
	for i, code := range codes {
		formatted[i] = FormatRecoveryCode(code)
	}
	return Enrollment{Credential: cred, RecoveryCodes: formatted}, nil
}
