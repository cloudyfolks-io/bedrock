package keys

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	secretKey = "key.pem"
	pemType   = "PRIVATE KEY"
)

func Load(ctx context.Context, c client.Reader) ([]Key, error) {
	var list v1alpha1.SigningKeyList
	if err := c.List(ctx, &list, client.InNamespace(release.SystemNamespace)); err != nil {
		return nil, err
	}
	loaded := make([]Key, 0, len(list.Items))
	for _, item := range list.Items {
		var stored corev1.Secret
		err := c.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: item.Spec.SecretRef}, &stored)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		private, err := decodePrivateKey(stored.Data[secretKey])
		if err != nil {
			continue
		}
		loaded = append(loaded, Key{ID: item.Name, Private: private, NotBefore: item.Spec.NotBefore.Time, RetireAfter: item.Spec.RetireAfter.Time})
	}
	return loaded, nil
}

func Save(ctx context.Context, c client.Client, key Key) error {
	encoded, err := encodePrivateKey(key.Private)
	if err != nil {
		return err
	}
	signing := v1alpha1.SigningKey{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.ID,
			Namespace: release.SystemNamespace,
			Labels:    map[string]string{v1alpha1.LabelKind: "SigningKey", v1alpha1.LabelName: key.ID},
		},
		Spec: v1alpha1.SigningKeySpec{
			Algorithm:   v1alpha1.AlgorithmES256,
			SecretRef:   key.ID,
			NotBefore:   metav1.NewTime(key.NotBefore),
			RetireAfter: metav1.NewTime(key.RetireAfter),
		},
	}
	owner := client.FieldOwner(v1alpha1.AuthnFieldManager)
	if err := c.Create(ctx, &signing, owner); err != nil {
		return err
	}
	signing.TypeMeta = metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "SigningKey"}
	if err := c.Create(ctx, secret.Object(&signing, key.ID, map[string][]byte{secretKey: encoded}), owner); err != nil {
		return errors.Join(err, c.Delete(ctx, &signing))
	}
	return nil
}

func Delete(ctx context.Context, c client.Client, key Key) error {
	meta := metav1.ObjectMeta{Name: key.ID, Namespace: release.SystemNamespace}
	return errors.Join(
		client.IgnoreNotFound(c.Delete(ctx, &corev1.Secret{ObjectMeta: meta})),
		client.IgnoreNotFound(c.Delete(ctx, &v1alpha1.SigningKey{ObjectMeta: meta})),
	)
}

func encodePrivateKey(private *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: der}), nil
}

func decodePrivateKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != pemType {
		return nil, errors.New("key.pem is not a PKCS#8 PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	private, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || private.Curve != elliptic.P256() {
		return nil, errors.New("key.pem is not an ECDSA P-256 key")
	}
	return private, nil
}
