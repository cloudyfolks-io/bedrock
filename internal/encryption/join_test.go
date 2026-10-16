package encryption

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

func newTestCA(t *testing.T, now time.Time) k0s.CA {
	t.Helper()
	cert, key, err := apiserver.NewCA(rand.Reader, now)
	if err != nil {
		t.Fatal(err)
	}
	return k0s.CA{Cert: cert, Key: key}
}

func newTestRSACA(t *testing.T, now time.Time) k0s.CA {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kubernetes-ca"},
		NotBefore:             now,
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return k0s.CA{
		Cert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		Key:  pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
	}
}

func parseCertificate(t *testing.T, content []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(content)
	if block == nil {
		t.Fatal("no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestJoinClientConfig(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	ca := newTestCA(t, now.Add(-time.Hour))

	cfg, err := JoinClientConfig(ca, "10.0.0.9", now)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Host != "https://10.0.0.9:6443" {
		t.Fatalf("host %q", cfg.Host)
	}
	if !bytes.Equal(cfg.CAData, ca.Cert) {
		t.Fatal("CAData differs from the CA certificate")
	}
	cert := parseCertificate(t, cfg.CertData)
	if cert.Subject.CommonName != JoinUser {
		t.Fatalf("common name %q", cert.Subject.CommonName)
	}
	if !slices.Equal(cert.Subject.Organization, []string{"system:masters"}) {
		t.Fatalf("organization %v", cert.Subject.Organization)
	}
	if want := now.Add(10 * time.Minute).Truncate(time.Second); !cert.NotAfter.Equal(want) {
		t.Fatalf("not after %s, want %s", cert.NotAfter, want)
	}
	if want := now.Add(-time.Minute).Truncate(time.Second); !cert.NotBefore.Equal(want) {
		t.Fatalf("not before %s, want %s", cert.NotBefore, want)
	}
	if !slices.Equal(cert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) {
		t.Fatalf("ext key usage %v", cert.ExtKeyUsage)
	}
	if cert.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Fatalf("key usage %v", cert.KeyUsage)
	}
	if err := cert.CheckSignatureFrom(parseCertificate(t, ca.Cert)); err != nil {
		t.Fatal(err)
	}
	if _, err := tls.X509KeyPair(cfg.CertData, cfg.KeyData); err != nil {
		t.Fatal(err)
	}
}

func TestJoinClientConfigRSACA(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	ca := newTestRSACA(t, now.Add(-time.Hour))

	cfg, err := JoinClientConfig(ca, "10.0.0.9", now)
	if err != nil {
		t.Fatal(err)
	}

	if err := parseCertificate(t, cfg.CertData).CheckSignatureFrom(parseCertificate(t, ca.Cert)); err != nil {
		t.Fatal(err)
	}
}

func TestJoinClientConfigNewSerialAndKey(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	ca := newTestCA(t, now)

	first, err := JoinClientConfig(ca, "10.0.0.9", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := JoinClientConfig(ca, "10.0.0.9", now)
	if err != nil {
		t.Fatal(err)
	}

	if parseCertificate(t, first.CertData).SerialNumber.Cmp(parseCertificate(t, second.CertData).SerialNumber) == 0 {
		t.Fatal("two certificates share a serial")
	}
	if bytes.Equal(first.KeyData, second.KeyData) {
		t.Fatal("two certificates share a key")
	}
}

func TestJoinClientConfigIPv6Host(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	cfg, err := JoinClientConfig(newTestCA(t, now), "fd00::9", now)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Host != "https://[fd00::9]:6443" {
		t.Fatalf("host %q", cfg.Host)
	}
}

func TestJoinClientConfigBadCA(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	ca := newTestCA(t, now)
	other := newTestCA(t, now)

	cases := map[string]k0s.CA{
		"mismatched key": {Cert: ca.Cert, Key: other.Key},
		"empty":          {},
		"no key":         {Cert: ca.Cert},
		"no certificate": {Key: ca.Key},
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := JoinClientConfig(bad, "10.0.0.9", now); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}
