package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	authRequestLifetime = 30 * time.Minute
	authCodeLifetime    = 60 * time.Second
	authRequestIDLength = 26
	authRequestAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
)

var errLoginNotDone = errors.New("login is not finished")

type AuthRequest struct {
	Object v1alpha1.AuthRequest
}

func (r AuthRequest) GetID() string {
	return r.Object.Name
}

func (r AuthRequest) GetACR() string {
	return ""
}

func (r AuthRequest) GetAMR() []string {
	return r.Object.Status.AMR
}

func (r AuthRequest) GetAudience() []string {
	return []string{audienceBedrock, r.Object.Spec.ClientID}
}

func (r AuthRequest) GetAuthTime() time.Time {
	if r.Object.Status.AuthTime == nil {
		return time.Time{}
	}
	return r.Object.Status.AuthTime.Time
}

func (r AuthRequest) GetClientID() string {
	return r.Object.Spec.ClientID
}

func (r AuthRequest) GetCodeChallenge() *oidc.CodeChallenge {
	if r.Object.Spec.CodeChallenge == "" {
		return nil
	}
	return &oidc.CodeChallenge{Challenge: r.Object.Spec.CodeChallenge, Method: oidc.CodeChallengeMethod(r.Object.Spec.CodeChallengeMethod)}
}

func (r AuthRequest) GetNonce() string {
	return r.Object.Spec.Nonce
}

func (r AuthRequest) GetRedirectURI() string {
	return r.Object.Spec.RedirectURI
}

func (r AuthRequest) GetResponseType() oidc.ResponseType {
	return oidc.ResponseType(r.Object.Spec.ResponseType)
}

func (r AuthRequest) GetResponseMode() oidc.ResponseMode {
	return oidc.ResponseMode(r.Object.Spec.ResponseMode)
}

func (r AuthRequest) GetScopes() []string {
	return r.Object.Spec.Scopes
}

func (r AuthRequest) GetState() string {
	return r.Object.Spec.State
}

func (r AuthRequest) GetSubject() string {
	return r.Object.Status.Subject
}

func (r AuthRequest) Done() bool {
	return r.Object.Status.Done
}

func (r AuthRequest) sessionID() string {
	return r.Object.Status.Session
}

func FromOIDC(req *oidc.AuthRequest, id string, now time.Time) v1alpha1.AuthRequest {
	return v1alpha1.AuthRequest{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: release.SystemNamespace, Labels: objectLabels("AuthRequest", id)},
		Spec: v1alpha1.AuthRequestSpec{
			ClientID:            req.ClientID,
			RedirectURI:         req.RedirectURI,
			Scopes:              slices.Clone([]string(req.Scopes)),
			State:               req.State,
			Nonce:               req.Nonce,
			ResponseType:        string(req.ResponseType),
			ResponseMode:        string(req.ResponseMode),
			CodeChallenge:       req.CodeChallenge,
			CodeChallengeMethod: string(req.CodeChallengeMethod),
			Prompt:              slices.Clone([]string(req.Prompt)),
			MaxAge:              maxAge(req.MaxAge),
			LoginHint:           req.LoginHint,
			UILocales:           localeNames(req.UILocales),
			ExpiresAt:           metav1.NewTime(now.Add(authRequestLifetime)),
		},
	}
}

func maxAge(value *uint) *int64 {
	if value == nil {
		return nil
	}
	seconds := int64(*value)
	return &seconds
}

func localeNames(locales oidc.Locales) []string {
	names := make([]string, 0, len(locales))
	for _, locale := range locales {
		names = append(names, locale.String())
	}
	return names
}

func pkceMissing(oauth v1alpha1.OAuthClient, req *oidc.AuthRequest) bool {
	if req.CodeChallenge == "" {
		return oauth.Spec.Public
	}
	return req.CodeChallengeMethod != oidc.CodeChallengeMethodS256
}

func (s *Store) CreateAuthRequest(ctx context.Context, req *oidc.AuthRequest, _ string) (op.AuthRequest, error) {
	oauth, err := s.oauthClient(ctx, req.ClientID)
	if err != nil {
		return nil, err
	}
	if !RedirectAllowed(oauth, req.RedirectURI) {
		return nil, oidc.ErrInvalidRequestRedirectURI().WithDescription("redirect_uri is not registered for this client")
	}
	if pkceMissing(oauth, req) {
		return nil, oidc.ErrInvalidRequest().WithDescription("this client must use PKCE with S256")
	}
	id, err := secret.FromAlphabet(s.random, authRequestAlphabet, authRequestIDLength)
	if err != nil {
		return nil, err
	}
	stored := FromOIDC(req, id, s.clock())
	if err := s.client.Create(ctx, &stored, authnOwner()); err != nil {
		return nil, err
	}
	return AuthRequest{Object: stored}, nil
}

func (s *Store) authRequest(ctx context.Context, id string) (v1alpha1.AuthRequest, error) {
	var stored v1alpha1.AuthRequest
	if err := s.reader.Get(ctx, objectKey(id), &stored); err != nil {
		return v1alpha1.AuthRequest{}, err
	}
	if due(stored.Spec.ExpiresAt, s.clock()) {
		return v1alpha1.AuthRequest{}, notFoundError{kind: "AuthRequest"}
	}
	return stored, nil
}

func (s *Store) AuthRequestByID(ctx context.Context, id string) (op.AuthRequest, error) {
	stored, err := s.authRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	return AuthRequest{Object: stored}, nil
}

func (s *Store) SaveLogin(ctx context.Context, id string, update func(v1alpha1.AuthRequest) v1alpha1.AuthRequest) (v1alpha1.AuthRequest, error) {
	var saved v1alpha1.AuthRequest
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := s.authRequest(ctx, id)
		if err != nil {
			return err
		}
		next := update(*current.DeepCopy())
		if err := s.client.Status().Update(ctx, &next, authnOwner()); err != nil {
			return err
		}
		saved = next
		return nil
	})
	return saved, err
}

func (s *Store) SaveAuthCode(ctx context.Context, id, code string) error {
	name := secret.SHA256Hex(code)
	stored := v1alpha1.AuthCode{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: release.SystemNamespace, Labels: objectLabels("AuthCode", name)},
		Spec:       v1alpha1.AuthCodeSpec{AuthRequest: id, ExpiresAt: metav1.NewTime(s.clock().Add(authCodeLifetime))},
	}
	return s.client.Create(ctx, &stored, authnOwner())
}

func (s *Store) AuthRequestByCode(ctx context.Context, code string) (op.AuthRequest, error) {
	var stored v1alpha1.AuthCode
	if err := s.reader.Get(ctx, objectKey(secret.SHA256Hex(code)), &stored); err != nil {
		return nil, err
	}
	if err := s.client.Delete(ctx, &stored, client.Preconditions{UID: &stored.UID, ResourceVersion: &stored.ResourceVersion}); err != nil {
		return nil, err
	}
	if due(stored.Spec.ExpiresAt, s.clock()) {
		return nil, notFoundError{kind: "AuthCode"}
	}
	request, err := s.authRequest(ctx, stored.Spec.AuthRequest)
	if err != nil {
		return nil, err
	}
	if !request.Status.Done {
		return nil, errLoginNotDone
	}
	return AuthRequest{Object: request}, nil
}

func (s *Store) DeleteAuthRequest(ctx context.Context, id string) error {
	stale := &v1alpha1.AuthRequest{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: release.SystemNamespace}}
	return client.IgnoreNotFound(s.client.Delete(ctx, stale))
}
