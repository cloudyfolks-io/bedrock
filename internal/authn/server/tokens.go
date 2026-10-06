package server

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/cloudyfolks-io/bedrock/internal/authn/store"
)

const devicePollBudget = 4 * time.Second

type tokenRequest interface {
	op.TokenRequest
	op.IDTokenRequest
}

type tokenServer struct {
	*op.LegacyServer
}

func tokenHandler(provider op.OpenIDProvider) http.Handler {
	endpoints := op.Endpoints{Token: op.NewEndpoint(pathToken)}
	return op.RegisterServer(tokenServer{op.NewLegacyServer(provider, endpoints)}, endpoints)
}

func (s tokenServer) CodeExchange(ctx context.Context, r *op.ClientRequest[oidc.AccessTokenRequest]) (*op.Response, error) {
	request, err := op.AuthRequestByCode(ctx, s.Provider().Storage(), r.Data.Code)
	if err != nil {
		return nil, err
	}
	if r.Client.AuthMethod() == oidc.AuthMethodNone || r.Data.CodeVerifier != "" {
		if err := op.AuthorizeCodeChallenge(r.Data.CodeVerifier, request.GetCodeChallenge()); err != nil {
			return nil, err
		}
	}
	if r.Client.GetID() != request.GetClientID() {
		return nil, oidc.ErrInvalidGrant()
	}
	if r.Data.RedirectURI != request.GetRedirectURI() {
		return nil, oidc.ErrInvalidGrant().WithDescription("redirect_uri does not correspond")
	}
	response, err := tokenResponse(ctx, s.Provider(), r.Client, request, r.Data.Code, "")
	if err != nil {
		return nil, err
	}
	if err := s.Provider().Storage().DeleteAuthRequest(ctx, request.GetID()); err != nil {
		return nil, err
	}
	return op.NewResponse(response), nil
}

func (s tokenServer) RefreshToken(ctx context.Context, r *op.ClientRequest[oidc.RefreshTokenRequest]) (*op.Response, error) {
	request, err := op.RefreshTokenRequestByRefreshToken(ctx, s.Provider().Storage(), r.Data.RefreshToken)
	if err != nil {
		return nil, err
	}
	if r.Client.GetID() != request.GetClientID() {
		return nil, oidc.ErrInvalidGrant()
	}
	if err := op.ValidateRefreshTokenScopes(r.Data.Scopes, request); err != nil {
		return nil, err
	}
	response, err := tokenResponse(ctx, s.Provider(), r.Client, request, "", r.Data.RefreshToken)
	if err != nil {
		return nil, err
	}
	return op.NewResponse(response), nil
}

func (s tokenServer) DeviceToken(ctx context.Context, r *op.ClientRequest[oidc.DeviceAccessTokenRequest]) (*op.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, devicePollBudget)
	defer cancel()
	state, err := op.CheckDeviceAuthorizationState(ctx, r.Client.GetID(), r.Data.DeviceCode, s.Provider())
	if err != nil {
		return nil, err
	}
	response, err := tokenResponse(ctx, s.Provider(), r.Client, state, "", "")
	if err != nil {
		return nil, err
	}
	return op.NewResponse(response), nil
}

func tokenResponse(ctx context.Context, provider op.OpenIDProvider, client op.Client, request tokenRequest, code, refreshToken string) (*oidc.AccessTokenResponse, error) {
	accessToken, rotated, validity, err := op.CreateAccessToken(ctx, request, client.AccessTokenType(), provider, client, refreshToken)
	if err != nil {
		return nil, err
	}
	idToken, err := idTokenFor(ctx, provider, client, request, accessToken, code)
	if err != nil {
		return nil, err
	}
	return &oidc.AccessTokenResponse{
		AccessToken:  accessToken,
		IDToken:      idToken,
		RefreshToken: rotated,
		TokenType:    oidc.BearerToken,
		ExpiresIn:    uint64(validity.Seconds()),
		Scope:        request.GetScopes(),
	}, nil
}

func idTokenFor(ctx context.Context, provider op.OpenIDProvider, client op.Client, request tokenRequest, accessToken, code string) (string, error) {
	if !slices.Contains(request.GetScopes(), oidc.ScopeOpenID) {
		return "", nil
	}
	return op.CreateIDToken(ctx, op.IssuerFromContext(ctx), store.ForIDToken(request), client.IDTokenLifetime(), accessToken, code, provider.Storage(), client)
}
