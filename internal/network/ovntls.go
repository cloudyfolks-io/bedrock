package network

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
