package methods

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/zitadel/oidc/v3/pkg/client/rp"
	"github.com/zitadel/oidc/v3/pkg/op"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type fakeUpstreamIssuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	claims map[string]any
}

func newFakeUpstreamIssuer(t *testing.T) *fakeUpstreamIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &fakeUpstreamIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", issuer.discovery)
	mux.HandleFunc("/jwks", issuer.jwks)
	mux.HandleFunc("/token", issuer.token)
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {})
	issuer.server = httptest.NewServer(mux)
	t.Cleanup(issuer.server.Close)
	return issuer
}

func (f *fakeUpstreamIssuer) discovery(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                f.server.URL,
		"authorization_endpoint":                f.server.URL + "/authorize",
		"token_endpoint":                        f.server.URL + "/token",
		"jwks_uri":                              f.server.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (f *fakeUpstreamIssuer) jwks(w http.ResponseWriter, r *http.Request) {
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "test-key", Algorithm: "RS256", Use: "sig"}}}
	_ = json.NewEncoder(w).Encode(set)
}

func (f *fakeUpstreamIssuer) token(w http.ResponseWriter, r *http.Request) {
	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.RS256,
		Key:       &jose.JSONWebKey{Key: f.key, KeyID: "test-key", Algorithm: "RS256", Use: "sig"},
	}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	idToken, err := jwt.Signed(signer).Claims(f.claims).Serialize()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "upstream-access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     idToken,
	})
}

func oidcProviderFixture(name, issuer string) v1alpha1.IdentityProvider {
	return v1alpha1.IdentityProvider{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "IdentityProvider"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "bedrock-system", Name: name},
		Spec: v1alpha1.IdentityProviderSpec{
			Type:      v1alpha1.MethodOIDC,
			SecretRef: name,
			OIDC:      &v1alpha1.OIDCProvider{Issuer: issuer, ClientID: "bedrock-authn"},
		},
	}
}

func fixedRelyingParty(clientSecret string) RelyingParties {
	return func(ctx context.Context, provider v1alpha1.IdentityProvider, redirectURI string) (rp.RelyingParty, error) {
		return rp.NewRelyingPartyOIDC(ctx, provider.Spec.OIDC.Issuer, provider.Spec.OIDC.ClientID, clientSecret, redirectURI, []string{"openid"}, rp.WithVerifierOpts(rp.WithNonce(expectedNonce)))
	}
}

func TestClaimsIdentityDefaults(t *testing.T) {
	claims := map[string]any{
		"sub": "subject-1", "preferred_username": "alice", "email": "alice@example.test", "name": "Alice A",
		"groups": []any{"eng", "ops"},
	}
	identity, err := ClaimsIdentity(v1alpha1.OIDCProvider{}, claims)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Username != "alice" || identity.Email != "alice@example.test" || identity.DisplayName != "Alice A" || identity.Subject != "subject-1" {
		t.Fatalf("identity = %+v", identity)
	}
	if len(identity.Groups) != 2 || identity.Groups[0] != "eng" || identity.Groups[1] != "ops" {
		t.Fatalf("groups = %v", identity.Groups)
	}

	custom := v1alpha1.OIDCProvider{UsernameClaim: "upn", EmailClaim: "mail", NameClaim: "fullName", GroupsClaim: "roles"}
	customClaims := map[string]any{"sub": "s2", "upn": "Bob.Custom", "mail": "bob@example.test", "fullName": "Bob C", "roles": []any{"admin"}}
	identity2, err := ClaimsIdentity(custom, customClaims)
	if err != nil {
		t.Fatal(err)
	}
	if identity2.Username != "bob.custom" {
		t.Fatalf("identity2.Username = %q, want bob.custom", identity2.Username)
	}
}

func TestUpstreamAMR(t *testing.T) {
	if got := UpstreamAMR(map[string]any{"amr": []any{"mfa", "otp"}}); len(got) != 2 || got[0] != "mfa" || got[1] != "otp" {
		t.Fatalf("UpstreamAMR = %v", got)
	}
	if got := UpstreamAMR(map[string]any{}); got != nil {
		t.Fatalf("UpstreamAMR(empty) = %v, want nil", got)
	}
}

func TestOIDCRedirectHasPKCEAndNonce(t *testing.T) {
	c, _ := startTestEnv(t)
	issuer := newFakeUpstreamIssuer(t)
	provider := oidcProviderFixture("corp-oidc", issuer.server.URL)
	if err := c.Create(context.Background(), &provider); err != nil {
		t.Fatal(err)
	}
	method := NewOIDC(c, fixedRelyingParty("client-secret"))
	cookie := UpstreamCookie{Verifier: "a-code-verifier-that-is-long-enough-1234567890", Nonce: "a-nonce-value", State: "auth-request-1"}
	request := v1alpha1.AuthRequest{Status: v1alpha1.AuthRequestStatus{Login: v1alpha1.LoginState{Provider: provider.Name, Upstream: EncodeUpstream(cookie)}}}
	ctx := op.ContextWithIssuer(context.Background(), "https://sso.example.test")

	challenge, err := method.Begin(ctx, Flow{AuthRequest: request}, v1alpha1.User{})
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Type != ChallengeRedirect || challenge.Redirect == "" {
		t.Fatalf("challenge = %+v", challenge)
	}
	redirectURL, err := url.Parse(challenge.Redirect)
	if err != nil {
		t.Fatal(err)
	}
	query := redirectURL.Query()
	if query.Get("nonce") != cookie.Nonce {
		t.Fatalf("redirect nonce = %q, want %q", query.Get("nonce"), cookie.Nonce)
	}
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		t.Fatalf("redirect PKCE params missing: %q", challenge.Redirect)
	}
	if query.Get("state") != cookie.State {
		t.Fatalf("redirect state = %q, want %q", query.Get("state"), cookie.State)
	}
}

func TestOIDCRefusesWrongNonce(t *testing.T) {
	c, _ := startTestEnv(t)
	issuer := newFakeUpstreamIssuer(t)
	provider := oidcProviderFixture("wrong-nonce-oidc", issuer.server.URL)
	if err := c.Create(context.Background(), &provider); err != nil {
		t.Fatal(err)
	}
	method := NewOIDC(c, fixedRelyingParty("client-secret"))
	ctx := op.ContextWithIssuer(context.Background(), "https://sso.example.test")
	now := time.Now()
	issuer.claims = map[string]any{
		"iss": issuer.server.URL, "sub": "subject-1", "aud": provider.Spec.OIDC.ClientID,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": "a-different-nonce",
		"preferred_username": "alice",
	}
	cookie := UpstreamCookie{Verifier: "verifier-value-long-enough-0123456789", Nonce: "expected-nonce", State: "auth-request-1"}
	request := v1alpha1.AuthRequest{Status: v1alpha1.AuthRequestStatus{Login: v1alpha1.LoginState{Provider: provider.Name, Upstream: EncodeUpstream(cookie)}}}

	result, err := method.Complete(ctx, Flow{AuthRequest: request, Now: now}, v1alpha1.User{}, Answer{Code: "auth-code-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure != FailureProviderError {
		t.Fatalf("a mismatched nonce must be refused: %+v", result)
	}
}

func TestOIDCRefusesChangedSubject(t *testing.T) {
	c, _ := startTestEnv(t)
	issuer := newFakeUpstreamIssuer(t)
	provider := oidcProviderFixture("changed-subject-oidc", issuer.server.URL)
	if err := c.Create(context.Background(), &provider); err != nil {
		t.Fatal(err)
	}
	method := NewOIDC(c, fixedRelyingParty("client-secret"))
	ctx := op.ContextWithIssuer(context.Background(), "https://sso.example.test")
	now := time.Now()

	existing := v1alpha1.User{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "User"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "bedrock-system", Name: v1alpha1.UserObjectName("frank")},
		Spec:       v1alpha1.UserSpec{Username: "frank", Source: provider.Name},
		Status:     v1alpha1.UserStatus{UpstreamSubject: "original-subject"},
	}
	cookie := UpstreamCookie{Verifier: "verifier-value-long-enough-0123456789", Nonce: "a-nonce-value", State: "auth-request-1"}
	request := v1alpha1.AuthRequest{Status: v1alpha1.AuthRequestStatus{Login: v1alpha1.LoginState{Provider: provider.Name, Upstream: EncodeUpstream(cookie)}}}
	issuer.claims = map[string]any{
		"iss": issuer.server.URL, "sub": "a-different-subject", "aud": provider.Spec.OIDC.ClientID,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": cookie.Nonce,
		"preferred_username": "frank",
	}

	result, err := method.Complete(ctx, Flow{AuthRequest: request, Now: now}, existing, Answer{Code: "auth-code-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure != FailureProviderError {
		t.Fatalf("a changed upstream subject must be refused: %+v", result)
	}
}

func TestOIDCRefusesALocalUserOfTheSameName(t *testing.T) {
	c, _ := startTestEnv(t)
	issuer := newFakeUpstreamIssuer(t)
	provider := oidcProviderFixture("conflict-oidc", issuer.server.URL)
	if err := c.Create(context.Background(), &provider); err != nil {
		t.Fatal(err)
	}
	local := createUser(t, c, "kim")
	method := NewOIDC(c, fixedRelyingParty("client-secret"))
	ctx := op.ContextWithIssuer(context.Background(), "https://sso.example.test")
	now := time.Now()
	issuer.claims = map[string]any{
		"iss": issuer.server.URL, "sub": "upstream-subject-kim", "aud": provider.Spec.OIDC.ClientID,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": "a-nonce-value",
		"preferred_username": "kim",
	}
	cookie := UpstreamCookie{Verifier: "verifier-value-long-enough-0123456789", Nonce: "a-nonce-value", State: "auth-request-1"}
	request := v1alpha1.AuthRequest{Status: v1alpha1.AuthRequestStatus{Login: v1alpha1.LoginState{Provider: provider.Name, Upstream: EncodeUpstream(cookie)}}}

	result, err := method.Complete(ctx, Flow{AuthRequest: request, Now: now}, local, Answer{Code: "auth-code-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure != FailureProviderError {
		t.Fatalf("an OIDC login for an existing local user of the same name must be refused: %+v", result)
	}
}

func TestOIDCProvisionsAndUpdatesTheUserWithSubject(t *testing.T) {
	c, cfg := startTestEnv(t)
	_ = cfg
	issuer := newFakeUpstreamIssuer(t)
	provider := oidcProviderFixture("provision-oidc", issuer.server.URL)
	if err := c.Create(context.Background(), &provider); err != nil {
		t.Fatal(err)
	}
	method := NewOIDC(c, fixedRelyingParty("client-secret"))
	ctx := op.ContextWithIssuer(context.Background(), "https://sso.example.test")
	now := time.Now()
	cookie := UpstreamCookie{Verifier: "verifier-value-long-enough-0123456789", Nonce: "a-nonce-value", State: "auth-request-1"}
	request := v1alpha1.AuthRequest{Status: v1alpha1.AuthRequestStatus{Login: v1alpha1.LoginState{Provider: provider.Name, Upstream: EncodeUpstream(cookie)}}}
	issuer.claims = map[string]any{
		"iss": issuer.server.URL, "sub": "upstream-subject-judy", "aud": provider.Spec.OIDC.ClientID,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": cookie.Nonce,
		"preferred_username": "judy", "name": "Judy One", "email": "judy@example.test",
	}

	first, err := method.Complete(ctx, Flow{AuthRequest: request, Now: now}, v1alpha1.User{}, Answer{Code: "auth-code-1"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Subject == nil {
		t.Fatalf("first login = %+v", first)
	}
	if first.Subject.User.Status.UpstreamSubject != "upstream-subject-judy" {
		t.Fatalf("upstream subject not persisted: %+v", first.Subject.User.Status)
	}
	var created v1alpha1.User
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "bedrock-system", Name: v1alpha1.UserObjectName("judy")}, &created); err != nil {
		t.Fatal(err)
	}
	if created.Status.UpstreamSubject != "upstream-subject-judy" {
		t.Fatalf("created.Status = %+v", created.Status)
	}

	issuer.claims["name"] = "Judy Two"
	second, err := method.Complete(ctx, Flow{AuthRequest: request, Now: now}, created, Answer{Code: "auth-code-2"})
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
	if updated.Spec.DisplayName != "Judy Two" || updated.Status.UpstreamSubject != "upstream-subject-judy" {
		t.Fatalf("updated = %+v %+v", updated.Spec, updated.Status)
	}
}

func TestOIDCMapsGroupClaims(t *testing.T) {
	c, _ := startTestEnv(t)
	issuer := newFakeUpstreamIssuer(t)
	provider := oidcProviderFixture("groupmap-oidc", issuer.server.URL)
	provider.Spec.GroupMapping = []v1alpha1.GroupMapping{{External: "eng", Group: "developers"}}
	if err := c.Create(context.Background(), &provider); err != nil {
		t.Fatal(err)
	}
	method := NewOIDC(c, fixedRelyingParty("client-secret"))
	ctx := op.ContextWithIssuer(context.Background(), "https://sso.example.test")
	now := time.Now()
	cookie := UpstreamCookie{Verifier: "verifier-value-long-enough-0123456789", Nonce: "a-nonce-value", State: "auth-request-1"}
	request := v1alpha1.AuthRequest{Status: v1alpha1.AuthRequestStatus{Login: v1alpha1.LoginState{Provider: provider.Name, Upstream: EncodeUpstream(cookie)}}}
	issuer.claims = map[string]any{
		"iss": issuer.server.URL, "sub": "upstream-subject-groupmap", "aud": provider.Spec.OIDC.ClientID,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": cookie.Nonce,
		"preferred_username": "gina", "groups": []any{"eng", "developers"},
	}

	result, err := method.Complete(ctx, Flow{AuthRequest: request, Now: now}, v1alpha1.User{}, Answer{Code: "auth-code-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Subject == nil {
		t.Fatalf("login = %+v", result)
	}
	got := result.Subject.User.Spec.Groups
	if len(got) != 1 || got[0] != "developers" {
		t.Fatalf("groups = %v, want [developers]: a raw upstream claim equal to an internal group name but absent from the mapping must not grant it", got)
	}
}
