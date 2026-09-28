package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

func publicClient(id string) *v1alpha1.OAuthClient {
	return &v1alpha1.OAuthClient{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: release.SystemNamespace},
		Spec: v1alpha1.OAuthClientSpec{
			ClientID:     id,
			Public:       true,
			RedirectURIs: []string{"http://127.0.0.1/callback"},
			GrantTypes:   []string{v1alpha1.GrantAuthorizationCode, v1alpha1.GrantRefreshToken, v1alpha1.GrantDeviceCode},
		},
	}
}

func confidentialClient(id, secretName string, grants ...string) *v1alpha1.OAuthClient {
	return &v1alpha1.OAuthClient{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: release.SystemNamespace},
		Spec: v1alpha1.OAuthClientSpec{
			ClientID:     id,
			RedirectURIs: []string{"https://" + id + ".example.test/oauth/callback"},
			GrantTypes:   grants,
			SecretRef:    secretName,
		},
	}
}

func clientSecret(name, value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: release.SystemNamespace, Labels: map[string]string{v1alpha1.LabelAuthn: "true"}},
		Data:       map[string][]byte{"clientSecret": []byte(value)},
	}
}

func TestRedirectURIMatching(t *testing.T) {
	cli := *publicClient("bedrock-cli")
	console := *confidentialClient("console", "console-secret", v1alpha1.GrantAuthorizationCode)
	confidentialLoopback := *confidentialClient("tool", "tool-secret", v1alpha1.GrantAuthorizationCode)
	confidentialLoopback.Spec.RedirectURIs = []string{"http://127.0.0.1/callback"}
	cases := []struct {
		name    string
		client  v1alpha1.OAuthClient
		uri     string
		allowed bool
	}{
		{"exact loopback", cli, "http://127.0.0.1/callback", true},
		{"loopback on another port", cli, "http://127.0.0.1:53121/callback", true},
		{"look-alike host", cli, "http://127.0.0.1.evil.test:53121/callback", false},
		{"localhost", cli, "http://localhost:53121/callback", false},
		{"ipv6 loopback", cli, "http://[::1]:53121/callback", false},
		{"user info", cli, "http://a@127.0.0.1:53121/callback", false},
		{"other path", cli, "http://127.0.0.1:53121/other", false},
		{"dot segments", cli, "http://127.0.0.1:53121/callback/../other", false},
		{"fragment", cli, "http://127.0.0.1:53121/callback#x", false},
		{"extra query", cli, "http://127.0.0.1:53121/callback?next=https://evil.test", false},
		{"https on the loopback", cli, "https://127.0.0.1:53121/callback", false},
		{"empty", cli, "", false},
		{"confidential exact", console, "https://console.example.test/oauth/callback", true},
		{"confidential over http", console, "http://console.example.test/oauth/callback", false},
		{"confidential on another port", console, "https://console.example.test:8443/oauth/callback", false},
		{"confidential with a fragment", console, "https://console.example.test/oauth/callback#x", false},
		{"no port rule for confidential clients", confidentialLoopback, "http://127.0.0.1:53121/callback", false},
	}
	for _, tc := range cases {
		if got := RedirectAllowed(tc.client, tc.uri); got != tc.allowed {
			t.Errorf("%s: RedirectAllowed(%q) = %v, want %v", tc.name, tc.uri, got, tc.allowed)
		}
	}
}

func TestToClient(t *testing.T) {
	var _ op.Client = Client{}
	public := ToClient(*publicClient("bedrock-cli"))
	if public.GetID() != "bedrock-cli" || public.ApplicationType() != op.ApplicationTypeNative || public.AuthMethod() != oidc.AuthMethodNone {
		t.Fatalf("public client view: id %q type %v method %q", public.GetID(), public.ApplicationType(), public.AuthMethod())
	}
	wantGrants := []oidc.GrantType{oidc.GrantTypeCode, oidc.GrantTypeRefreshToken, oidc.GrantTypeDeviceCode}
	if !reflect.DeepEqual(public.GrantTypes(), wantGrants) || !reflect.DeepEqual(public.ResponseTypes(), []oidc.ResponseType{oidc.ResponseTypeCode}) {
		t.Fatalf("grants %v, response types %v", public.GrantTypes(), public.ResponseTypes())
	}
	if public.LoginURL("abc") != "/login/?authRequest=abc" || public.AccessTokenType() != op.AccessTokenTypeJWT || public.IDTokenLifetime() != time.Hour || public.ClockSkew() != 0 || public.DevMode() || public.IDTokenUserinfoClaimsAssertion() {
		t.Fatal("fixed client settings differ from the contract")
	}
	if !public.IsScopeAllowed("groups") || !public.IsScopeAllowed(oidc.ScopeOfflineAccess) || public.IsScopeAllowed("admin") {
		t.Fatal("only groups and offline_access are extra scopes")
	}
	if got := public.RestrictAdditionalAccessTokenScopes()([]string{"openid", "groups"}); !reflect.DeepEqual(got, []string{"openid", "groups"}) {
		t.Fatalf("scopes must pass unchanged, got %v", got)
	}
	confidential := ToClient(*confidentialClient("console", "console-secret", v1alpha1.GrantTokenExchange))
	if confidential.ApplicationType() != op.ApplicationTypeWeb || confidential.AuthMethod() != oidc.AuthMethodBasic || len(confidential.ResponseTypes()) != 0 {
		t.Fatalf("confidential client view: type %v method %q response types %v", confidential.ApplicationType(), confidential.AuthMethod(), confidential.ResponseTypes())
	}
}

func TestAuthorizeClientSecret(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c,
		confidentialClient("console", "console-secret", v1alpha1.GrantAuthorizationCode),
		clientSecret("console-secret", "correct-secret-value"),
		publicClient("bedrock-cli"),
	)
	s := newTestStore(c, testNow, testSettings(), nil)
	if err := s.AuthorizeClientIDSecret(ctx, "console", "correct-secret-value"); err != nil {
		t.Fatalf("the right secret must pass: %v", err)
	}
	refused := []struct{ id, secret string }{
		{"console", "wrong-secret-value"},
		{"console", ""},
		{"console", "correct-secret-valu"},
		{"bedrock-cli", ""},
		{"missing", "correct-secret-value"},
	}
	for _, tc := range refused {
		if err := s.AuthorizeClientIDSecret(ctx, tc.id, tc.secret); err == nil {
			t.Fatalf("client %q with secret %q must be refused", tc.id, tc.secret)
		}
	}
	found, err := s.GetClientByClientID(ctx, "console")
	if err != nil || found.GetID() != "console" || found.AuthMethod() != oidc.AuthMethodBasic {
		t.Fatalf("client %v, err %v", found, err)
	}
	if _, err := s.GetClientByClientID(ctx, "missing"); !isNotFound(err) {
		t.Fatalf("a missing client must be not found, got %v", err)
	}
	if _, err := s.GetKeyByIDAndClientID(ctx, "key", "console"); err == nil {
		t.Fatal("JWT profile keys are not supported")
	}
	if _, err := s.ValidateJWTProfileScopes(ctx, "alice", []string{"openid"}); err == nil {
		t.Fatal("JWT profile scopes are not supported")
	}
}
