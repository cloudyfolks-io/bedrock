package methods

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestLDAPEscapesTheUsername(t *testing.T) {
	search := v1alpha1.LDAPUserSearch{UsernameAttribute: "uid"}
	cases := map[string]string{
		"alice":        "(&(objectClass=person)(uid=alice))",
		"alice)(uid=*": "(&(objectClass=person)(uid=alice\\29\\28uid=\\2a))",
		"a\\b":         "(&(objectClass=person)(uid=a\\5cb))",
		"a,b":          "(&(objectClass=person)(uid=a,b))",
		"a\x00b":       "(&(objectClass=person)(uid=a\\00b))",
	}
	for username, want := range cases {
		if got := UserFilter(search, username); got != want {
			t.Fatalf("UserFilter(%q) = %q, want %q", username, got, want)
		}
	}
}

func TestLDAPEscapesTheUserDNInTheGroupFilter(t *testing.T) {
	search := v1alpha1.LDAPGroupSearch{MemberAttribute: "member"}
	got := GroupFilter(search, "cn=alice (admin),ou=people,dc=example,dc=test")
	want := "(&(objectClass=groupOfNames)(member=cn=alice \\28admin\\29,ou=people,dc=example,dc=test))"
	if got != want {
		t.Fatalf("GroupFilter = %q, want %q", got, want)
	}
}

func TestLDAPRefusesPlainLDAP(t *testing.T) {
	provider := v1alpha1.IdentityProvider{Spec: v1alpha1.IdentityProviderSpec{LDAP: &v1alpha1.LDAPProvider{URL: "ldap://directory.example.test", StartTLS: false}}}
	if _, err := DialLDAP(context.Background(), provider, nil); err == nil {
		t.Fatal("plain ldap:// without startTLS must be refused")
	}
}

func TestLDAPRefusesEmptyPassword(t *testing.T) {
	c, _ := startTestEnv(t)
	dial := func(ctx context.Context, provider v1alpha1.IdentityProvider, caBundle []byte) (LDAPConn, error) {
		t.Fatal("an empty password must be refused before dialing LDAP")
		return nil, nil
	}
	method := NewLDAP(c, dial)
	request := loginRequestFor("any-provider", "alice")
	result, err := method.Complete(context.Background(), Flow{AuthRequest: request}, v1alpha1.User{}, Answer{Password: ""})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure != FailureInvalidCredentials {
		t.Fatalf("result = %+v", result)
	}
}

func selfSignedCACertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestLDAPTLSConfigNeverDisablesVerification(t *testing.T) {
	config, err := ldapTLSConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if config.InsecureSkipVerify {
		t.Fatal("LDAP TLS config must never disable certificate verification")
	}
	if config.MinVersion < tls.VersionTLS12 {
		t.Fatalf("MinVersion = %v, want at least TLS 1.2", config.MinVersion)
	}
}

func TestLDAPTLSConfigUsesProvidedCABundle(t *testing.T) {
	config, err := ldapTLSConfig(selfSignedCACertPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	if config.RootCAs == nil {
		t.Fatal("expected RootCAs to be set from the provided CA bundle")
	}
	if config.InsecureSkipVerify {
		t.Fatal("LDAP TLS config must never disable certificate verification, even with a CA bundle")
	}
}

func TestLDAPTLSConfigRejectsInvalidCABundle(t *testing.T) {
	if _, err := ldapTLSConfig([]byte("not a pem certificate")); err == nil {
		t.Fatal("expected an error for an invalid CA bundle")
	}
}

func TestDialLDAPHonorsTheContextTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	provider := v1alpha1.IdentityProvider{Spec: v1alpha1.IdentityProviderSpec{LDAP: &v1alpha1.LDAPProvider{
		URL:      "ldap://" + listener.Addr().String(),
		StartTLS: true,
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	conn, err := DialLDAP(ctx, provider, nil)
	elapsed := time.Since(start)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected an error from an LDAP server that never completes the handshake")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("DialLDAP did not honor the context timeout, took %v", elapsed)
	}
}

func selfSignedServerCert(t *testing.T, host string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP(host)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert, certPEM
}

func TestDialLDAPCancelsAPendingOperationWhenTheContextIsCancelled(t *testing.T) {
	cert, certPEM := selfSignedServerCert(t, "127.0.0.1")
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	provider := v1alpha1.IdentityProvider{Spec: v1alpha1.IdentityProviderSpec{LDAP: &v1alpha1.LDAPProvider{
		URL: "ldaps://" + listener.Addr().String(),
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := DialLDAP(ctx, provider, certPEM)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	done := make(chan error, 1)
	go func() {
		_, searchErr := conn.Search(ldap.NewSearchRequest(
			"dc=example,dc=test", ldap.ScopeBaseObject, ldap.NeverDerefAliases, 0, 0, false, "(objectClass=*)", nil, nil,
		))
		done <- searchErr
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case searchErr := <-done:
		if searchErr == nil {
			t.Fatal("expected an error once the context was cancelled mid-search")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Search did not return promptly after the context was cancelled")
	}
}

type fakeLDAPConn struct {
	bind   func(dn, password string) error
	search func(req *ldap.SearchRequest) (*ldap.SearchResult, error)
}

func (f *fakeLDAPConn) Bind(dn, password string) error { return f.bind(dn, password) }

func (f *fakeLDAPConn) Search(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
	return f.search(req)
}

func (f *fakeLDAPConn) Close() error { return nil }

func fixedDialer(conn LDAPConn) LDAPDialer {
	return func(ctx context.Context, provider v1alpha1.IdentityProvider, caBundle []byte) (LDAPConn, error) {
		return conn, nil
	}
}

func loginRequestFor(provider, username string) v1alpha1.AuthRequest {
	return v1alpha1.AuthRequest{Status: v1alpha1.AuthRequestStatus{Login: v1alpha1.LoginState{Provider: provider, Username: username}}}
}

func createLDAPProvider(t *testing.T, c client.Client, name, bindPassword string) v1alpha1.IdentityProvider {
	t.Helper()
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "bedrock-system", Name: name, Labels: map[string]string{v1alpha1.LabelAuthn: "true", v1alpha1.LabelKind: "IdentityProvider", v1alpha1.LabelName: name}},
		Data:       map[string][]byte{"bindPassword": []byte(bindPassword)},
	}
	if err := c.Create(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
	provider := v1alpha1.IdentityProvider{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "IdentityProvider"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "bedrock-system", Name: name, Labels: map[string]string{v1alpha1.LabelKind: "IdentityProvider", v1alpha1.LabelName: name}},
		Spec: v1alpha1.IdentityProviderSpec{
			Type:      v1alpha1.MethodLDAP,
			SecretRef: name,
			LDAP: &v1alpha1.LDAPProvider{
				URL:    "ldaps://directory.example.test",
				BindDN: "cn=service,dc=example,dc=test",
				UserSearch: v1alpha1.LDAPUserSearch{
					BaseDN:               "ou=people,dc=example,dc=test",
					UsernameAttribute:    "uid",
					DisplayNameAttribute: "cn",
					EmailAttribute:       "mail",
				},
				MemberOfAttribute: "memberOf",
			},
		},
	}
	if err := c.Create(context.Background(), &provider); err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestLDAPRefusesAmbiguousSearch(t *testing.T) {
	c, _ := startTestEnv(t)
	provider := createLDAPProvider(t, c, "ambiguous-ldap", "service-secret")
	cases := map[string][]*ldap.Entry{
		"zero entries": {},
		"two entries": {
			ldap.NewEntry("uid=alice,ou=people,dc=example,dc=test", map[string][]string{"uid": {"alice"}}),
			ldap.NewEntry("uid=alice2,ou=people,dc=example,dc=test", map[string][]string{"uid": {"alice"}}),
		},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			conn := &fakeLDAPConn{
				bind: func(dn, password string) error { return nil },
				search: func(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
					return &ldap.SearchResult{Entries: entries}, nil
				},
			}
			method := NewLDAP(c, fixedDialer(conn))
			request := loginRequestFor(provider.Name, "alice")
			result, err := method.Complete(context.Background(), Flow{AuthRequest: request}, v1alpha1.User{}, Answer{Password: "whatever"})
			if err != nil {
				t.Fatal(err)
			}
			if result.Failure != FailureInvalidCredentials {
				t.Fatalf("%s: result = %+v", name, result)
			}
		})
	}
}

func TestLDAPNormalizesTheUsername(t *testing.T) {
	c, _ := startTestEnv(t)
	provider := createLDAPProvider(t, c, "normalize-ldap", "service-secret")
	entry := ldap.NewEntry("uid=T.Farahani,ou=people,dc=example,dc=test", map[string][]string{
		"uid": {"T.Farahani"}, "cn": {"Taha Farahani"}, "mail": {"t.farahani@example.test"},
	})
	conn := &fakeLDAPConn{
		bind: func(dn, password string) error {
			if dn == entry.DN && password != "correct-horse" {
				return errors.New("bind refused")
			}
			return nil
		},
		search: func(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
			return &ldap.SearchResult{Entries: []*ldap.Entry{entry}}, nil
		},
	}
	method := NewLDAP(c, fixedDialer(conn))
	request := loginRequestFor(provider.Name, "t.farahani")
	result, err := method.Complete(context.Background(), Flow{AuthRequest: request, Now: time.Now()}, v1alpha1.User{}, Answer{Password: "correct-horse"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Subject == nil || result.Subject.User.Spec.Username != "t.farahani" {
		t.Fatalf("result = %+v", result)
	}
	if result.Subject.User.Name != v1alpha1.UserObjectName("t.farahani") {
		t.Fatalf("object name = %q", result.Subject.User.Name)
	}
}

func TestLDAPGroupsFromMemberOf(t *testing.T) {
	c, _ := startTestEnv(t)
	provider := createLDAPProvider(t, c, "memberof-ldap", "service-secret")
	provider.Spec.GroupMapping = []v1alpha1.GroupMapping{{External: "Operators", Group: "operators"}, {External: "Developers", Group: "developers"}}
	if err := c.Update(context.Background(), &provider); err != nil {
		t.Fatal(err)
	}
	entry := ldap.NewEntry("uid=heidi,ou=people,dc=example,dc=test", map[string][]string{
		"uid": {"heidi"}, "memberOf": {"cn=Operators,ou=groups,dc=example,dc=test", "cn=Developers,ou=groups,dc=example,dc=test"},
	})
	conn := &fakeLDAPConn{
		bind: func(dn, password string) error { return nil },
		search: func(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
			return &ldap.SearchResult{Entries: []*ldap.Entry{entry}}, nil
		},
	}
	method := NewLDAP(c, fixedDialer(conn))
	request := loginRequestFor(provider.Name, "heidi")
	result, err := method.Complete(context.Background(), Flow{AuthRequest: request, Now: time.Now()}, v1alpha1.User{}, Answer{Password: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	got := result.Subject.User.Spec.Groups
	if result.Subject == nil || len(got) != 2 || got[0] != "developers" || got[1] != "operators" {
		t.Fatalf("result = %+v", result)
	}
}

func TestLDAPGroupsFromSearch(t *testing.T) {
	c, _ := startTestEnv(t)
	provider := createLDAPProvider(t, c, "groupsearch-ldap", "service-secret")
	provider.Spec.LDAP.MemberOfAttribute = ""
	provider.Spec.LDAP.GroupSearch = &v1alpha1.LDAPGroupSearch{BaseDN: "ou=groups,dc=example,dc=test", MemberAttribute: "member", NameAttribute: "cn"}
	provider.Spec.GroupMapping = []v1alpha1.GroupMapping{{External: "qa", Group: "testers"}}
	if err := c.Update(context.Background(), &provider); err != nil {
		t.Fatal(err)
	}
	userEntry := ldap.NewEntry("uid=ivan,ou=people,dc=example,dc=test", map[string][]string{"uid": {"ivan"}})
	groupEntry := ldap.NewEntry("cn=qa,ou=groups,dc=example,dc=test", map[string][]string{"cn": {"qa"}})
	conn := &fakeLDAPConn{
		bind: func(dn, password string) error { return nil },
		search: func(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
			if req.BaseDN == "ou=groups,dc=example,dc=test" {
				return &ldap.SearchResult{Entries: []*ldap.Entry{groupEntry}}, nil
			}
			return &ldap.SearchResult{Entries: []*ldap.Entry{userEntry}}, nil
		},
	}
	method := NewLDAP(c, fixedDialer(conn))
	request := loginRequestFor(provider.Name, "ivan")
	result, err := method.Complete(context.Background(), Flow{AuthRequest: request, Now: time.Now()}, v1alpha1.User{}, Answer{Password: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Subject == nil || len(result.Subject.User.Spec.Groups) != 1 || result.Subject.User.Spec.Groups[0] != "testers" {
		t.Fatalf("result = %+v", result)
	}
}

func TestLDAPProvisionsAndUpdatesTheUser(t *testing.T) {
	c, _ := startTestEnv(t)
	provider := createLDAPProvider(t, c, "provision-ldap", "service-secret")
	entry := ldap.NewEntry("uid=judy,ou=people,dc=example,dc=test", map[string][]string{"uid": {"judy"}, "cn": {"Judy One"}, "mail": {"judy@example.test"}})
	conn := &fakeLDAPConn{
		bind: func(dn, password string) error { return nil },
		search: func(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
			return &ldap.SearchResult{Entries: []*ldap.Entry{entry}}, nil
		},
	}
	method := NewLDAP(c, fixedDialer(conn))
	request := loginRequestFor(provider.Name, "judy")

	first, err := method.Complete(context.Background(), Flow{AuthRequest: request, Now: time.Now()}, v1alpha1.User{}, Answer{Password: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Subject == nil {
		t.Fatalf("first login = %+v", first)
	}
	var created v1alpha1.User
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "bedrock-system", Name: v1alpha1.UserObjectName("judy")}, &created); err != nil {
		t.Fatal(err)
	}
	if created.Spec.DisplayName != "Judy One" || created.Spec.Source != provider.Name {
		t.Fatalf("created = %+v", created.Spec)
	}

	entry.Attributes = ldap.NewEntry(entry.DN, map[string][]string{"uid": {"judy"}, "cn": {"Judy Two"}, "mail": {"judy@example.test"}}).Attributes
	second, err := method.Complete(context.Background(), Flow{AuthRequest: request, Now: time.Now()}, created, Answer{Password: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Subject == nil {
		t.Fatalf("second login = %+v", second)
	}
	var updated v1alpha1.User
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "bedrock-system", Name: v1alpha1.UserObjectName("judy")}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.DisplayName != "Judy Two" || updated.ResourceVersion == created.ResourceVersion {
		t.Fatalf("updated = %+v", updated.Spec)
	}
}

func TestLDAPRefusesALocalUserOfTheSameName(t *testing.T) {
	c, _ := startTestEnv(t)
	provider := createLDAPProvider(t, c, "conflict-ldap", "service-secret")
	local := createUser(t, c, "kim")
	entry := ldap.NewEntry("uid=kim,ou=people,dc=example,dc=test", map[string][]string{"uid": {"kim"}})
	conn := &fakeLDAPConn{
		bind: func(dn, password string) error { return nil },
		search: func(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
			return &ldap.SearchResult{Entries: []*ldap.Entry{entry}}, nil
		},
	}
	method := NewLDAP(c, fixedDialer(conn))
	request := loginRequestFor(provider.Name, "kim")
	result, err := method.Complete(context.Background(), Flow{AuthRequest: request, Now: time.Now()}, local, Answer{Password: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure != FailureInvalidCredentials {
		t.Fatalf("an LDAP login for an existing local user of the same name must be refused: %+v", result)
	}
}

func TestLDAPRefusesAUserProvisionedByADifferentUpstreamProvider(t *testing.T) {
	c, _ := startTestEnv(t)
	provider := createLDAPProvider(t, c, "other-provider-ldap", "service-secret")
	name := v1alpha1.UserObjectName("oscar")
	existing := v1alpha1.User{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "User"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "bedrock-system", Name: name, Labels: map[string]string{v1alpha1.LabelKind: "User", v1alpha1.LabelName: name}},
		Spec:       v1alpha1.UserSpec{Username: "oscar", Source: "corp-oidc", Methods: []string{v1alpha1.MethodOIDC}},
	}
	if err := c.Create(context.Background(), &existing); err != nil {
		t.Fatal(err)
	}
	entry := ldap.NewEntry("uid=oscar,ou=people,dc=example,dc=test", map[string][]string{"uid": {"oscar"}})
	conn := &fakeLDAPConn{
		bind: func(dn, password string) error { return nil },
		search: func(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
			return &ldap.SearchResult{Entries: []*ldap.Entry{entry}}, nil
		},
	}
	method := NewLDAP(c, fixedDialer(conn))
	request := loginRequestFor(provider.Name, "oscar")
	result, err := method.Complete(context.Background(), Flow{AuthRequest: request, Now: time.Now()}, existing, Answer{Password: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure != FailureInvalidCredentials {
		t.Fatalf("an LDAP login for a user provisioned by a different upstream provider must be refused: %+v", result)
	}
}

func TestLDAPFailsWhenTheDirectoryUsernameCannotBeNormalized(t *testing.T) {
	c, _ := startTestEnv(t)
	provider := createLDAPProvider(t, c, "bad-username-ldap", "service-secret")
	entry := ldap.NewEntry("uid=weird,ou=people,dc=example,dc=test", map[string][]string{"uid": {"not a valid username"}})
	conn := &fakeLDAPConn{
		bind: func(dn, password string) error { return nil },
		search: func(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
			return &ldap.SearchResult{Entries: []*ldap.Entry{entry}}, nil
		},
	}
	method := NewLDAP(c, fixedDialer(conn))
	request := loginRequestFor(provider.Name, "weird")
	result, err := method.Complete(context.Background(), Flow{AuthRequest: request, Now: time.Now()}, v1alpha1.User{}, Answer{Password: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure != FailureProviderError {
		t.Fatalf("result = %+v, want FailureProviderError", result)
	}
}

func TestLDAPProviderForFindsTheMatchingProvider(t *testing.T) {
	c, _ := startTestEnv(t)
	provider := createLDAPProvider(t, c, "discover-ldap", "service-secret")
	entry := ldap.NewEntry("uid=frank,ou=people,dc=example,dc=test", map[string][]string{"uid": {"frank"}})
	conn := &fakeLDAPConn{
		bind: func(dn, password string) error { return nil },
		search: func(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
			return &ldap.SearchResult{Entries: []*ldap.Entry{entry}}, nil
		},
	}
	name, ok, err := LDAPProviderFor(context.Background(), c, fixedDialer(conn), "frank")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || name != provider.Name {
		t.Fatalf("name = %q, ok = %v", name, ok)
	}
}

func TestLDAPProviderForReportsNoMatchWithoutEnumeration(t *testing.T) {
	c, _ := startTestEnv(t)
	createLDAPProvider(t, c, "discover-none-ldap", "service-secret")
	conn := &fakeLDAPConn{
		bind: func(dn, password string) error { return nil },
		search: func(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
			return &ldap.SearchResult{Entries: nil}, nil
		},
	}
	name, ok, err := LDAPProviderFor(context.Background(), c, fixedDialer(conn), "ghost")
	if err != nil {
		t.Fatal(err)
	}
	if ok || name != "" {
		t.Fatalf("name = %q, ok = %v, want no match", name, ok)
	}
}

func TestLDAPToleratesARacingFirstLogin(t *testing.T) {
	c, _ := startTestEnv(t)
	provider := createLDAPProvider(t, c, "race-ldap", "service-secret")
	entry := ldap.NewEntry("uid=racer,ou=people,dc=example,dc=test", map[string][]string{"uid": {"racer"}})
	conn := &fakeLDAPConn{
		bind: func(dn, password string) error { return nil },
		search: func(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
			return &ldap.SearchResult{Entries: []*ldap.Entry{entry}}, nil
		},
	}
	request := loginRequestFor(provider.Name, "racer")

	raced := &createRaceClient{Client: c}
	raced.beforeCreate = func() {
		winner := NewLDAP(c, fixedDialer(conn))
		result, err := winner.Complete(context.Background(), Flow{AuthRequest: request, Now: time.Now()}, v1alpha1.User{}, Answer{Password: "anything"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Subject == nil {
			t.Fatalf("setup: the racing winner must succeed: %+v", result)
		}
	}
	method := NewLDAP(raced, fixedDialer(conn))
	result, err := method.Complete(context.Background(), Flow{AuthRequest: request, Now: time.Now()}, v1alpha1.User{}, Answer{Password: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Subject == nil {
		t.Fatalf("a first login racing another must still succeed once the user already exists: %+v", result)
	}
	var users v1alpha1.UserList
	if err := c.List(context.Background(), &users, client.InNamespace("bedrock-system")); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, u := range users.Items {
		if u.Spec.Username == "racer" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("want exactly one User for the racing identity, got %d", count)
	}
}

func TestLDAPConcurrentFirstLoginsProduceOneUser(t *testing.T) {
	c, _ := startTestEnv(t)
	provider := createLDAPProvider(t, c, "concurrent-ldap", "service-secret")
	entry := ldap.NewEntry("uid=concurrent,ou=people,dc=example,dc=test", map[string][]string{"uid": {"concurrent"}})
	conn := &fakeLDAPConn{
		bind: func(dn, password string) error { return nil },
		search: func(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
			return &ldap.SearchResult{Entries: []*ldap.Entry{entry}}, nil
		},
	}
	method := NewLDAP(c, fixedDialer(conn))
	request := loginRequestFor(provider.Name, "concurrent")

	var wg sync.WaitGroup
	results := make([]Result, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = method.Complete(context.Background(), Flow{AuthRequest: request, Now: time.Now()}, v1alpha1.User{}, Answer{Password: "anything"})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
	}
	for i, r := range results {
		if r.Subject == nil {
			t.Fatalf("login %d must succeed: %+v", i, r)
		}
	}
	var users v1alpha1.UserList
	if err := c.List(context.Background(), &users, client.InNamespace("bedrock-system")); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, u := range users.Items {
		if u.Spec.Username == "concurrent" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("want exactly one User, got %d", count)
	}
}
