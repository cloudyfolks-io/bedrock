package login

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/authn/store"
	"github.com/cloudyfolks-io/bedrock/internal/saxess"
)

const (
	AnswerSAxessStart = "saxess-start"
	ChallengeSAxess   = v1alpha1.MethodSAxess
	saxessTTL         = 2 * time.Minute
)

var (
	errSAxessPending = errors.New("login: no sAxess approval to consume")
	errSAxessUser    = errors.New("login: sAxess user changed")
)

type SAxessBackend interface {
	CreateChallenge(ctx context.Context, operatorID, browserTransactionID string, ttl time.Duration) (saxess.Challenge, error)
	RedeemBrowserApproval(ctx context.Context, operatorID, browserTransactionID, authReqID string) (string, error)
}

type SAxessConfig struct {
	Backend     SAxessBackend
	ClientID    string
	RedirectURI string
}

func (s SAxessConfig) eligible(spec v1alpha1.AuthRequestSpec, oauth v1alpha1.OAuthClientSpec) bool {
	if s.ClientID == "" || s.RedirectURI == "" || spec.ClientID != s.ClientID || oauth.ClientID != s.ClientID ||
		spec.RedirectURI != s.RedirectURI || !slices.Contains(oauth.RedirectURIs, s.RedirectURI) ||
		!slices.Contains(oauth.GrantTypes, v1alpha1.GrantAuthorizationCode) ||
		spec.DeviceRequest != "" || spec.ResponseType != "code" || spec.CodeChallengeMethod != "S256" {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(spec.CodeChallenge)
	return err == nil && len(raw) == sha256.Size && base64.RawURLEncoding.EncodeToString(raw) == spec.CodeChallenge
}

func checkSAxessScope(ctx context.Context, deps Deps, request v1alpha1.AuthRequest) error {
	cfg := deps.SAxess
	if cfg == nil || cfg.Backend == nil || request.Spec.ClientID != cfg.ClientID {
		return errUnexpectedAnswer
	}
	var oauth v1alpha1.OAuthClient
	if err := deps.Client.Get(ctx, objectKey(request.Spec.ClientID), &oauth); err != nil {
		if apierrors.IsNotFound(err) {
			return errUnexpectedAnswer
		}
		return err
	}
	if !cfg.eligible(request.Spec, oauth.Spec) {
		return errUnexpectedAnswer
	}
	return nil
}

func checkSAxessLogin(ctx context.Context, deps Deps, request v1alpha1.AuthRequest, state v1alpha1.LoginState) error {
	if state.Primary != ChallengeSAxess {
		return nil
	}
	if err := checkSAxessScope(ctx, deps, request); err != nil {
		if errors.Is(err, errUnexpectedAnswer) {
			return errSAxessUser
		}
		return err
	}
	subject, err := saxessAdminSubject(ctx, deps, state.Username)
	if err != nil {
		return err
	}
	if (state.Step != ChallengeSAxess || request.Status.Subject != "") && request.Status.Subject != subject.User.Name {
		return errSAxessUser
	}
	return checkSAxessUser(state, subject.User)
}

func dispatchSAxess(r *http.Request, deps Deps, request v1alpha1.AuthRequest, given methods.Answer) (Step, v1alpha1.AuthRequest, error) {
	cfg := deps.SAxess
	state := request.Status.Login
	start := given.Type == AnswerSAxessStart
	if request.Status.Subject != "" || len(state.Completed) != 0 ||
		(start && state.Step != methods.ChallengeUsername) ||
		(!start && (state.Step != ChallengeSAxess || state.Upstream == "")) {
		return Step{}, request, errUnexpectedAnswer
	}
	ctx := r.Context()
	if err := checkSAxessScope(ctx, deps, request); err != nil {
		return Step{}, request, err
	}
	now := deps.Clock()
	nonce, deadline := saxessPending(state)
	if !start && (nonce == "" || !now.Before(deadline)) {
		return failed(state, errorExpired), request, nil
	}
	username := state.Username
	bucket := ChallengeSAxess + "\x00" + username
	if start {
		username = usernameOf(given.Username)
		bucket = AnswerSAxessStart
	}
	// Keep approval polling from spending password or local MFA attempts.
	if !deps.Limiter.Allow(AttemptKey(ClientIP(r), bucket), now) {
		if start {
			facts, err := loadFacts(ctx, deps, request, nil)
			if err != nil {
				return Step{}, request, err
			}
			return usernameLimited(facts), request, errSAxessPending
		}
		step := ask(state, Facts{})
		step.Challenge.Error = challengeError(methods.FailureRateLimited)
		return step, request, errSAxessPending
	}
	subject, err := saxessAdminSubject(ctx, deps, username)
	if err != nil && !errors.Is(err, errSAxessUser) {
		return Step{}, request, err
	}
	if err != nil {
		if start {
			return saxessStartRejected(ctx, deps, request)
		}
		return failed(state, methods.FailureInvalidCredentials), request, nil
	}
	operator := string(subject.User.UID)
	// Browser binding stays server-side; the backend separately binds the User UID.
	transaction := secret.SHA256Hex(request.Name + "\x00" + request.Status.CookieHash)
	if start {
		ttl := min(saxessTTL, request.Spec.ExpiresAt.Sub(now))
		if ttl <= 0 {
			return failed(state, errorExpired), request, nil
		}
		challenge, err := cfg.Backend.CreateChallenge(ctx, operator, transaction, ttl)
		if errors.Is(err, saxess.ErrInvalidChallenge) {
			// A competing start may already own this browser transaction. Do not
			// replace its state with a permanent failure before its CAS commits.
			return saxessStartRejected(ctx, deps, request)
		}
		if err != nil {
			return Step{}, request, err
		}
		if challenge.Nonce == "" || strings.Contains(challenge.Nonce, "|") || challenge.ExpiresAt.IsZero() {
			return Step{}, request, errors.New("login: invalid sAxess challenge")
		}
		deadline := minTime(challenge.ExpiresAt, request.Spec.ExpiresAt.Time)
		if !deps.Clock().Before(deadline) {
			return failed(state, errorExpired), request, nil
		}
		// The mobile app gets the nonce from its backend, never from browser input.
		next := v1alpha1.LoginState{
			Step: ChallengeSAxess, Primary: ChallengeSAxess, Username: username, Provider: operator,
			Upstream: challenge.Nonce + "|" + deadline.UTC().Format(time.RFC3339Nano),
		}
		return ask(next, Facts{}), request, nil
	}
	if operator != state.Provider {
		return failed(state, methods.FailureInvalidCredentials), request, nil
	}
	approved, err := cfg.Backend.RedeemBrowserApproval(ctx, operator, transaction, nonce)
	if !deps.Clock().Before(deadline) {
		return failed(state, errorExpired), request, nil
	}
	if errors.Is(err, saxess.ErrInvalidChallenge) {
		// The backend does not distinguish a pending approval from denial.
		return ask(state, Facts{}), request, errSAxessPending
	}
	if err != nil {
		return Step{}, request, err
	}
	if approved != operator {
		return failed(state, methods.FailureInvalidCredentials), request, nil
	}
	current, err := saxessAdminSubject(ctx, deps, state.Username)
	if err != nil && !errors.Is(err, errSAxessUser) {
		return Step{}, request, err
	}
	if err != nil || string(current.User.UID) != operator {
		return failed(state, methods.FailureInvalidCredentials), request, nil
	}
	if now = deps.Clock(); !now.Before(deadline) {
		return failed(state, errorExpired), request, nil
	}
	result := methods.Result{Subject: &methods.Subject{User: current.User, AMR: []string{ChallengeSAxess}}}
	return afterResult(ctx, deps, request, ChallengeSAxess, result, now)
}

func saxessAdminSubject(ctx context.Context, deps Deps, username string) (methods.Subject, error) {
	if username == "" {
		return methods.Subject{}, errSAxessUser
	}
	subject, err := deps.Store.Subject(ctx, v1alpha1.UserObjectName(username))
	switch {
	case apierrors.IsNotFound(err), errors.Is(err, store.ErrUserDisabled):
		return methods.Subject{}, errSAxessUser
	case err != nil:
		return methods.Subject{}, err
	case subject.User.UID == "", subject.User.Spec.Username != username, !slices.Contains(subject.Groups, v1alpha1.GroupAdmins):
		return methods.Subject{}, errSAxessUser
	}
	return subject, nil
}

func claimSAxessAnswer(ctx context.Context, deps Deps, request v1alpha1.AuthRequest) (string, error) {
	_, hash, err := NewCSRF(deps.Random)
	if err != nil {
		return "", err
	}
	saved, err := deps.Store.SaveLogin(ctx, request.Name, withStatus(request.Status.Login.CSRFHash, request.Status, hash))
	if err != nil {
		return "", err
	}
	return hash, savedWith(saved, hash)
}

func saxessStartRejected(ctx context.Context, deps Deps, request v1alpha1.AuthRequest) (Step, v1alpha1.AuthRequest, error) {
	facts, err := loadFacts(ctx, deps, request, nil)
	if err != nil {
		return Step{}, request, err
	}
	step := Start(facts)
	step.Challenge.Error = challengeError(methods.FailureInvalidCredentials)
	return step, request, errSAxessPending
}

func saxessPending(state v1alpha1.LoginState) (string, time.Time) {
	nonce, raw, _ := strings.Cut(state.Upstream, "|")
	deadline, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", time.Time{}
	}
	return nonce, deadline
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func checkSAxessUser(state v1alpha1.LoginState, user v1alpha1.User) error {
	if state.Primary == ChallengeSAxess &&
		(user.UID == "" || string(user.UID) != state.Provider || user.Spec.Username != state.Username || user.Spec.Disabled) {
		return errSAxessUser
	}
	return nil
}
