package store

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	refreshTokenLength = 43
	familyLength       = 22
	tokenIDLength      = 22
	fieldUserRef       = "spec.userRef"
	scopeIDSeparator   = "."
)

var (
	errRefreshReused = errors.New("refresh token was already used")
	errNoIdentity    = errors.New("a refresh token needs a request with a subject")
)

var accessTokenScopeCodes = []struct {
	scope string
	code  byte
}{
	{oidc.ScopeOpenID, 'o'},
	{oidc.ScopeProfile, 'p'},
	{oidc.ScopeEmail, 'e'},
	{scopeGroups, 'g'},
}

func encodeAccessTokenScopes(scopes []string) string {
	granted := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		granted[scope] = true
	}
	encoded := make([]byte, 0, len(accessTokenScopeCodes))
	for _, entry := range accessTokenScopeCodes {
		if granted[entry.scope] {
			encoded = append(encoded, entry.code)
		}
	}
	return string(encoded)
}

func decodeAccessTokenScopes(encoded string) ([]string, bool) {
	codeToScope := make(map[byte]string, len(accessTokenScopeCodes))
	for _, entry := range accessTokenScopeCodes {
		codeToScope[entry.code] = entry.scope
	}
	scopes := make([]string, 0, len(encoded))
	seen := make(map[byte]bool, len(encoded))
	for i := range len(encoded) {
		scope, known := codeToScope[encoded[i]]
		if !known || seen[encoded[i]] {
			return nil, false
		}
		seen[encoded[i]] = true
		scopes = append(scopes, scope)
	}
	return scopes, true
}

func accessTokenID(random string, scopes []string) string {
	return random + scopeIDSeparator + encodeAccessTokenScopes(scopes)
}

func scopesOfAccessTokenID(id string) ([]string, bool) {
	random, encoded, cut := strings.Cut(id, scopeIDSeparator)
	if !cut || len(random) != tokenIDLength {
		return nil, false
	}
	return decodeAccessTokenScopes(encoded)
}

type RefreshTokenRequest struct {
	Object v1alpha1.RefreshToken
	scopes []string
}

func (r *RefreshTokenRequest) GetAMR() []string {
	return r.Object.Spec.AMR
}

func (r *RefreshTokenRequest) GetAudience() []string {
	return r.Object.Spec.Audience
}

func (r *RefreshTokenRequest) GetAuthTime() time.Time {
	return r.Object.Spec.AuthTime.Time
}

func (r *RefreshTokenRequest) GetClientID() string {
	return r.Object.Spec.ClientID
}

func (r *RefreshTokenRequest) GetScopes() []string {
	if r.scopes != nil {
		return r.scopes
	}
	return r.Object.Spec.Scopes
}

func (r *RefreshTokenRequest) GetSubject() string {
	return r.Object.Spec.UserRef
}

func (r *RefreshTokenRequest) SetCurrentScopes(scopes []string) {
	r.scopes = slices.Clone(scopes)
}

func (r *RefreshTokenRequest) sessionID() string {
	return r.Object.Spec.Session
}

func (s *Store) CreateAccessToken(_ context.Context, request op.TokenRequest) (string, time.Time, error) {
	random, err := secret.Base62(s.random, tokenIDLength)
	if err != nil {
		return "", time.Time{}, err
	}
	return accessTokenID(random, request.GetScopes()), accessTokenExpiry(request, s.clock()), nil
}

func accessTokenExpiry(request op.TokenRequest, now time.Time) time.Time {
	exchange, ok := request.(op.TokenExchangeRequest)
	if !ok {
		return now.Add(accessTokenLifetime)
	}
	return ExchangeExpiry(claimTime(exchange.GetExchangeSubjectTokenClaims()["exp"]), now)
}

func (s *Store) CreateAccessAndRefreshTokens(ctx context.Context, request op.TokenRequest, _ string) (string, string, time.Time, error) {
	identity, ok := request.(op.IDTokenRequest)
	if !ok {
		return "", "", time.Time{}, errNoIdentity
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return "", "", time.Time{}, err
	}
	family, err := s.familyOf(request)
	if err != nil {
		return "", "", time.Time{}, err
	}
	token, err := secret.Base62(s.random, refreshTokenLength)
	if err != nil {
		return "", "", time.Time{}, err
	}
	now := s.clock()
	next := refreshTokenObject(secret.SHA256Hex(token), family, identity, sessionOf(request), now.Add(settings.RefreshTTL))
	if err := s.storeRefreshToken(ctx, request, next, now); err != nil {
		return "", "", time.Time{}, err
	}
	accessID, expiry, err := s.CreateAccessToken(ctx, request)
	if err != nil {
		return "", "", time.Time{}, err
	}
	return accessID, token, expiry, nil
}

func (s *Store) familyOf(request op.TokenRequest) (string, error) {
	if previous, ok := request.(*RefreshTokenRequest); ok {
		return previous.Object.Spec.Family, nil
	}
	return secret.Base62(s.random, familyLength)
}

func refreshTokenObject(name, family string, request op.IDTokenRequest, session string, expiresAt time.Time) v1alpha1.RefreshToken {
	labels := objectLabels("RefreshToken", name)
	labels[v1alpha1.LabelFamily] = family
	return v1alpha1.RefreshToken{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: release.SystemNamespace, Labels: labels},
		Spec: v1alpha1.RefreshTokenSpec{
			Family:    family,
			UserRef:   request.GetSubject(),
			ClientID:  request.GetClientID(),
			Scopes:    slices.Clone(request.GetScopes()),
			Audience:  slices.Clone(request.GetAudience()),
			AMR:       slices.Clone(request.GetAMR()),
			AuthTime:  metav1.NewTime(request.GetAuthTime()),
			Session:   session,
			ExpiresAt: metav1.NewTime(expiresAt),
		},
	}
}

func (s *Store) storeRefreshToken(ctx context.Context, request op.TokenRequest, next v1alpha1.RefreshToken, now time.Time) error {
	previous, ok := request.(*RefreshTokenRequest)
	if !ok {
		return s.client.Create(ctx, &next, authnOwner())
	}
	return s.rotate(ctx, previous.Object, next, now)
}

func (s *Store) rotate(ctx context.Context, previous, next v1alpha1.RefreshToken, now time.Time) error {
	used := previous.DeepCopy()
	used.Status.UsedAt = &metav1.Time{Time: now}
	err := s.client.Status().Update(ctx, used, authnOwner())
	if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
		return reused(s.revokeFamily(ctx, previous.Spec.Family))
	}
	if err != nil {
		return err
	}
	if err := s.client.Create(ctx, &next, authnOwner()); err != nil {
		return err
	}
	if err := s.reader.Get(ctx, objectKey(previous.Name), &v1alpha1.RefreshToken{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		return reused(client.IgnoreNotFound(s.client.Delete(ctx, &next)), s.revokeFamily(ctx, previous.Spec.Family))
	}
	return nil
}

func reused(errs ...error) error {
	return oidc.ErrInvalidGrant().WithDescription("refresh token was already used").WithParent(errors.Join(append([]error{errRefreshReused}, errs...)...))
}

func (s *Store) revokeFamily(ctx context.Context, family string) error {
	selector := []client.DeleteAllOfOption{client.InNamespace(release.SystemNamespace), client.MatchingLabels{v1alpha1.LabelFamily: family}}
	if err := s.client.DeleteAllOf(ctx, &v1alpha1.RefreshToken{}, selector...); err != nil {
		slog.ErrorContext(ctx, "refresh token family revocation failed", "error", err)
		return err
	}
	return nil
}

func (s *Store) refreshToken(ctx context.Context, token string) (v1alpha1.RefreshToken, error) {
	var stored v1alpha1.RefreshToken
	if err := s.reader.Get(ctx, objectKey(secret.SHA256Hex(token)), &stored); err != nil {
		return v1alpha1.RefreshToken{}, err
	}
	if due(stored.Spec.ExpiresAt, s.clock()) {
		return v1alpha1.RefreshToken{}, notFoundError{kind: "RefreshToken"}
	}
	return stored, nil
}

func (s *Store) TokenRequestByRefreshToken(ctx context.Context, token string) (op.RefreshTokenRequest, error) {
	stored, err := s.refreshToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if stored.Status.UsedAt != nil {
		return nil, errors.Join(errRefreshReused, s.revokeFamily(ctx, stored.Spec.Family))
	}
	if _, err := s.Subject(ctx, stored.Spec.UserRef); err != nil {
		return nil, err
	}
	return &RefreshTokenRequest{Object: stored}, nil
}

func (s *Store) GetRefreshTokenInfo(ctx context.Context, clientID, token string) (string, string, error) {
	stored, err := s.refreshToken(ctx, token)
	if err != nil && !isNotFound(err) {
		return "", "", err
	}
	if err != nil || stored.Spec.ClientID != clientID {
		return "", "", op.ErrInvalidRefreshToken
	}
	return stored.Spec.UserRef, stored.Name, nil
}

func (s *Store) RevokeToken(ctx context.Context, tokenOrTokenID, userID, clientID string) *oidc.Error {
	name := tokenOrTokenID
	if userID == "" {
		name = secret.SHA256Hex(tokenOrTokenID)
	}
	var stored v1alpha1.RefreshToken
	err := s.reader.Get(ctx, objectKey(name), &stored)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return oidc.ErrServerError().WithParent(err)
	}
	if stored.Spec.ClientID != clientID || (userID != "" && stored.Spec.UserRef != userID) {
		return oidc.ErrInvalidClient().WithDescription("the token was issued to another client")
	}
	if err := s.revokeFamily(ctx, stored.Spec.Family); err != nil {
		return oidc.ErrServerError().WithParent(err)
	}
	return nil
}

func (s *Store) TerminateSession(ctx context.Context, userID, clientID string) error {
	byUser := client.MatchingFields{fieldUserRef: userID}
	var tokens v1alpha1.RefreshTokenList
	if err := s.reader.List(ctx, &tokens, client.InNamespace(release.SystemNamespace), byUser); err != nil {
		return err
	}
	for i := range tokens.Items {
		if tokens.Items[i].Spec.ClientID != clientID {
			continue
		}
		if err := client.IgnoreNotFound(s.client.Delete(ctx, &tokens.Items[i])); err != nil {
			return err
		}
	}
	return s.client.DeleteAllOf(ctx, &v1alpha1.Session{}, client.InNamespace(release.SystemNamespace), byUser)
}

func (s *Store) RevokeUser(ctx context.Context, user string) error {
	byUser := []client.DeleteAllOfOption{client.InNamespace(release.SystemNamespace), client.MatchingFields{fieldUserRef: user}}
	return errors.Join(
		s.client.DeleteAllOf(ctx, &v1alpha1.RefreshToken{}, byUser...),
		s.client.DeleteAllOf(ctx, &v1alpha1.Session{}, byUser...),
		s.client.DeleteAllOf(ctx, &v1alpha1.APIToken{}, byUser...),
	)
}
