package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type exchangeRequest struct {
	subject   string
	clientID  string
	audience  []string
	tokenType oidc.TokenType
	requested oidc.TokenType
	actor     string
	claims    map[string]any
}

func (r *exchangeRequest) GetAMR() []string                               { return nil }
func (r *exchangeRequest) GetAudience() []string                          { return r.audience }
func (r *exchangeRequest) GetResourses() []string                         { return nil }
func (r *exchangeRequest) GetAuthTime() time.Time                         { return testNow }
func (r *exchangeRequest) GetClientID() string                            { return r.clientID }
func (r *exchangeRequest) GetScopes() []string                            { return []string{oidc.ScopeOpenID} }
func (r *exchangeRequest) GetSubject() string                             { return r.subject }
func (r *exchangeRequest) GetRequestedTokenType() oidc.TokenType          { return r.requested }
func (r *exchangeRequest) GetExchangeSubject() string                     { return r.subject }
func (r *exchangeRequest) GetExchangeSubjectTokenType() oidc.TokenType    { return r.tokenType }
func (r *exchangeRequest) GetExchangeSubjectTokenIDOrToken() string       { return "subject-token-id" }
func (r *exchangeRequest) GetExchangeSubjectTokenClaims() map[string]any  { return r.claims }
func (r *exchangeRequest) GetExchangeActor() string                       { return r.actor }
func (r *exchangeRequest) GetExchangeActorTokenType() oidc.TokenType      { return "" }
func (r *exchangeRequest) GetExchangeActorTokenIDOrToken() string         { return "" }
func (r *exchangeRequest) GetExchangeActorTokenClaims() map[string]any    { return nil }
func (r *exchangeRequest) SetCurrentScopes([]string)                      {}
func (r *exchangeRequest) SetRequestedTokenType(tokenType oidc.TokenType) { r.requested = tokenType }
func (r *exchangeRequest) SetSubject(subject string)                      { r.subject = subject }

func subjectClaims(expiry time.Time) map[string]any {
	return map[string]any{
		"iss":    "https://sso.example.test",
		"sub":    "alice",
		"aud":    []any{"bedrock", "bedrock-cli"},
		"exp":    float64(expiry.Unix()),
		"groups": []any{"admins", "ops"},
		"amr":    []any{"pwd", "otp"},
	}
}

func exchangeFor(clientID string, audience ...string) *exchangeRequest {
	return &exchangeRequest{subject: "alice", clientID: clientID, audience: audience, tokenType: oidc.AccessTokenType, claims: subjectClaims(testNow.Add(30 * time.Minute))}
}

func exchangeClient(id string, grants ...string) *v1alpha1.OAuthClient {
	oauth := confidentialClient(id, id+"-secret", grants...)
	oauth.Spec.TokenExchange = &v1alpha1.TokenExchange{Audiences: []string{"grafana", "billing-api"}}
	return oauth
}

func TestExchangeAllowed(t *testing.T) {
	allowed := *exchangeClient("grafana", v1alpha1.GrantTokenExchange)
	withoutGrant := *exchangeClient("grafana", v1alpha1.GrantAuthorizationCode)
	withoutList := *confidentialClient("grafana", "grafana-secret", v1alpha1.GrantTokenExchange)
	public := *exchangeClient("grafana", v1alpha1.GrantTokenExchange)
	public.Spec.Public = true
	cases := []struct {
		name      string
		client    v1alpha1.OAuthClient
		audiences []string
		want      bool
	}{
		{"listed audience", allowed, []string{"grafana"}, true},
		{"two listed audiences", allowed, []string{"grafana", "billing-api"}, true},
		{"one audience outside the list", allowed, []string{"grafana", "bedrock"}, false},
		{"no audience", allowed, nil, false},
		{"client without the grant", withoutGrant, []string{"grafana"}, false},
		{"client without a list", withoutList, []string{"grafana"}, false},
		{"public client", public, []string{"grafana"}, false},
	}
	for _, tc := range cases {
		if got := ExchangeAllowed(tc.client, tc.audiences); got != tc.want {
			t.Errorf("%s: ExchangeAllowed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestExchangeExpiry(t *testing.T) {
	cases := []struct {
		subject time.Time
		want    time.Time
	}{
		{testNow.Add(30 * time.Minute), testNow.Add(30 * time.Minute)},
		{testNow.Add(3 * time.Hour), testNow.Add(time.Hour)},
		{testNow.Add(time.Hour), testNow.Add(time.Hour)},
		{time.Time{}, time.Time{}},
	}
	for _, tc := range cases {
		if got := ExchangeExpiry(tc.subject, testNow); !got.Equal(tc.want) {
			t.Errorf("ExchangeExpiry(%v) = %v, want %v", tc.subject, got, tc.want)
		}
	}
}

func TestTokenExchangeKeepsSubjectAndAddsAct(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"), exchangeClient("grafana", v1alpha1.GrantTokenExchange))
	s := newTestStore(c, testNow, testSettings(), nil)
	request := exchangeFor("grafana", "grafana")
	if err := s.ValidateTokenExchangeRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if request.requested != oidc.AccessTokenType {
		t.Fatalf("requested token type %q, want an access token", request.requested)
	}
	if err := s.CreateTokenExchangeRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	claims, err := s.GetPrivateClaimsFromTokenExchangeRequest(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"groups": []string{"admins", "ops"},
		"amr":    []string{"pwd", "otp"},
		"email":  "alice@example.test",
		"name":   "Test alice",
		"act":    map[string]any{"sub": "grafana"},
	}
	if !reflect.DeepEqual(claims, want) {
		t.Fatalf("exchange claims %v, want %v", claims, want)
	}
	id, expiry, err := s.CreateAccessToken(ctx, request)
	scopes, ok := scopesOfAccessTokenID(id)
	if err != nil || !ok || !reflect.DeepEqual(scopes, []string{oidc.ScopeOpenID}) || !expiry.Equal(testNow.Add(30*time.Minute)) {
		t.Fatalf("id %q scopes %v expiry %v err %v: the token must stay scope-encoded and end with the subject token", id, scopes, expiry, err)
	}
	long := exchangeFor("grafana", "grafana")
	long.claims = subjectClaims(testNow.Add(3 * time.Hour))
	if _, expiry, err := s.CreateAccessToken(ctx, long); err != nil || !expiry.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("expiry %v err %v: an exchanged token lives at most one hour", expiry, err)
	}
	info := &oidc.UserInfo{}
	if err := s.SetUserinfoFromTokenExchangeRequest(ctx, info, request); err != nil {
		t.Fatal(err)
	}
	if info.Subject != "alice" || !reflect.DeepEqual(info.Claims["act"], map[string]any{"sub": "grafana"}) {
		t.Fatalf("exchange userinfo %+v", info)
	}
}

func TestTokenExchangeRefusesAudienceOutsideList(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	carol := testUser("carol")
	carol.Spec.Disabled = true
	create(t, c, testUser("alice"), carol, exchangeClient("grafana", v1alpha1.GrantTokenExchange))
	s := newTestStore(c, testNow, testSettings(), nil)

	chained := exchangeFor("grafana", "grafana")
	chained.claims["aud"] = []any{"grafana"}
	otherIssuer := exchangeFor("grafana", "grafana")
	otherIssuer.claims["iss"] = "https://sso.other.test"
	idToken := exchangeFor("grafana", "grafana")
	idToken.tokenType = oidc.IDTokenType
	refreshWanted := exchangeFor("grafana", "grafana")
	refreshWanted.requested = oidc.RefreshTokenType
	withActor := exchangeFor("grafana", "grafana")
	withActor.actor = "someone"
	disabled := exchangeFor("grafana", "grafana")
	disabled.subject = "carol"
	refused := map[string]*exchangeRequest{
		"audience outside the list":       exchangeFor("grafana", "bedrock"),
		"no audience":                     exchangeFor("grafana"),
		"subject token from an exchange":  chained,
		"subject token of another issuer": otherIssuer,
		"id token as subject":             idToken,
		"refresh token requested":         refreshWanted,
		"actor token":                     withActor,
		"disabled user":                   disabled,
	}
	for name, request := range refused {
		if err := s.ValidateTokenExchangeRequest(ctx, request); err == nil {
			t.Errorf("%s: the exchange must be refused", name)
		}
	}
	var oidcErr *oidc.Error
	if err := s.ValidateTokenExchangeRequest(ctx, exchangeFor("grafana", "bedrock")); !errors.As(err, &oidcErr) || oidcErr.ErrorType != oidc.InvalidTarget {
		t.Fatalf("an audience outside the list must be invalid_target, got %v", err)
	}
}

func TestTokenExchangeRefusesClientWithoutGrant(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	create(t, c, testUser("alice"), exchangeClient("grafana", v1alpha1.GrantAuthorizationCode))
	s := newTestStore(c, testNow, testSettings(), nil)
	var oidcErr *oidc.Error
	if err := s.ValidateTokenExchangeRequest(ctx, exchangeFor("grafana", "grafana")); !errors.As(err, &oidcErr) || oidcErr.ErrorType != oidc.UnauthorizedClient {
		t.Fatalf("a client without the grant must be unauthorized_client, got %v", err)
	}
	if err := s.ValidateTokenExchangeRequest(ctx, exchangeFor("missing", "grafana")); err == nil {
		t.Fatal("an unknown client must be refused")
	}
}

func TestIntrospection(t *testing.T) {
	c := startTestEnv(t)
	ctx := context.Background()
	carol := testUser("carol")
	carol.Spec.Disabled = true
	create(t, c, testUser("alice"), carol, testGroup("admins", "alice"))
	s := newTestStore(c, testNow, testSettings(), nil)
	response := &oidc.IntrospectionResponse{}
	if err := s.SetIntrospectionFromToken(ctx, response, "token-id", "alice", "grafana"); err != nil {
		t.Fatal(err)
	}
	if response.Subject != "alice" || response.Username != "alice" || response.Email != "alice@example.test" || !reflect.DeepEqual(response.Claims["groups"], []string{"admins"}) {
		t.Fatalf("introspection %+v", response)
	}
	if err := s.SetIntrospectionFromToken(ctx, &oidc.IntrospectionResponse{}, "token-id", "carol", "grafana"); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("a disabled user's token must be inactive, got %v", err)
	}
	if err := s.SetIntrospectionFromToken(ctx, &oidc.IntrospectionResponse{}, "token-id", "nobody", "grafana"); !isNotFound(err) {
		t.Fatalf("a deleted user's token must be inactive, got %v", err)
	}
}
