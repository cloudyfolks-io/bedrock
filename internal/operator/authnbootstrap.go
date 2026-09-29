package operator

import (
	"context"
	"crypto/rand"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	authnGroup         = "authn"
	webhookTokenKey    = "token"
	webhookTokenLength = 32
)

func NewWebhookToken(random io.Reader) (string, error) {
	return secret.Base62(random, webhookTokenLength)
}

func WebhookTokenSecret(bearer string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: release.SystemNamespace,
			Name:      apiserver.TokenSecretName,
			Labels:    map[string]string{v1alpha1.LabelAuthn: "true", v1alpha1.LabelKind: "WebhookToken", v1alpha1.LabelName: apiserver.TokenSecretName},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{webhookTokenKey: []byte(bearer)},
	}
}

func ensureAuthnMaterial(ctx context.Context, c client.Client, random io.Reader, now time.Time) error {
	caKey := client.ObjectKey{Namespace: apiserver.CASecretNamespace, Name: apiserver.CASecretName}
	if err := createIfAbsent(ctx, c, caKey, func() (*corev1.Secret, error) { return newCASecret(random, now) }); err != nil {
		return err
	}
	tokenKey := client.ObjectKey{Namespace: release.SystemNamespace, Name: apiserver.TokenSecretName}
	return createIfAbsent(ctx, c, tokenKey, func() (*corev1.Secret, error) { return newWebhookTokenSecret(random) })
}

func newCASecret(random io.Reader, now time.Time) (*corev1.Secret, error) {
	certPEM, keyPEM, err := apiserver.NewCA(random, now)
	if err != nil {
		return nil, err
	}
	return apiserver.CASecret(certPEM, keyPEM), nil
}

func newWebhookTokenSecret(random io.Reader) (*corev1.Secret, error) {
	bearer, err := NewWebhookToken(random)
	if err != nil {
		return nil, err
	}
	return WebhookTokenSecret(bearer), nil
}

func createIfAbsent(ctx context.Context, c client.Client, key client.ObjectKey, desired func() (*corev1.Secret, error)) error {
	var existing corev1.Secret
	err := c.Get(ctx, key, &existing)
	if !apierrors.IsNotFound(err) {
		return err
	}
	created, err := desired()
	if err != nil {
		return err
	}
	err = c.Create(ctx, created, client.FieldOwner(v1alpha1.AuthnFieldManager))
	if apierrors.IsAlreadyExists(err) || apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func authnBootstrapHook(c client.Client) func(context.Context, release.Group) error {
	return func(ctx context.Context, group release.Group) error {
		if group.Name != authnGroup {
			return nil
		}
		return ensureAuthnMaterial(ctx, c, rand.Reader, time.Now())
	}
}
