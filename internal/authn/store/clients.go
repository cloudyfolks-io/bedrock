package store

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
)

const (
	clientSecretKey = "clientSecret"
	loopbackHost    = "127.0.0.1"
)

var (
	errInvalidClient     = errors.New("invalid client credentials")
	errJWTProfileRefused = errors.New("jwt profile grant is not supported")
)

type Client struct {
	oauth v1alpha1.OAuthClient
}

func ToClient(c v1alpha1.OAuthClient) Client {
	return Client{oauth: c}
}

func (c Client) GetID() string {
	return c.oauth.Spec.ClientID
}

func (c Client) RedirectURIs() []string {
	return slices.Clone(c.oauth.Spec.RedirectURIs)
}

func (c Client) PostLogoutRedirectURIs() []string {
	return nil
}

func (c Client) ApplicationType() op.ApplicationType {
	if c.oauth.Spec.Public {
		return op.ApplicationTypeNative
	}
	return op.ApplicationTypeWeb
}

func (c Client) AuthMethod() oidc.AuthMethod {
	if c.oauth.Spec.Public {
		return oidc.AuthMethodNone
	}
	return oidc.AuthMethodBasic
}

func (c Client) ResponseTypes() []oidc.ResponseType {
	if slices.Contains(c.oauth.Spec.GrantTypes, v1alpha1.GrantAuthorizationCode) {
		return []oidc.ResponseType{oidc.ResponseTypeCode}
	}
	return nil
}

func (c Client) GrantTypes() []oidc.GrantType {
	grants := make([]oidc.GrantType, 0, len(c.oauth.Spec.GrantTypes))
	for _, grant := range c.oauth.Spec.GrantTypes {
		grants = append(grants, oidc.GrantType(grant))
	}
	return grants
}

func (c Client) LoginURL(id string) string {
	return "/login/?authRequest=" + url.QueryEscape(id)
}

func (c Client) AccessTokenType() op.AccessTokenType {
	return op.AccessTokenTypeJWT
}

func (c Client) IDTokenLifetime() time.Duration {
	return idTokenLifetime
}

func (c Client) DevMode() bool {
	return false
}

func (c Client) RestrictAdditionalIdTokenScopes() func(scopes []string) []string {
	return keepScopes
}

func (c Client) RestrictAdditionalAccessTokenScopes() func(scopes []string) []string {
	return keepScopes
}

func (c Client) IsScopeAllowed(scope string) bool {
	return scope == scopeGroups || scope == oidc.ScopeOfflineAccess
}

func (c Client) IDTokenUserinfoClaimsAssertion() bool {
	return false
}

func (c Client) ClockSkew() time.Duration {
	return 0
}

func keepScopes(scopes []string) []string {
	return scopes
}

func RedirectAllowed(client v1alpha1.OAuthClient, uri string) bool {
	if uri == "" {
		return false
	}
	if slices.Contains(client.Spec.RedirectURIs, uri) {
		return true
	}
	if !client.Spec.Public {
		return false
	}
	requested, err := url.Parse(uri)
	if err != nil || !loopbackRequest(requested) {
		return false
	}
	return slices.ContainsFunc(client.Spec.RedirectURIs, func(registered string) bool {
		return loopbackMatch(registered, requested)
	})
}

func loopbackRequest(requested *url.URL) bool {
	return requested.Scheme == "http" &&
		requested.Hostname() == loopbackHost &&
		requested.Port() != "" &&
		requested.User == nil &&
		requested.Opaque == "" &&
		requested.Fragment == ""
}

func loopbackMatch(registered string, requested *url.URL) bool {
	base, err := url.Parse(registered)
	return err == nil &&
		base.Scheme == "http" &&
		base.Host == loopbackHost &&
		base.Path == requested.Path &&
		base.RawQuery == requested.RawQuery
}

func (s *Store) oauthClient(ctx context.Context, clientID string) (v1alpha1.OAuthClient, error) {
	var oauth v1alpha1.OAuthClient
	if err := s.reader.Get(ctx, objectKey(clientID), &oauth); err != nil {
		return v1alpha1.OAuthClient{}, err
	}
	if oauth.Spec.ClientID != clientID {
		return v1alpha1.OAuthClient{}, notFoundError{kind: "OAuthClient"}
	}
	return oauth, nil
}

func (s *Store) GetClientByClientID(ctx context.Context, clientID string) (op.Client, error) {
	oauth, err := s.oauthClient(ctx, clientID)
	if err != nil {
		return nil, err
	}
	return ToClient(oauth), nil
}

func (s *Store) AuthorizeClientIDSecret(ctx context.Context, clientID, clientSecret string) error {
	oauth, err := s.oauthClient(ctx, clientID)
	if isNotFound(err) {
		return errInvalidClient
	}
	if err != nil {
		return err
	}
	if oauth.Spec.Public || oauth.Spec.SecretRef == "" {
		return errInvalidClient
	}
	var stored corev1.Secret
	if err := s.reader.Get(ctx, objectKey(oauth.Spec.SecretRef), &stored); err != nil {
		return err
	}
	expected := string(stored.Data[clientSecretKey])
	if expected == "" || !secret.Equal(expected, clientSecret) {
		return errInvalidClient
	}
	return nil
}

func (s *Store) ValidateJWTProfileScopes(context.Context, string, []string) ([]string, error) {
	return nil, errJWTProfileRefused
}

func (s *Store) GetKeyByIDAndClientID(context.Context, string, string) (*jose.JSONWebKey, error) {
	return nil, errJWTProfileRefused
}
