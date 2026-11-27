package network

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	OVNTLSSecretName      = "fabric-tls"
	OVNTLSSecretNamespace = "kube-system"

	ovnTLSKeyBits  = 2048
	ovnTLSLifetime = 10 * 365 * 24 * time.Hour
	ovnTLSCommon   = "ovn"
)

func certificateTemplate(commonName string, now time.Time, serial int64, isCA bool) *x509.Certificate {
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(ovnTLSLifetime),
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	if isCA {
		template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		template.ExtKeyUsage = nil
	}
	return template
}

func pemBlock(kind string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

func OVNTLSSecret(now time.Time) (*corev1.Secret, error) {
	caKey, err := rsa.GenerateKey(rand.Reader, ovnTLSKeyBits)
	if err != nil {
		return nil, err
	}
	caTemplate := certificateTemplate("ovn-ca", now, 1, true)
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	key, err := rsa.GenerateKey(rand.Reader, ovnTLSKeyBits)
	if err != nil {
		return nil, err
	}
	certDER, err := x509.CreateCertificate(rand.Reader, certificateTemplate(ovnTLSCommon, now, 2, false), caTemplate, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: OVNTLSSecretName, Namespace: OVNTLSSecretNamespace},
		Data: map[string][]byte{
			"cacert": pemBlock("CERTIFICATE", caDER),
			"cert":   pemBlock("CERTIFICATE", certDER),
			"key":    pemBlock("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)),
		},
	}, nil
}

func EnsureOVNTLSSecret(ctx context.Context, c client.Client, now time.Time) error {
	name := client.ObjectKey{Namespace: OVNTLSSecretNamespace, Name: OVNTLSSecretName}
	var existing corev1.Secret
	err := c.Get(ctx, name, &existing)
	if apierrors.IsNotFound(err) {
		secret, generateErr := OVNTLSSecret(now)
		if generateErr != nil {
			return generateErr
		}
		err = c.Create(ctx, secret)
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		err = c.Get(ctx, name, &existing)
	}
	if err != nil {
		return err
	}
	return validateOVNTLSSecret(existing, now)
}

func validateOVNTLSSecret(secret corev1.Secret, now time.Time) error {
	name := client.ObjectKeyFromObject(&secret)
	if !secret.DeletionTimestamp.IsZero() {
		return fmt.Errorf("OVN TLS secret %s is marked for deletion", name)
	}
	pair, err := tls.X509KeyPair(secret.Data["cert"], secret.Data["key"])
	if err != nil {
		return fmt.Errorf("OVN TLS secret %s has an invalid certificate or key: %w", name, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse the certificate in OVN TLS secret %s: %w", name, err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(secret.Data["cacert"]) {
		return fmt.Errorf("OVN TLS secret %s has no valid CA certificate", name)
	}
	intermediates := x509.NewCertPool()
	for _, raw := range pair.Certificate[1:] {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			return fmt.Errorf("parse the certificate chain in OVN TLS secret %s: %w", name, err)
		}
		intermediates.AddCert(cert)
	}
	for _, usage := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		if _, err := leaf.Verify(x509.VerifyOptions{CurrentTime: now, Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
			return fmt.Errorf("verify the certificate in OVN TLS secret %s: %w", name, err)
		}
	}
	return nil
}
