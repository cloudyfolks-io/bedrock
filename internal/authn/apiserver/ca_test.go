package apiserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func parseCert(t *testing.T, raw []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("certificate PEM %q", raw)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestNewCA(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	certPEM, keyPEM, err := NewCA(rand.Reader, now)
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCert(t, certPEM)
	if cert.Subject.CommonName != "Bedrock CA" || !cert.IsCA || !cert.BasicConstraintsValid {
		t.Fatalf("subject %v isCA %v", cert.Subject, cert.IsCA)
	}
	if cert.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign {
		t.Fatalf("key usage %v", cert.KeyUsage)
	}
	if !cert.NotBefore.Equal(now) || !cert.NotAfter.Equal(now.AddDate(10, 0, 0)) {
		t.Fatalf("validity %v to %v", cert.NotBefore, cert.NotAfter)
	}
	public, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || public.Curve != elliptic.P256() {
		t.Fatalf("public key %T", cert.PublicKey)
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Fatalf("the CA must be self-signed: %v", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		t.Fatalf("key PEM type %v", block)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	private, ok := key.(*ecdsa.PrivateKey)
	if !ok || !private.PublicKey.Equal(public) {
		t.Fatal("the key must belong to the certificate")
	}
}

func TestCASecret(t *testing.T) {
	secret := CASecret([]byte("CERT"), []byte("KEY"))
	if secret.Namespace != "cert-manager" || secret.Name != "bedrock-ca" || secret.Type != corev1.SecretTypeTLS {
		t.Fatalf("secret meta %s/%s %s", secret.Namespace, secret.Name, secret.Type)
	}
	if secret.APIVersion != "v1" || secret.Kind != "Secret" {
		t.Fatalf("type meta %+v", secret.TypeMeta)
	}
	for key, want := range map[string]string{"tls.crt": "CERT", "tls.key": "KEY", "ca.crt": "CERT"} {
		if string(secret.Data[key]) != want {
			t.Fatalf("%s = %q", key, secret.Data[key])
		}
	}
	if secret.Labels["bedrock.cloudyfolks.io/kind"] != "PlatformCA" || secret.Labels["bedrock.cloudyfolks.io/name"] != "bedrock-ca" {
		t.Fatalf("labels %v", secret.Labels)
	}
}
