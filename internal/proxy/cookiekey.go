package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	CookieKeySecret = "cookie-key"
	cookieKeyField  = "key"
	cookieKeyBytes  = 32
)

type CookieSigner struct {
	key []byte
}

func NewCookieSigner(key []byte) *CookieSigner {
	return &CookieSigner{key: key}
}

func LoadCookieSigner(ctx context.Context, c client.Client, random io.Reader) (*CookieSigner, error) {
	key := client.ObjectKey{Namespace: StoreNamespace, Name: CookieKeySecret}
	var stored corev1.Secret
	err := c.Get(ctx, key, &stored)
	if apierrors.IsNotFound(err) {
		stored, err = createCookieKey(ctx, c, random, key)
	}
	if err != nil {
		return nil, fmt.Errorf("cookie key: %w", err)
	}
	if len(stored.Data[cookieKeyField]) < cookieKeyBytes {
		return nil, fmt.Errorf("cookie key: the Secret %s holds a short key", CookieKeySecret)
	}
	return NewCookieSigner(stored.Data[cookieKeyField]), nil
}

func createCookieKey(ctx context.Context, c client.Client, random io.Reader, key client.ObjectKey) (corev1.Secret, error) {
	raw := make([]byte, cookieKeyBytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return corev1.Secret{}, err
	}
	created := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: key.Namespace, Name: key.Name,
			Labels: map[string]string{v1alpha1.LabelKind: "ProxyCookieKey", v1alpha1.LabelName: key.Name},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{cookieKeyField: raw},
	}
	err := c.Create(ctx, &created, client.FieldOwner(v1alpha1.ProxyFieldManager))
	if apierrors.IsAlreadyExists(err) {
		var winner corev1.Secret
		return winner, c.Get(ctx, key, &winner)
	}
	return created, err
}

func (s *CookieSigner) Sign(id string) string {
	return id + "." + s.mac(id)
}

func (s *CookieSigner) Verify(value string) bool {
	id, mac, found := strings.Cut(value, ".")
	if !found || id == "" {
		return false
	}
	given, err := hex.DecodeString(mac)
	if err != nil {
		return false
	}
	want, _ := hex.DecodeString(s.mac(id))
	return hmac.Equal(given, want)
}

func (s *CookieSigner) mac(id string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(id))
	return hex.EncodeToString(h.Sum(nil))
}
