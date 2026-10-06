package methods

import (
	"context"
	"errors"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const dummyHash = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

var conflictRetryBackoff = wait.Backoff{Steps: 20, Duration: 20 * time.Millisecond, Factor: 1.0, Jitter: 0.1}

type passwordMethod struct {
	client   client.Client
	random   io.Reader
	settings func(context.Context) (policy.Settings, error)
}

func NewPassword(c client.Client, random io.Reader, settings func(context.Context) (policy.Settings, error)) Method {
	return passwordMethod{client: c, random: random, settings: settings}
}

func (m passwordMethod) Name() string { return v1alpha1.MethodPassword }

func (m passwordMethod) Kind() Kind { return Primary }

func (m passwordMethod) Begin(ctx context.Context, flow Flow, user v1alpha1.User) (Challenge, error) {
	return Challenge{Type: ChallengePassword, Username: user.Spec.Username}, nil
}

func (m passwordMethod) Complete(ctx context.Context, flow Flow, user v1alpha1.User, answer Answer) (Result, error) {
	settings, err := m.settings(ctx)
	if err != nil {
		return Result{}, err
	}
	locked := user.Name != "" && Locked(user.Status, flow.Now)
	hash, known := dummyHash, false
	if !locked {
		hash, known, err = m.currentHash(ctx, user)
		if err != nil {
			return Result{}, err
		}
	}
	verified, err := secret.Verify(hash, answer.Password)
	if err != nil {
		return Result{}, err
	}
	if !known || !verified {
		if user.Name != "" && !locked {
			if err := m.recordFailure(ctx, user, settings.LockoutThreshold, flow.Now); err != nil {
				return Result{}, err
			}
		}
		return Result{Failure: FailureInvalidCredentials}, nil
	}
	if err := m.recordSuccess(ctx, user, flow.Now); err != nil {
		return Result{}, err
	}
	return Result{Subject: &Subject{User: user, AMR: []string{"pwd"}}}, nil
}

func (m passwordMethod) Enroll(ctx context.Context, user v1alpha1.User, input Answer) (Enrollment, error) {
	return Enrollment{}, errors.New("methods: password has no enrollment step, use SetPassword")
}

func (m passwordMethod) currentHash(ctx context.Context, user v1alpha1.User) (string, bool, error) {
	if user.Name == "" {
		return dummyHash, false, nil
	}
	var cred v1alpha1.Credential
	name := v1alpha1.CredentialName(user.Name, v1alpha1.MethodPassword)
	if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: name}, &cred); err != nil {
		if apierrors.IsNotFound(err) {
			return dummyHash, false, nil
		}
		return "", false, err
	}
	var sec corev1.Secret
	if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: cred.Spec.SecretRef}, &sec); err != nil {
		return "", false, err
	}
	return string(sec.Data["hash"]), true, nil
}

func (m passwordMethod) recordFailure(ctx context.Context, user v1alpha1.User, threshold int, now time.Time) error {
	return retry.RetryOnConflict(conflictRetryBackoff, func() error {
		var fresh v1alpha1.User
		if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: user.Name}, &fresh); err != nil {
			return err
		}
		fresh.Status = RecordFailure(fresh.Status, threshold, now)
		return m.client.Status().Update(ctx, &fresh, client.FieldOwner(v1alpha1.AuthnFieldManager))
	})
}

func (m passwordMethod) recordSuccess(ctx context.Context, user v1alpha1.User, now time.Time) error {
	return retry.RetryOnConflict(conflictRetryBackoff, func() error {
		var fresh v1alpha1.User
		if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: user.Name}, &fresh); err != nil {
			return err
		}
		fresh.Status = RecordSuccess(fresh.Status, now)
		return m.client.Status().Update(ctx, &fresh, client.FieldOwner(v1alpha1.AuthnFieldManager))
	})
}

func SetPassword(ctx context.Context, c client.Client, random io.Reader, user v1alpha1.User, password string) error {
	hash, err := secret.Hash(random, secret.DefaultParams, password)
	if err != nil {
		return err
	}
	name := v1alpha1.CredentialName(user.Name, v1alpha1.MethodPassword)
	var cred v1alpha1.Credential
	err = c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: name}, &cred)
	if apierrors.IsNotFound(err) {
		cred = v1alpha1.Credential{
			TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Credential"},
			ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: name, Labels: map[string]string{v1alpha1.LabelKind: "Credential", v1alpha1.LabelName: v1alpha1.LabelValue(name)}},
			Spec:       v1alpha1.CredentialSpec{UserRef: user.Name, Method: v1alpha1.MethodPassword, SecretRef: name},
		}
		if err := c.Create(ctx, &cred, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
			return err
		}
		cred.TypeMeta = metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Credential"}
		if err := c.Create(ctx, secret.Object(&cred, name, map[string][]byte{"hash": []byte(hash)}), client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
			return err
		}
		cred.Status.EnrolledAt = &metav1.Time{Time: time.Now()}
		return c.Status().Update(ctx, &cred, client.FieldOwner(v1alpha1.AuthnFieldManager))
	}
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(conflictRetryBackoff, func() error {
		var sec corev1.Secret
		if err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: cred.Spec.SecretRef}, &sec); err != nil {
			return err
		}
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		sec.Data["hash"] = []byte(hash)
		return c.Update(ctx, &sec, client.FieldOwner(v1alpha1.AuthnFieldManager))
	})
}
