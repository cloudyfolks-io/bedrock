package store

import (
	"context"
	"errors"
	"maps"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

var ErrUserDisabled = errors.New("user is disabled")

type sessionRequest interface {
	sessionID() string
}

type amrRequest interface {
	GetAMR() []string
}

func (s *Store) User(ctx context.Context, name string) (v1alpha1.User, error) {
	var user v1alpha1.User
	if err := s.reader.Get(ctx, objectKey(name), &user); err != nil {
		return v1alpha1.User{}, err
	}
	return user, nil
}

func (s *Store) UserByUsername(ctx context.Context, raw string) (v1alpha1.User, error) {
	username, err := v1alpha1.NormalizeUsername(raw)
	if err != nil {
		return v1alpha1.User{}, err
	}
	user, err := s.User(ctx, v1alpha1.UserObjectName(username))
	if err != nil {
		return v1alpha1.User{}, err
	}
	if user.Spec.Username != username {
		return v1alpha1.User{}, notFoundError{kind: "User"}
	}
	return user, nil
}

func (s *Store) Groups(ctx context.Context) ([]v1alpha1.Group, error) {
	var list v1alpha1.GroupList
	if err := s.reader.List(ctx, &list, client.InNamespace(release.SystemNamespace)); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (s *Store) Subject(ctx context.Context, name string) (methods.Subject, error) {
	user, err := s.User(ctx, name)
	if err != nil {
		return methods.Subject{}, err
	}
	if user.Spec.Disabled {
		return methods.Subject{}, ErrUserDisabled
	}
	groups, err := s.Groups(ctx)
	if err != nil {
		return methods.Subject{}, err
	}
	return methods.Subject{User: user, Groups: policy.EffectiveGroups(user, groups)}, nil
}

func (s *Store) SetUserinfoFromScopes(context.Context, *oidc.UserInfo, string, string, []string) error {
	return nil
}

func (s *Store) SetUserinfoFromRequest(ctx context.Context, userinfo *oidc.UserInfo, request op.IDTokenRequest, scopes []string) error {
	subject, err := s.Subject(ctx, request.GetSubject())
	if err != nil {
		return err
	}
	*userinfo = withClaims(*Userinfo(subject.User, subject.Groups, scopes), Claims(subject.User, subject.Groups, request.GetAMR(), sessionOf(request)))
	return nil
}

func (s *Store) SetUserinfoFromToken(ctx context.Context, userinfo *oidc.UserInfo, _, subject, _ string) error {
	found, err := s.Subject(ctx, subject)
	if err != nil {
		return err
	}
	*userinfo = *Userinfo(found.User, found.Groups, allScopes())
	return nil
}

func (s *Store) GetPrivateClaimsFromScopes(ctx context.Context, userID, _ string, _ []string) (map[string]any, error) {
	subject, err := s.Subject(ctx, userID)
	if err != nil {
		return nil, err
	}
	return Claims(subject.User, subject.Groups, nil, ""), nil
}

func (s *Store) GetPrivateClaimsFromRequest(ctx context.Context, request op.TokenRequest, _ []string) (map[string]any, error) {
	subject, err := s.Subject(ctx, request.GetSubject())
	if err != nil {
		return nil, err
	}
	return Claims(subject.User, subject.Groups, amrOf(request), sessionOf(request)), nil
}

func Claims(user v1alpha1.User, groups []string, amr []string, sid string) map[string]any {
	claims := map[string]any{"groups": append([]string{}, groups...)}
	if user.Spec.Email != "" {
		claims["email"] = user.Spec.Email
	}
	if user.Spec.DisplayName != "" {
		claims["name"] = user.Spec.DisplayName
	}
	if len(amr) > 0 {
		claims["amr"] = append([]string{}, amr...)
	}
	if sid != "" {
		claims["sid"] = sid
	}
	return claims
}

func Userinfo(user v1alpha1.User, groups []string, scopes []string) *oidc.UserInfo {
	info := &oidc.UserInfo{}
	for _, scope := range scopes {
		switch scope {
		case oidc.ScopeOpenID:
			info.Subject = user.Name
		case oidc.ScopeProfile:
			info.Name = user.Spec.DisplayName
			info.PreferredUsername = user.Spec.Username
		case oidc.ScopeEmail:
			info.Email = user.Spec.Email
		case scopeGroups:
			info.AppendClaims(scopeGroups, append([]string{}, groups...))
		}
	}
	return info
}

func allScopes() []string {
	return []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail, scopeGroups}
}

func withClaims(info oidc.UserInfo, claims map[string]any) oidc.UserInfo {
	merged := maps.Clone(claims)
	maps.Copy(merged, info.Claims)
	info.Claims = merged
	return info
}

func sessionOf(request any) string {
	if carrier, ok := request.(sessionRequest); ok {
		return carrier.sessionID()
	}
	return ""
}

func amrOf(request any) []string {
	if carrier, ok := request.(amrRequest); ok {
		return carrier.GetAMR()
	}
	return nil
}
