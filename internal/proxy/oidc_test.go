package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type mutableHost struct {
	mu    sync.Mutex
	value string
}

func (m *mutableHost) Get(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.value, nil
}

func (m *mutableHost) Set(value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.value = value
}

func newOIDCFor(fake *fakeAuthn, host *mutableHost, clientSecret string) *OIDC {
	return NewOIDC(OIDCConfig{
		Host:            host.Get,
		ClientSecret:    func(context.Context) (string, error) { return clientSecret, nil },
		AuthnAddress:    fake.Address(),
		AuthnServerName: fakeAuthnServerName,
		AuthnCA:         fake.caPEM,
		Clock:           time.Now,
	})
}

func newTestOIDC(t *testing.T) (*OIDC, *fakeAuthn) {
	t.Helper()
	fake := newFakeAuthn(t)
	return newOIDCFor(fake, &mutableHost{value: "lab.example"}, fakeClientSecret), fake
}

func signIn(t *testing.T, o *OIDC, fake *fakeAuthn) (Tokens, string) {
	t.Helper()
	verifier, challenge, err := PKCE(strings.NewReader(strings.Repeat("v", 64)))
	if err != nil {
		t.Fatal(err)
	}
	authorize, err := o.AuthorizeURL(context.Background(), "state-1", "nonce-1", challenge)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := o.Exchange(context.Background(), fake.CodeFor(authorize), verifier, "nonce-1")
	if err != nil {
		t.Fatal(err)
	}
	return tokens, verifier
}

func TestAuthorizeURLCarriesPKCEAndNonce(t *testing.T) {
	o, _ := newTestOIDC(t)
	got, err := o.AuthorizeURL(context.Background(), "state-1", "nonce-1", "challenge-1")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != "https://sso.lab.example/oauth/v2/authorize" {
		t.Fatalf("authorize URL %s", got)
	}
	want := map[string]string{
		"response_type":         "code",
		"client_id":             "bedrock-console",
		"redirect_uri":          "https://console.lab.example/oauth/callback",
		"scope":                 "openid profile email groups offline_access",
		"state":                 "state-1",
		"nonce":                 "nonce-1",
		"code_challenge":        "challenge-1",
		"code_challenge_method": "S256",
	}
	for name, value := range want {
		if parsed.Query().Get(name) != value {
			t.Errorf("%s = %q, want %q", name, parsed.Query().Get(name), value)
		}
	}
}

func TestPKCEChallengeMatchesTheVerifier(t *testing.T) {
	verifier, challenge, err := PKCE(strings.NewReader(strings.Repeat("x", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if len(verifier) < 43 || len(verifier) > 128 || challenge != pkceChallenge(verifier) {
		t.Fatalf("verifier %q challenge %q", verifier, challenge)
	}
	if _, _, err := PKCE(strings.NewReader("")); err == nil {
		t.Fatal("PKCE accepted an empty random source")
	}
}

func TestExchangeReturnsTokensAndIdentity(t *testing.T) {
	o, fake := newTestOIDC(t)
	before := time.Now()
	tokens, _ := signIn(t, o, fake)
	if tokens.Access == "" || tokens.Refresh == "" {
		t.Fatalf("tokens %+v", tokens)
	}
	if tokens.AccessExpiry.Before(before.Add(59*time.Minute)) || tokens.AccessExpiry.After(before.Add(61*time.Minute)) {
		t.Fatalf("expiry %s", tokens.AccessExpiry)
	}
	want := Identity{Subject: "alice", Name: "Alice", Email: "alice@lab.example", Groups: []string{"ops"}, SessionName: "sid-1"}
	if tokens.Identity.Subject != want.Subject || tokens.Identity.Name != want.Name || tokens.Identity.Email != want.Email ||
		strings.Join(tokens.Identity.Groups, ",") != "ops" || tokens.Identity.SessionName != want.SessionName {
		t.Fatalf("identity %+v", tokens.Identity)
	}
}

func TestExchangeVerifiesTheIDToken(t *testing.T) {
	o, fake := newTestOIDC(t)
	cases := map[string]func(*fakeAuthn){
		"wrong nonce":    func(f *fakeAuthn) { f.wrongNonce = true },
		"wrong audience": func(f *fakeAuthn) { f.wrongAud = true },
		"wrong issuer":   func(f *fakeAuthn) { f.wrongIssuer = true },
		"expired":        func(f *fakeAuthn) { f.expired = true },
		"missing sid":    func(f *fakeAuthn) { f.noSID = true },
		"bad signature":  func(f *fakeAuthn) { f.badSig = true },
	}
	for name, change := range cases {
		fake.mutate(clearFlags)
		fake.mutate(change)
		verifier, challenge, _ := PKCE(strings.NewReader(strings.Repeat("v", 64)))
		authorize, err := o.AuthorizeURL(context.Background(), "s", "nonce-1", challenge)
		if err != nil {
			t.Fatal(err)
		}
		tokens, err := o.Exchange(context.Background(), fake.CodeFor(authorize), verifier, "nonce-1")
		if err == nil || tokens.Access != "" || tokens.Refresh != "" {
			t.Errorf("%s: error %v tokens %+v", name, err, tokens)
		}
	}
}

func clearFlags(f *fakeAuthn) {
	f.wrongNonce, f.wrongAud, f.wrongIssuer, f.expired, f.noSID, f.badSig = false, false, false, false, false, false
}

func TestClientAuthenticatesWithTheSecret(t *testing.T) {
	o, fake := newTestOIDC(t)
	signIn(t, o, fake)
	if fake.LastBasic() != "bedrock-console" {
		t.Fatalf("basic user %q", fake.LastBasic())
	}
	wrong := newOIDCFor(fake, &mutableHost{value: "lab.example"}, "wrong")
	verifier, challenge, _ := PKCE(strings.NewReader(strings.Repeat("v", 64)))
	authorize, err := wrong.AuthorizeURL(context.Background(), "s", "nonce-1", challenge)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Exchange(context.Background(), fake.CodeFor(authorize), verifier, "nonce-1"); err == nil {
		t.Fatal("Exchange accepted a wrong client secret")
	}
}

func TestRefreshReturnsRotatedToken(t *testing.T) {
	o, fake := newTestOIDC(t)
	first, _ := signIn(t, o, fake)
	second, err := o.Refresh(context.Background(), first.Refresh)
	if err != nil {
		t.Fatal(err)
	}
	if second.Refresh == "" || second.Refresh == first.Refresh || second.Access == first.Access || second.Identity.SessionName != "sid-1" {
		t.Fatalf("rotated %+v", second)
	}
	if _, err := o.Refresh(context.Background(), first.Refresh); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("reuse error = %v", err)
	}
}

func TestRefreshWithoutIDTokenKeepsTheIdentityEmpty(t *testing.T) {
	o, fake := newTestOIDC(t)
	first, _ := signIn(t, o, fake)
	fake.mutate(func(f *fakeAuthn) { f.noIDRefresh = true })
	second, err := o.Refresh(context.Background(), first.Refresh)
	if err != nil || second.Identity.SessionName != "" || second.Access == "" {
		t.Fatalf("refresh %+v, %v", second, err)
	}
}

func TestRefreshInvalidGrantMapsToErrInvalidGrant(t *testing.T) {
	o, _ := newTestOIDC(t)
	if _, err := o.Refresh(context.Background(), "unknown"); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("error = %v", err)
	}
}

func TestRefreshServerErrorIsNotInvalidGrant(t *testing.T) {
	o, fake := newTestOIDC(t)
	first, _ := signIn(t, o, fake)
	fake.mutate(func(f *fakeAuthn) { f.failStatus = http.StatusServiceUnavailable })
	_, err := o.Refresh(context.Background(), first.Refresh)
	if err == nil || errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("error = %v", err)
	}
}

func TestRevokeCallsTheEndpoint(t *testing.T) {
	o, fake := newTestOIDC(t)
	first, _ := signIn(t, o, fake)
	if err := o.Revoke(context.Background(), first.Refresh); err != nil {
		t.Fatal(err)
	}
	if got := fake.Revoked(); len(got) != 1 || got[0] != first.Refresh {
		t.Fatalf("revoked %v", got)
	}
}

func TestTransportDialsTheInClusterAddress(t *testing.T) {
	fake := newFakeAuthn(t)
	host := &mutableHost{value: "lab.example"}
	cfg := OIDCConfig{
		Host:            host.Get,
		AuthnAddress:    fake.Address(),
		AuthnServerName: fakeAuthnServerName,
		AuthnCA:         fake.caPEM,
	}
	httpClient := authnHTTPClient(cfg)
	resp, err := httpClient.Get("https://sso.lab.example/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if _, err := httpClient.Get("https://evil.example/.well-known/openid-configuration"); err == nil {
		t.Fatal("dialer accepted another host name")
	}
	_, _, otherCA := generateCA(t)
	cfg.AuthnCA = otherCA
	if _, err := authnHTTPClient(cfg).Get("https://sso.lab.example/.well-known/openid-configuration"); err == nil {
		t.Fatal("client trusted a certificate from an unknown CA")
	}
	cfg.AuthnCA = fake.caPEM
	cfg.AuthnServerName = "other.test"
	if _, err := authnHTTPClient(cfg).Get("https://sso.lab.example/.well-known/openid-configuration"); err == nil {
		t.Fatal("client accepted a wrong server name")
	}
}

func TestIssuerFollowsPlatformHost(t *testing.T) {
	fake := newFakeAuthn(t)
	host := &mutableHost{value: "lab.example"}
	o := newOIDCFor(fake, host, fakeClientSecret)
	first, err := o.AuthorizeURL(context.Background(), "s", "n", "c")
	if err != nil || !strings.HasPrefix(first, "https://sso.lab.example/") {
		t.Fatalf("first %q, %v", first, err)
	}
	host.Set("other.example")
	second, err := o.AuthorizeURL(context.Background(), "s", "n", "c")
	if err != nil || !strings.HasPrefix(second, "https://sso.other.example/") || !strings.Contains(second, "console.other.example") {
		t.Fatalf("second %q, %v", second, err)
	}
}
