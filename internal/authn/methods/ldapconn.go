package methods

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type LDAPConn interface {
	Bind(dn, password string) error
	Search(req *ldap.SearchRequest) (*ldap.SearchResult, error)
	Close() error
}

type LDAPDialer func(ctx context.Context, provider v1alpha1.IdentityProvider, caBundle []byte) (LDAPConn, error)

const defaultLDAPTimeout = 10 * time.Second

func ldapDialTimeout(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining
		}
	}
	return defaultLDAPTimeout
}

func ldapTLSConfig(caBundle []byte) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(caBundle) == 0 {
		return config, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBundle) {
		return nil, errors.New("methods: invalid LDAP CA bundle")
	}
	config.RootCAs = pool
	return config, nil
}

type ldapDialOutcome struct {
	conn *ldap.Conn
	err  error
}

func dialLDAPConn(provider v1alpha1.IdentityProvider, scheme string, dialer *net.Dialer, tlsConfig *tls.Config, timeout time.Duration) ldapDialOutcome {
	if scheme == "ldaps" {
		conn, err := ldap.DialURL(provider.Spec.LDAP.URL, ldap.DialWithDialer(dialer), ldap.DialWithTLSConfig(tlsConfig))
		if err != nil {
			return ldapDialOutcome{err: err}
		}
		conn.SetTimeout(timeout)
		return ldapDialOutcome{conn: conn}
	}
	conn, err := ldap.DialURL(provider.Spec.LDAP.URL, ldap.DialWithDialer(dialer))
	if err != nil {
		return ldapDialOutcome{err: err}
	}
	conn.SetTimeout(timeout)
	if err := conn.StartTLS(tlsConfig); err != nil {
		conn.Close()
		return ldapDialOutcome{err: err}
	}
	return ldapDialOutcome{conn: conn}
}

func closeWhenDialed(outcome <-chan ldapDialOutcome) {
	result := <-outcome
	if result.conn != nil {
		result.conn.Close()
	}
}

func DialLDAP(ctx context.Context, provider v1alpha1.IdentityProvider, caBundle []byte) (LDAPConn, error) {
	parsed, err := url.Parse(provider.Spec.LDAP.URL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "ldaps" && !(parsed.Scheme == "ldap" && provider.Spec.LDAP.StartTLS) {
		return nil, errors.New("methods: plain ldap:// without startTLS is refused")
	}
	tlsConfig, err := ldapTLSConfig(caBundle)
	if err != nil {
		return nil, err
	}
	timeout := ldapDialTimeout(ctx)
	dialer := &net.Dialer{Timeout: timeout}
	outcome := make(chan ldapDialOutcome, 1)
	go func() {
		outcome <- dialLDAPConn(provider, parsed.Scheme, dialer, tlsConfig, timeout)
	}()
	select {
	case <-ctx.Done():
		go closeWhenDialed(outcome)
		return nil, ctx.Err()
	case result := <-outcome:
		if result.err != nil {
			return nil, result.err
		}
		return result.conn, nil
	}
}
