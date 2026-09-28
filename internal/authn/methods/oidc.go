package methods

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/zitadel/oidc/v3/pkg/client/rp"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	"golang.org/x/oauth2"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

type RelyingParties func(ctx context.Context, provider v1alpha1.IdentityProvider, redirectURI string) (rp.RelyingParty, error)

type expectedNonceKey struct{}

func withExpectedNonce(ctx context.Context, nonce string) context.Context {
	return context.WithValue(ctx, expectedNonceKey{}, nonce)
}

func expectedNonce(ctx context.Context) string {
	nonce, _ := ctx.Value(expectedNonceKey{}).(string)
	return nonce
}

func DefaultRelyingParties(httpClient func(caBundle []byte) *http.Client, clientSecret func(ctx context.Context, provider v1alpha1.IdentityProvider) (string, error)) RelyingParties {
	return func(ctx context.Context, provider v1alpha1.IdentityProvider, redirectURI string) (rp.RelyingParty, error) {
		secret, err := clientSecret(ctx, provider)
		if err != nil {
			return nil, err
		}
		return rp.NewRelyingPartyOIDC(
			ctx, provider.Spec.OIDC.Issuer, provider.Spec.OIDC.ClientID, secret, redirectURI, oidcScopes(*provider.Spec.OIDC),
			rp.WithHTTPClient(httpClient([]byte(provider.Spec.CABundle))),
			rp.WithVerifierOpts(rp.WithNonce(expectedNonce)),
		)
	}
}

func oidcScopes(spec v1alpha1.OIDCProvider) []string {
	if len(spec.Scopes) > 0 {
		return spec.Scopes
	}
	return []string{"openid", "profile", "email", "groups"}
}

func UpstreamRedirectURI(issuer, provider string) string {
	return issuer + "/api/v1/login/providers/" + provider + "/callback"
}

type UpstreamCookie struct {
	Verifier string `json:"verifier"`
	Nonce    string `json:"nonce"`
	State    string `json:"state"`
}

func EncodeUpstream(c UpstreamCookie) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func DecodeUpstream(v string) (UpstreamCookie, error) {
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return UpstreamCookie{}, err
	}
	var c UpstreamCookie
	if err := json.Unmarshal(raw, &c); err != nil {
		return UpstreamCookie{}, err
	}
	return c, nil
}

func stringOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func claimStrings(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func ClaimsIdentity(provider v1alpha1.OIDCProvider, claims map[string]any) (ExternalIdentity, error) {
	usernameClaim := stringOr(provider.UsernameClaim, "preferred_username")
	emailClaim := stringOr(provider.EmailClaim, "email")
	nameClaim := stringOr(provider.NameClaim, "name")
	groupsClaim := stringOr(provider.GroupsClaim, "groups")
	raw, _ := claims[usernameClaim].(string)
	if raw == "" {
		return ExternalIdentity{}, errors.New("methods: oidc claims missing the username claim")
	}
	username, err := v1alpha1.NormalizeUsername(raw)
	if err != nil {
		return ExternalIdentity{}, err
	}
	email, _ := claims[emailClaim].(string)
	name, _ := claims[nameClaim].(string)
	subject, _ := claims["sub"].(string)
	return ExternalIdentity{
		Username:    username,
		DisplayName: name,
		Email:       email,
		Subject:     subject,
		Groups:      claimStrings(claims[groupsClaim]),
	}, nil
}

func UpstreamAMR(claims map[string]any) []string {
	return claimStrings(claims["amr"])
}

func codeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func withNonce(nonce string) rp.AuthURLOpt {
	return func() []oauth2.AuthCodeOption { return []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("nonce", nonce)} }
}

type oidcMethod struct {
	client       client.Client
	relyingParty RelyingParties
}

func NewOIDC(c client.Client, relyingParty RelyingParties) Method {
	return oidcMethod{client: c, relyingParty: relyingParty}
}

func (m oidcMethod) Name() string { return v1alpha1.MethodOIDC }

func (m oidcMethod) Kind() Kind { return Primary }

func (m oidcMethod) setup(ctx context.Context, flow Flow) (context.Context, v1alpha1.IdentityProvider, UpstreamCookie, rp.RelyingParty, error) {
	var provider v1alpha1.IdentityProvider
	if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: flow.AuthRequest.Status.Login.Provider}, &provider); err != nil {
		return ctx, v1alpha1.IdentityProvider{}, UpstreamCookie{}, nil, err
	}
	cookie, err := DecodeUpstream(flow.AuthRequest.Status.Login.Upstream)
	if err != nil {
		return ctx, v1alpha1.IdentityProvider{}, UpstreamCookie{}, nil, err
	}
	ctx = withExpectedNonce(ctx, cookie.Nonce)
	redirectURI := UpstreamRedirectURI(op.IssuerFromContext(ctx), provider.Name)
	party, err := m.relyingParty(ctx, provider, redirectURI)
	if err != nil {
		return ctx, v1alpha1.IdentityProvider{}, UpstreamCookie{}, nil, err
	}
	return ctx, provider, cookie, party, nil
}

func (m oidcMethod) Begin(ctx context.Context, flow Flow, user v1alpha1.User) (Challenge, error) {
	_, _, cookie, party, err := m.setup(ctx, flow)
	if err != nil {
		return Challenge{}, err
	}
	authURL := rp.AuthURL(cookie.State, party, rp.WithCodeChallenge(codeChallengeS256(cookie.Verifier)), withNonce(cookie.Nonce))
	return Challenge{Type: ChallengeRedirect, Redirect: authURL}, nil
}

func (m oidcMethod) Complete(ctx context.Context, flow Flow, user v1alpha1.User, answer Answer) (Result, error) {
	setupCtx, provider, cookie, party, err := m.setup(ctx, flow)
	if err != nil {
		return Result{Failure: FailureProviderError}, nil
	}
	tokens, err := rp.CodeExchange[*oidc.IDTokenClaims](setupCtx, answer.Code, party, rp.WithCodeVerifier(cookie.Verifier))
	if err != nil {
		return Result{Failure: FailureProviderError}, nil
	}
	claims := tokens.IDTokenClaims.Claims
	nonce, _ := claims["nonce"].(string)
	if nonce == "" || nonce != cookie.Nonce {
		return Result{Failure: FailureProviderError}, nil
	}
	identity, err := ClaimsIdentity(*provider.Spec.OIDC, claims)
	if err != nil {
		return Result{Failure: FailureProviderError}, nil
	}
	identity.Groups = policy.MapGroups(provider.Spec.GroupMapping, identity.Groups)
	var existing *v1alpha1.User
	if user.Name != "" {
		existing = &user
	}
	if existing != nil {
		if existing.Spec.Source != provider.Name {
			return Result{Failure: FailureProviderError}, nil
		}
		if existing.Status.UpstreamSubject != "" && existing.Status.UpstreamSubject != identity.Subject {
			return Result{Failure: FailureProviderError}, nil
		}
	}
	provisioned, err := m.provision(ctx, existing, provider.Name, identity)
	if err != nil {
		return Result{}, err
	}
	amr := append(UpstreamAMR(claims), "fed")
	return Result{Subject: &Subject{User: provisioned, AMR: amr}}, nil
}

func (m oidcMethod) provision(ctx context.Context, existing *v1alpha1.User, providerName string, identity ExternalIdentity) (v1alpha1.User, error) {
	if existing == nil {
		return m.createUser(ctx, providerName, identity)
	}
	return m.updateUser(ctx, existing.Name, providerName, identity)
}

func (m oidcMethod) createUser(ctx context.Context, providerName string, identity ExternalIdentity) (v1alpha1.User, error) {
	created := ProvisionUser(nil, providerName, identity)
	created.Spec.Methods = appendMethod(created.Spec.Methods, v1alpha1.MethodOIDC)
	if err := m.client.Create(ctx, &created, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
		return v1alpha1.User{}, err
	}
	created.Status.UpstreamSubject = identity.Subject
	if err := m.client.Status().Update(ctx, &created, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
		return v1alpha1.User{}, err
	}
	return created, nil
}

func (m oidcMethod) updateUser(ctx context.Context, name, providerName string, identity ExternalIdentity) (v1alpha1.User, error) {
	var updated v1alpha1.User
	err := retry.RetryOnConflict(conflictRetryBackoff, func() error {
		var fresh v1alpha1.User
		if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: name}, &fresh); err != nil {
			return err
		}
		next := ProvisionUser(&fresh, providerName, identity)
		next.Spec.Methods = appendMethod(next.Spec.Methods, v1alpha1.MethodOIDC)
		if err := m.client.Update(ctx, &next, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
			return err
		}
		updated = next
		return nil
	})
	if err != nil {
		return v1alpha1.User{}, err
	}
	err = retry.RetryOnConflict(conflictRetryBackoff, func() error {
		var fresh v1alpha1.User
		if err := m.client.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: name}, &fresh); err != nil {
			return err
		}
		fresh.Status.UpstreamSubject = identity.Subject
		if err := m.client.Status().Update(ctx, &fresh, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
			return err
		}
		updated = fresh
		return nil
	})
	if err != nil {
		return v1alpha1.User{}, err
	}
	return updated, nil
}

func (m oidcMethod) Enroll(ctx context.Context, user v1alpha1.User, input Answer) (Enrollment, error) {
	return Enrollment{}, errors.New("methods: oidc has no enrollment step")
}
