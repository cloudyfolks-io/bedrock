package v1alpha1

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"
)

const (
	DatabaseImportNamespace = DatabasePlatformNamespace
	DatabaseImportMarker    = "bedrock.cloudyfolks.io/import"
	DatabaseImportSource    = "dadehat.cloudyfolks.com/source"
	DatabaseImportProject   = "bedrock.cloudyfolks.io/import-project"
	DatabaseImportTarget    = "bedrock.cloudyfolks.io/import-target"
	DatabaseImportTargetUID = "bedrock.cloudyfolks.io/import-target-uid"
)

func DatabaseImportBackupName(source string) string { return "import-backup-" + source }

func DatabaseImportAdminName(source string) string { return "import-admin-" + source }

func DatabaseImportTLSName(source string) string { return "import-tls-" + source }

func BoundDatabaseImportName(name, uid string) string { return name + "-" + uid }

func CheckDatabaseTLS(host string, cert, key, ca []byte) error {
	return CheckDatabaseTLSAt(host, cert, key, ca, time.Time{})
}

func CheckDatabaseTLSAt(host string, cert, key, ca []byte, at time.Time) error {
	if host == "" {
		return errors.New("the database endpoint name is missing")
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return errors.New("the database certificate and key do not match")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return errors.New("the database certificate authority is invalid")
	}
	chain := x509.NewCertPool()
	for _, raw := range pair.Certificate[1:] {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			return errors.New("a database certificate in the chain is invalid")
		}
		chain.AddCert(cert)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return errors.New("the database certificate is invalid")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: chain, CurrentTime: at}); err != nil {
		return errors.New("the database certificate is invalid for the customer endpoint or its certificate authority")
	}
	return nil
}
