package store

import (
	"context"
	"slices"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
)

const exchangeLifetime = time.Hour

func ExchangeAllowed(client v1alpha1.OAuthClient, audiences []string) bool {
	if client.Spec.Public || client.Spec.TokenExchange == nil || len(audiences) == 0 || !slices.Contains(client.Spec.GrantTypes, v1alpha1.GrantTokenExchange) {
		return false
	}
	for _, audience := range audiences {
		if !slices.Contains(client.Spec.TokenExchange.Audiences, audience) {
			return false
		}
	}
	return true
}

func ExchangeExpiry(subjectExpiry, now time.Time) time.Time {
	limit := now.Add(exchangeLifetime)
	if subjectExpiry.Before(limit) {
		return subjectExpiry
	}
	return limit
}

func (s *Store) ValidateTokenExchangeRequest(ctx context.Context, request op.TokenExchangeRequest) error {
	oauth, err := s.oauthClient(ctx, request.GetClientID())
	if err != nil {
		return oidc.ErrInvalidClient().WithParent(err)
	}
	if oauth.Spec.Public || !slices.Contains(oauth.Spec.GrantTypes, v1alpha1.GrantTokenExchange) {
		return oidc.ErrUnauthorizedClient().WithDescription("the client may not exchange tokens")
	}
	if !ExchangeAllowed(oauth, request.GetAudience()) {
		return oidc.ErrInvalidTarget().WithDescription("an audience is not allowed for this client")
	}
	if request.GetExchangeSubjectTokenType() != oidc.AccessTokenType || request.GetExchangeActor() != "" {
		return oidc.ErrInvalidRequest().WithDescription("only a Bedrock access token can be exchanged")
	}
	if requested := request.GetRequestedTokenType(); requested != "" && requested != oidc.AccessTokenType {
		return oidc.ErrInvalidRequest().WithDescription("only access tokens are issued")
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return err
	}
	if !bedrockAccessToken(request.GetExchangeSubjectTokenClaims(), policy.Issuer(settings)) {
		return oidc.ErrInvalidRequest().WithDescription("subject_token is not a Bedrock access token")
	}
	if _, err := s.Subject(ctx, request.GetSubject()); err != nil {
		return oidc.ErrInvalidGrant().WithParent(err)
	}
	request.SetRequestedTokenType(oidc.AccessTokenType)
	return nil
}

func (s *Store) CreateTokenExchangeRequest(context.Context, op.TokenExchangeRequest) error {
	return nil
}

func (s *Store) GetPrivateClaimsFromTokenExchangeRequest(ctx context.Context, request op.TokenExchangeRequest) (map[string]any, error) {
	subject, err := s.Subject(ctx, request.GetSubject())
	if err != nil {
		return nil, err
	}
	return exchangeClaims(subject.User, request.GetExchangeSubjectTokenClaims(), request.GetClientID()), nil
}

func (s *Store) SetUserinfoFromTokenExchangeRequest(ctx context.Context, userinfo *oidc.UserInfo, request op.TokenExchangeRequest) error {
	subject, err := s.Subject(ctx, request.GetSubject())
	if err != nil {
		return err
	}
	claims := request.GetExchangeSubjectTokenClaims()
	*userinfo = withClaims(*Userinfo(subject.User, stringsOf(claims["groups"]), allScopes()), exchangeClaims(subject.User, claims, request.GetClientID()))
	return nil
}

func (s *Store) SetIntrospectionFromToken(ctx context.Context, response *oidc.IntrospectionResponse, _, subject, _ string) error {
	found, err := s.Subject(ctx, subject)
	if err != nil {
		return err
	}
	response.SetUserInfo(Userinfo(found.User, found.Groups, allScopes()))
	return nil
}

func exchangeClaims(user v1alpha1.User, subjectClaims map[string]any, clientID string) map[string]any {
	claims := Claims(user, stringsOf(subjectClaims["groups"]), stringsOf(subjectClaims["amr"]), "")
	claims["act"] = map[string]any{"sub": clientID}
	return claims
}

func bedrockAccessToken(claims map[string]any, issuer string) bool {
	return claims["iss"] == issuer && slices.Contains(stringsOf(claims["aud"]), audienceBedrock)
}

func claimTime(value any) time.Time {
	seconds, ok := value.(float64)
	if !ok {
		return time.Time{}
	}
	return time.Unix(int64(seconds), 0).UTC()
}

func stringsOf(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []string:
		return slices.Clone(typed)
	case []any:
		texts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				texts = append(texts, text)
			}
		}
		return texts
	}
	return nil
}

func allScopes() []string {
	scopes := make([]string, 0, len(accessTokenScopeCodes))
	for _, entry := range accessTokenScopeCodes {
		scopes = append(scopes, entry.scope)
	}
	return scopes
}
