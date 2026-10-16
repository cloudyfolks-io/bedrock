package encryption

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"

	"k8s.io/client-go/rest"

	"github.com/cloudyfolks-io/bedrock/internal/k0s"
)

const (
	joinOrganization = "system:masters"
	joinAPIPort      = "6443"
	joinValidity     = 10 * time.Minute
	joinBackdate     = time.Minute
	joinSerialBits   = 128
)

func JoinClientConfig(ca k0s.CA, host string, now time.Time) (*rest.Config, error) {
	pair, err := tls.X509KeyPair(ca.Cert, ca.Key)
	if err != nil {
		return nil, fmt.Errorf("load cluster CA: %w", err)
	}
	signer, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("cluster CA key cannot sign")
	}
	caCert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse cluster CA certificate: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), joinSerialBits))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: JoinUser, Organization: []string{joinOrganization}},
		NotBefore:    now.Add(-joinBackdate),
		NotAfter:     now.Add(joinValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, signer)
	if err != nil {
		return nil, fmt.Errorf("sign join client certificate: %w", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &rest.Config{
		Host: "https://" + net.JoinHostPort(host, joinAPIPort),
		TLSClientConfig: rest.TLSClientConfig{
			CAData:   ca.Cert,
			CertData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			KeyData:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}),
		},
	}, nil
}
