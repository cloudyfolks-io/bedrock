package login

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/authn/store"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	cookieSecretLength = 43
	requestIDLength    = 26
	requestIDAlphabet  = "abcdefghijklmnopqrstuvwxyz0123456789"
	verifierLength     = 64
	nonceLength        = 32
	upstreamTTL        = 10 * time.Minute
)

var (
	errUnexpectedAnswer = errors.New("login: unexpected answer")
	errLoginDone        = errors.New("login: the auth request is done")
	errLoginChanged     = errors.New("login: the login state changed meanwhile")
)

type Deps struct {
	Store    *store.Store
	Client   client.Client
	Methods  methods.Registry
	Settings func(context.Context) (policy.Settings, error)
	Random   io.Reader
	Clock    func() time.Time
	Limiter  *methods.RateLimiter
	Callback func(ctx context.Context, id string) string
	LDAPDial methods.LDAPDialer
}

type decoration struct {
	challenge methods.Challenge
	status    v1alpha1.AuthRequestStatus
	cookies   []func(http.ResponseWriter)
}

type startBody struct {
	AuthRequest string `json:"authRequest"`
}

type deviceBody struct {
	UserCode string `json:"userCode"`
}

func Handler(deps Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/login/start", func(w http.ResponseWriter, r *http.Request) { start(w, r, deps) })
	mux.HandleFunc("POST /api/v1/login/device", func(w http.ResponseWriter, r *http.Request) { startDevice(w, r, deps) })
	mux.HandleFunc("GET /api/v1/login/challenge", func(w http.ResponseWriter, r *http.Request) { current(w, r, deps) })
	mux.HandleFunc("POST /api/v1/login/answer", func(w http.ResponseWriter, r *http.Request) { answer(w, r, deps) })
	mux.HandleFunc("GET /api/v1/login/providers/{name}/callback", func(w http.ResponseWriter, r *http.Request) { callback(w, r, deps) })
	return mux
}

func start(w http.ResponseWriter, r *http.Request, deps Deps) {
	body, err := httpjson.Decode[startBody](w, r)
	if err != nil {
		httpjson.WriteDecodeError(w, err)
		return
	}
	request, err := loadRequest(r.Context(), deps, body.AuthRequest)
	if err != nil || request.Status.Done || request.Spec.DeviceRequest != "" {
		httpjson.Write(w, http.StatusOK, expired())
		return
	}
	if request.Status.CookieHash != "" {
		restart(w, r, deps, request)
		return
	}
	value, err := newLoginCookie(deps.Random, request.Name)
	if err != nil {
		internal(w, r, err)
		return
	}
	bound, err := deps.Store.SaveLogin(r.Context(), request.Name, bindCookie(secret.SHA256Hex(value)))
	if err != nil {
		internal(w, r, err)
		return
	}
	if !isBound(bound, value) {
		httpjson.WriteError(w, http.StatusConflict, "already_started")
		return
	}
	SetCookie(w, CookieLogin, value, request.Spec.ExpiresAt.Sub(deps.Clock()))
	facts, err := loadFacts(r.Context(), deps, bound, nil)
	if err != nil {
		internal(w, r, err)
		return
	}
	respond(w, r, deps, bound, Start(facts))
}

func restart(w http.ResponseWriter, r *http.Request, deps Deps, request v1alpha1.AuthRequest) {
	_, value, ok := loginCookie(r)
	if !ok || !isBound(request, value) {
		httpjson.WriteError(w, http.StatusConflict, "already_started")
		return
	}
	resumeLogin(w, r, deps, request)
}

func startDevice(w http.ResponseWriter, r *http.Request, deps Deps) {
	body, err := httpjson.Decode[deviceBody](w, r)
	if err != nil {
		httpjson.WriteDecodeError(w, err)
		return
	}
	ctx := r.Context()
	now := deps.Clock()
	if !deps.Limiter.Allow(ClientIP(r), now) {
		httpjson.WriteError(w, http.StatusTooManyRequests, methods.FailureRateLimited)
		return
	}
	device, err := deps.Store.DeviceRequestByUserCode(ctx, body.UserCode)
	if err != nil || settled(device) {
		httpjson.WriteError(w, http.StatusNotFound, methods.FailureInvalidCode)
		return
	}
	id, err := secret.FromAlphabet(deps.Random, requestIDAlphabet, requestIDLength)
	if err != nil {
		internal(w, r, err)
		return
	}
	value, err := newLoginCookie(deps.Random, id)
	if err != nil {
		internal(w, r, err)
		return
	}
	request := deviceAuthRequest(id, device, store.NormalizeUserCode(body.UserCode))
	if err := deps.Client.Create(ctx, &request, client.FieldOwner(v1alpha1.AuthnFieldManager)); err != nil {
		internal(w, r, err)
		return
	}
	bound, err := deps.Store.SaveLogin(ctx, id, bindCookie(secret.SHA256Hex(value)))
	if err != nil {
		internal(w, r, err)
		return
	}
	SetCookie(w, CookieLogin, value, device.Spec.ExpiresAt.Sub(now))
	facts, err := loadFacts(ctx, deps, bound, nil)
	if err != nil {
		internal(w, r, err)
		return
	}
	respond(w, r, deps, bound, Start(facts))
}

func current(w http.ResponseWriter, r *http.Request, deps Deps) {
	request, ok := boundRequest(w, r, deps)
	if !ok {
		return
	}
	resumeLogin(w, r, deps, request)
}

func resumeLogin(w http.ResponseWriter, r *http.Request, deps Deps, request v1alpha1.AuthRequest) {
	facts, err := subjectFacts(r.Context(), deps, request)
	if err != nil {
		internal(w, r, err)
		return
	}
	respond(w, r, deps, request, resume(request.Status.Login, facts))
}

func answer(w http.ResponseWriter, r *http.Request, deps Deps) {
	request, ok := boundRequest(w, r, deps)
	if !ok {
		return
	}
	if !CheckCSRF(request.Status.Login.CSRFHash, r.Header.Get("X-CSRF-Token")) {
		httpjson.WriteError(w, http.StatusForbidden, "csrf")
		return
	}
	given, err := httpjson.Decode[methods.Answer](w, r)
	if err != nil {
		httpjson.WriteDecodeError(w, err)
		return
	}
	step, updated, err := dispatch(r, deps, request, given)
	switch {
	case errors.Is(err, errUnexpectedAnswer):
		httpjson.WriteError(w, http.StatusBadRequest, "unexpected_answer")
	case err != nil:
		internal(w, r, err)
	default:
		respond(w, r, deps, updated, step)
	}
}

func callback(w http.ResponseWriter, r *http.Request, deps Deps) {
	request, ok := boundRequest(w, r, deps)
	if !ok {
		return
	}
	upstream, err := r.Cookie(CookieUpstream)
	if err != nil || !upstreamMatches(request, upstream.Value, r.PathValue("name"), r.URL.Query().Get("state")) {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_state")
		return
	}
	ClearCookie(w, CookieUpstream)
	step, updated, err := upstreamAnswer(r, deps, request, upstream.Value)
	if err != nil {
		internal(w, r, err)
		return
	}
	challenge, err := commit(w, r, deps, updated, step)
	if err != nil {
		writeCommitError(w, r, err)
		return
	}
	http.Redirect(w, r, afterUpstream(challenge, request.Name), http.StatusSeeOther)
}

func upstreamAnswer(r *http.Request, deps Deps, request v1alpha1.AuthRequest, upstream string) (Step, v1alpha1.AuthRequest, error) {
	query := r.URL.Query()
	flow := flowOf(r, deps, withUpstream(request, upstream))
	if query.Has("error") || query.Get("code") == "" {
		return afterResult(r.Context(), deps, request, v1alpha1.MethodOIDC, methods.Result{Failure: methods.FailureProviderError}, flow.Now)
	}
	given := methods.Answer{Type: v1alpha1.MethodOIDC, Provider: r.PathValue("name"), Code: query.Get("code")}
	return runMethod(r.Context(), deps, request, flow, v1alpha1.MethodOIDC, given)
}

func dispatch(r *http.Request, deps Deps, request v1alpha1.AuthRequest, given methods.Answer) (Step, v1alpha1.AuthRequest, error) {
	ctx := r.Context()
	state := request.Status.Login
	flow := flowOf(r, deps, request)
	switch {
	case state.Step == methods.ChallengeUsername && given.Type == methods.ChallengeUsername:
		username := usernameOf(given.Username)
		facts, err := usernameFacts(ctx, deps, request, username)
		return AfterUsername(state, username, facts), request, err
	case (state.Step == methods.ChallengeUsername || state.Step == methods.ChallengeProviders) && given.Type == answerProvider:
		facts, err := loadFacts(ctx, deps, request, nil)
		return afterProvider(state, given.Provider, facts), request, err
	case state.Step == methods.ChallengePassword && given.Type == methods.ChallengePassword:
		return runMethod(ctx, deps, request, flow, state.Primary, given)
	case state.Step == methods.ChallengeTOTP && wantsRecovery(given) && given.Code == "":
		return toRecovery(state), request, nil
	case state.Step == methods.ChallengeTOTP && wantsRecovery(given):
		return runMethod(ctx, deps, request, flow, v1alpha1.MethodRecovery, given)
	case state.Step == methods.ChallengeTOTP && given.Type == methods.ChallengeTOTP:
		return runMethod(ctx, deps, request, flow, v1alpha1.MethodTOTP, given)
	case state.Step == methods.ChallengeRecovery && given.Type == methods.ChallengeRecovery:
		return runMethod(ctx, deps, request, flow, v1alpha1.MethodRecovery, given)
	case state.Step == methods.ChallengeTOTPEnroll && given.Type == methods.ChallengeTOTPEnroll:
		return runMethod(ctx, deps, request, flow, v1alpha1.MethodTOTP, given)
	case state.Step == stepRecoveryCodes && given.Type == methods.ChallengeTOTPEnroll:
		facts, err := subjectFacts(ctx, deps, request)
		return afterRecoveryCodes(state, facts), request, err
	case isDeviceAnswer(state, given) && *given.Approve:
		return AfterDeviceApprove(state), request, nil
	case isDeviceAnswer(state, given):
		return AfterDeviceDeny(state), request, deps.Store.DenyDevice(ctx, request.Spec.DeviceRequest)
	}
	return Step{}, request, errUnexpectedAnswer
}

func runMethod(ctx context.Context, deps Deps, request v1alpha1.AuthRequest, flow methods.Flow, name string, given methods.Answer) (Step, v1alpha1.AuthRequest, error) {
	method, err := registered(deps, name)
	if err != nil {
		return Step{}, request, err
	}
	if !deps.Limiter.Allow(flow.ClientIP, flow.Now) {
		return afterResult(ctx, deps, request, name, methods.Result{Failure: methods.FailureRateLimited}, flow.Now)
	}
	user, err := methodUser(ctx, deps, request)
	if err != nil {
		return Step{}, request, err
	}
	result, err := method.Complete(ctx, flow, user, withUsername(given, request.Status.Login.Username))
	if err != nil {
		return Step{}, request, err
	}
	return afterResult(ctx, deps, request, name, result, flow.Now)
}

func afterResult(ctx context.Context, deps Deps, request v1alpha1.AuthRequest, name string, result methods.Result, now time.Time) (Step, v1alpha1.AuthRequest, error) {
	if result.Subject == nil {
		facts, err := subjectFacts(ctx, deps, request)
		return AfterMethod(request.Status.Login, name, result, facts), request, err
	}
	updated := withSubject(request, *result.Subject, now)
	facts, err := loadFacts(ctx, deps, updated, &result.Subject.User)
	return AfterMethod(updated.Status.Login, name, result, facts), updated, err
}

func respond(w http.ResponseWriter, r *http.Request, deps Deps, request v1alpha1.AuthRequest, step Step) {
	challenge, err := commit(w, r, deps, request, step)
	if err != nil {
		writeCommitError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, challenge)
}

func commit(w http.ResponseWriter, r *http.Request, deps Deps, request v1alpha1.AuthRequest, step Step) (methods.Challenge, error) {
	next, err := decorate(r, deps, request, step)
	if err != nil {
		return methods.Challenge{}, err
	}
	csrf, csrfHash, err := NewCSRF(deps.Random)
	if err != nil {
		return methods.Challenge{}, err
	}
	saved, err := deps.Store.SaveLogin(r.Context(), request.Name, withStatus(request.Status.Login.CSRFHash, next.status, csrfHash))
	if err != nil {
		return methods.Challenge{}, err
	}
	if err := savedWith(saved, csrfHash); err != nil {
		return methods.Challenge{}, err
	}
	for _, write := range next.cookies {
		write(w)
	}
	return withCSRF(next.challenge, csrf), nil
}

func savedWith(saved v1alpha1.AuthRequest, csrfHash string) error {
	switch {
	case saved.Status.Login.CSRFHash == csrfHash:
		return nil
	case saved.Status.Done:
		return errLoginDone
	}
	return errLoginChanged
}

func decorate(r *http.Request, deps Deps, request v1alpha1.AuthRequest, step Step) (decoration, error) {
	status := *request.Status.DeepCopy()
	status.Login = step.State
	switch {
	case step.Complete && request.Spec.DeviceRequest != "":
		return approveDevice(r.Context(), deps, request, step, status)
	case step.Complete:
		return finish(r, deps, request, step, status)
	case step.Begin != "":
		return beginUpstream(r, deps, request, step, status)
	case step.State.Step == methods.ChallengeTOTPEnroll && step.Challenge.Error == nil:
		return enrollTOTP(r.Context(), deps, request, step, status)
	case step.State.Step == stepRecoveryCodes:
		return recoveryCodes(r.Context(), deps, request, step, status)
	case step.Challenge.Type == methods.ChallengeDeviceConfirm:
		return decoration{challenge: withUserCode(step.Challenge, request.Spec.State), status: status}, nil
	}
	return decoration{challenge: step.Challenge, status: status}, nil
}

func finish(r *http.Request, deps Deps, request v1alpha1.AuthRequest, step Step, status v1alpha1.AuthRequestStatus) (decoration, error) {
	ctx := r.Context()
	subject, err := sessionSubject(ctx, deps, request)
	if err != nil {
		return decoration{}, err
	}
	settings, err := deps.Settings(ctx)
	if err != nil {
		return decoration{}, err
	}
	cookie, err := deps.Store.CreateSession(ctx, subject, r.UserAgent(), ClientIP(r))
	if err != nil {
		return decoration{}, err
	}
	done := status
	done.Done = true
	done.Session = secret.SHA256Hex(cookie)
	challenge := step.Challenge
	challenge.Redirect = deps.Callback(ctx, request.Name)
	return decoration{
		challenge: challenge,
		status:    done,
		cookies: []func(http.ResponseWriter){
			func(w http.ResponseWriter) { SetCookie(w, CookieSession, cookie, settings.SessionTTL) },
			func(w http.ResponseWriter) { ClearCookie(w, CookieLogin) },
		},
	}, nil
}

func approveDevice(ctx context.Context, deps Deps, request v1alpha1.AuthRequest, step Step, status v1alpha1.AuthRequestStatus) (decoration, error) {
	subject, err := sessionSubject(ctx, deps, request)
	if err != nil {
		return decoration{}, err
	}
	if err := deps.Store.ApproveDevice(ctx, request.Spec.DeviceRequest, subject); err != nil {
		return decoration{}, err
	}
	done := status
	done.Done = true
	return decoration{
		challenge: step.Challenge,
		status:    done,
		cookies:   []func(http.ResponseWriter){func(w http.ResponseWriter) { ClearCookie(w, CookieLogin) }},
	}, nil
}

func beginUpstream(r *http.Request, deps Deps, request v1alpha1.AuthRequest, step Step, status v1alpha1.AuthRequestStatus) (decoration, error) {
	method, err := registered(deps, step.Begin)
	if err != nil {
		return decoration{}, err
	}
	cookie, err := newUpstreamCookie(deps.Random, request.Name)
	if err != nil {
		return decoration{}, err
	}
	encoded := methods.EncodeUpstream(cookie)
	flowRequest := request.DeepCopy()
	flowRequest.Status.Login = step.State
	challenge, err := method.Begin(r.Context(), flowOf(r, deps, withUpstream(*flowRequest, encoded)), v1alpha1.User{})
	if err != nil {
		return decoration{}, err
	}
	begun := status
	begun.Login.Upstream = secret.SHA256Hex(encoded)
	return decoration{
		challenge: challenge,
		status:    begun,
		cookies:   []func(http.ResponseWriter){func(w http.ResponseWriter) { SetCookie(w, CookieUpstream, encoded, upstreamTTL) }},
	}, nil
}

func enrollTOTP(ctx context.Context, deps Deps, request v1alpha1.AuthRequest, step Step, status v1alpha1.AuthRequestStatus) (decoration, error) {
	method, err := registered(deps, v1alpha1.MethodTOTP)
	if err != nil {
		return decoration{}, err
	}
	user, err := deps.Store.User(ctx, request.Status.Subject)
	if err != nil {
		return decoration{}, err
	}
	replaceable, err := dropPendingTOTP(ctx, deps.Client, user.Name)
	if err != nil {
		return decoration{}, err
	}
	if !replaceable {
		return decoration{challenge: step.Challenge, status: status}, nil
	}
	enrollment, err := method.Enroll(ctx, user, methods.Answer{})
	if err != nil {
		return decoration{}, err
	}
	challenge := step.Challenge
	challenge.Enroll = enrollment.TOTP
	return decoration{challenge: challenge, status: status}, nil
}

func recoveryCodes(ctx context.Context, deps Deps, request v1alpha1.AuthRequest, step Step, status v1alpha1.AuthRequestStatus) (decoration, error) {
	method, err := registered(deps, v1alpha1.MethodRecovery)
	if err != nil {
		return decoration{}, err
	}
	user, err := deps.Store.User(ctx, request.Status.Subject)
	if err != nil {
		return decoration{}, err
	}
	enrollment, err := method.Enroll(ctx, user, methods.Answer{})
	if err != nil {
		return decoration{}, err
	}
	challenge := step.Challenge
	challenge.RecoveryCodes = enrollment.RecoveryCodes
	return decoration{challenge: challenge, status: status}, nil
}

func dropPendingTOTP(ctx context.Context, c client.Client, user string) (bool, error) {
	var credential v1alpha1.Credential
	err := c.Get(ctx, objectKey(v1alpha1.CredentialName(user, v1alpha1.MethodTOTP)), &credential)
	switch {
	case apierrors.IsNotFound(err):
		return true, nil
	case err != nil:
		return false, err
	case credential.Status.EnrolledAt != nil:
		return false, nil
	}
	var pending corev1.Secret
	if err := client.IgnoreNotFound(c.Get(ctx, objectKey(credential.Spec.SecretRef), &pending)); err != nil {
		return false, err
	}
	err = c.Delete(ctx, &credential, client.Preconditions{UID: &credential.UID, ResourceVersion: &credential.ResourceVersion})
	switch {
	case apierrors.IsConflict(err), apierrors.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, err
	case pending.UID == "":
		return true, nil
	}
	err = c.Delete(ctx, &pending, client.Preconditions{UID: &pending.UID})
	if err != nil && !apierrors.IsConflict(err) && !apierrors.IsNotFound(err) {
		return false, err
	}
	return true, nil
}

func loadFacts(ctx context.Context, deps Deps, request v1alpha1.AuthRequest, user *v1alpha1.User) (Facts, error) {
	settings, err := deps.Settings(ctx)
	if err != nil {
		return Facts{}, err
	}
	groups, err := deps.Store.Groups(ctx)
	if err != nil {
		return Facts{}, err
	}
	var oauth v1alpha1.OAuthClient
	if err := deps.Client.Get(ctx, objectKey(request.Spec.ClientID), &oauth); err != nil {
		return Facts{}, err
	}
	providers, err := enabledProviders(ctx, deps.Client)
	if err != nil {
		return Facts{}, err
	}
	enrolled, err := enrolledMethods(ctx, deps.Client, user)
	if err != nil {
		return Facts{}, err
	}
	device, err := deviceRequest(ctx, deps.Client, request.Spec.DeviceRequest)
	if err != nil {
		return Facts{}, err
	}
	return Facts{User: user, Groups: groups, Client: oauth, Settings: settings, Enrolled: enrolled, Providers: providers, Device: device}, nil
}

func subjectFacts(ctx context.Context, deps Deps, request v1alpha1.AuthRequest) (Facts, error) {
	if request.Status.Subject == "" {
		return loadFacts(ctx, deps, request, nil)
	}
	user, err := deps.Store.User(ctx, request.Status.Subject)
	if err != nil {
		return Facts{}, err
	}
	return loadFacts(ctx, deps, request, &user)
}

func usernameFacts(ctx context.Context, deps Deps, request v1alpha1.AuthRequest, username string) (Facts, error) {
	user, err := userByUsername(ctx, deps.Client, username)
	if err != nil {
		return Facts{}, err
	}
	if user.Name != "" {
		return loadFacts(ctx, deps, request, &user)
	}
	facts, err := loadFacts(ctx, deps, request, nil)
	if err != nil || !slices.ContainsFunc(facts.Providers, isLDAP) {
		return facts, err
	}
	provider, found, err := methods.LDAPProviderFor(ctx, deps.Client, deps.LDAPDial, username)
	if err != nil || !found {
		return facts, err
	}
	return withLDAPProvider(facts, provider), nil
}

func methodUser(ctx context.Context, deps Deps, request v1alpha1.AuthRequest) (v1alpha1.User, error) {
	if request.Status.Subject != "" {
		return deps.Store.User(ctx, request.Status.Subject)
	}
	return userByUsername(ctx, deps.Client, request.Status.Login.Username)
}

func userByUsername(ctx context.Context, c client.Client, username string) (v1alpha1.User, error) {
	if username == "" {
		return v1alpha1.User{}, nil
	}
	var user v1alpha1.User
	err := c.Get(ctx, objectKey(v1alpha1.UserObjectName(username)), &user)
	switch {
	case apierrors.IsNotFound(err):
		return v1alpha1.User{}, nil
	case err != nil:
		return v1alpha1.User{}, err
	case user.Spec.Username != username:
		return v1alpha1.User{}, nil
	}
	return user, nil
}

func sessionSubject(ctx context.Context, deps Deps, request v1alpha1.AuthRequest) (methods.Subject, error) {
	subject, err := deps.Store.Subject(ctx, request.Status.Subject)
	if err != nil {
		return methods.Subject{}, err
	}
	return methods.Subject{User: subject.User, Groups: subject.Groups, AMR: slices.Clone(request.Status.AMR)}, nil
}

func enabledProviders(ctx context.Context, c client.Client) ([]v1alpha1.IdentityProvider, error) {
	var list v1alpha1.IdentityProviderList
	if err := c.List(ctx, &list, client.InNamespace(release.SystemNamespace)); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(list.Items, func(provider v1alpha1.IdentityProvider) bool { return provider.Spec.Disabled }), nil
}

func enrolledMethods(ctx context.Context, c client.Client, user *v1alpha1.User) ([]string, error) {
	if user == nil {
		return nil, nil
	}
	var list v1alpha1.CredentialList
	if err := c.List(ctx, &list, client.InNamespace(release.SystemNamespace), client.MatchingFields{"spec.userRef": user.Name}); err != nil {
		return nil, err
	}
	var enrolled []string
	for _, credential := range list.Items {
		if credential.Status.EnrolledAt != nil {
			enrolled = append(enrolled, credential.Spec.Method)
		}
	}
	slices.Sort(enrolled)
	return enrolled, nil
}

func deviceRequest(ctx context.Context, c client.Client, name string) (*v1alpha1.DeviceRequest, error) {
	if name == "" {
		return nil, nil
	}
	var device v1alpha1.DeviceRequest
	err := c.Get(ctx, objectKey(name), &device)
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return &device, nil
}

func loadRequest(ctx context.Context, deps Deps, id string) (v1alpha1.AuthRequest, error) {
	found, err := deps.Store.AuthRequestByID(ctx, id)
	if err != nil {
		return v1alpha1.AuthRequest{}, err
	}
	request, ok := found.(store.AuthRequest)
	if !ok {
		return v1alpha1.AuthRequest{}, fmt.Errorf("login: unexpected auth request type %T", found)
	}
	return request.Object, nil
}

func boundRequest(w http.ResponseWriter, r *http.Request, deps Deps) (v1alpha1.AuthRequest, bool) {
	id, value, ok := loginCookie(r)
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "no_login")
		return v1alpha1.AuthRequest{}, false
	}
	request, err := loadRequest(r.Context(), deps, id)
	if err != nil {
		httpjson.Write(w, http.StatusOK, expired())
		return v1alpha1.AuthRequest{}, false
	}
	if !isBound(request, value) {
		httpjson.WriteError(w, http.StatusUnauthorized, "no_login")
		return v1alpha1.AuthRequest{}, false
	}
	if request.Status.Done {
		httpjson.Write(w, http.StatusOK, expired())
		return v1alpha1.AuthRequest{}, false
	}
	return request, true
}

func loginCookie(r *http.Request) (string, string, bool) {
	cookie, err := r.Cookie(CookieLogin)
	if err != nil {
		return "", "", false
	}
	id, rest, found := strings.Cut(cookie.Value, ".")
	return id, cookie.Value, found && id != "" && rest != ""
}

func isBound(request v1alpha1.AuthRequest, value string) bool {
	return request.Status.CookieHash != "" && secret.Equal(request.Status.CookieHash, secret.SHA256Hex(value))
}

func bindCookie(hash string) func(v1alpha1.AuthRequest) v1alpha1.AuthRequest {
	return func(current v1alpha1.AuthRequest) v1alpha1.AuthRequest {
		if current.Status.CookieHash != "" || current.Status.Done {
			return current
		}
		next := current.DeepCopy()
		next.Status.CookieHash = hash
		return *next
	}
}

func withStatus(expected string, status v1alpha1.AuthRequestStatus, csrfHash string) func(v1alpha1.AuthRequest) v1alpha1.AuthRequest {
	return func(current v1alpha1.AuthRequest) v1alpha1.AuthRequest {
		if current.Status.Done || current.Status.Login.CSRFHash != expected {
			return current
		}
		next := current.DeepCopy()
		next.Status = *status.DeepCopy()
		next.Status.Login.CSRFHash = csrfHash
		return *next
	}
}

func withSubject(request v1alpha1.AuthRequest, subject methods.Subject, now time.Time) v1alpha1.AuthRequest {
	next := request.DeepCopy()
	next.Status.Subject = subject.User.Name
	next.Status.AMR = union(request.Status.AMR, subject.AMR)
	if next.Status.AuthTime == nil {
		next.Status.AuthTime = &metav1.Time{Time: now}
	}
	return *next
}

func withUpstream(request v1alpha1.AuthRequest, encoded string) v1alpha1.AuthRequest {
	next := request.DeepCopy()
	next.Status.Login.Upstream = encoded
	return *next
}

func upstreamMatches(request v1alpha1.AuthRequest, raw, provider, state string) bool {
	cookie, err := methods.DecodeUpstream(raw)
	login := request.Status.Login
	return err == nil &&
		login.Step == methods.ChallengeRedirect &&
		login.Provider == provider &&
		login.Upstream != "" &&
		secret.Equal(login.Upstream, secret.SHA256Hex(raw)) &&
		cookie.State == request.Name &&
		secret.Equal(cookie.State, state)
}

func afterUpstream(challenge methods.Challenge, id string) string {
	if challenge.Type == methods.ChallengeDone && challenge.Redirect != "" {
		return challenge.Redirect
	}
	return "/login/?authRequest=" + url.QueryEscape(id)
}

func deviceAuthRequest(id string, device v1alpha1.DeviceRequest, userCode string) v1alpha1.AuthRequest {
	return v1alpha1.AuthRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      id,
			Namespace: release.SystemNamespace,
			Labels:    map[string]string{v1alpha1.LabelKind: "AuthRequest", v1alpha1.LabelName: id},
		},
		Spec: v1alpha1.AuthRequestSpec{
			ClientID:      device.Spec.ClientID,
			Scopes:        slices.Clone(device.Spec.Scopes),
			State:         formatUserCode(userCode),
			DeviceRequest: device.Name,
			ExpiresAt:     device.Spec.ExpiresAt,
		},
	}
}

func formatUserCode(normalized string) string {
	if len(normalized) != 8 {
		return normalized
	}
	return normalized[:4] + "-" + normalized[4:]
}

func settled(device v1alpha1.DeviceRequest) bool {
	return device.Status.State == v1alpha1.DeviceStateApproved || device.Status.State == v1alpha1.DeviceStateDenied
}

func isDeviceAnswer(state v1alpha1.LoginState, given methods.Answer) bool {
	return state.Step == methods.ChallengeDeviceConfirm && given.Type == methods.ChallengeDeviceConfirm && given.Approve != nil
}

func wantsRecovery(given methods.Answer) bool {
	return given.Type == methods.ChallengeRecovery || given.Method == v1alpha1.MethodRecovery
}

func isLDAP(provider v1alpha1.IdentityProvider) bool {
	return provider.Spec.Type == v1alpha1.MethodLDAP
}

func withLDAPProvider(facts Facts, provider string) Facts {
	next := facts
	next.LDAPProviders = []string{provider}
	return next
}

func withUsername(given methods.Answer, username string) methods.Answer {
	next := given
	next.Username = username
	return next
}

func withCSRF(challenge methods.Challenge, csrf string) methods.Challenge {
	next := challenge
	next.CSRF = csrf
	return next
}

func withUserCode(challenge methods.Challenge, userCode string) methods.Challenge {
	if challenge.Device == nil {
		return challenge
	}
	device := *challenge.Device
	device.UserCode = userCode
	next := challenge
	next.Device = &device
	return next
}

func usernameOf(raw string) string {
	normalized, err := v1alpha1.NormalizeUsername(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return normalized
}

func union(current, added []string) []string {
	merged := append(slices.Clone(current), added...)
	slices.Sort(merged)
	return slices.Compact(merged)
}

func newLoginCookie(random io.Reader, id string) (string, error) {
	value, err := secret.Base62(random, cookieSecretLength)
	if err != nil {
		return "", err
	}
	return id + "." + value, nil
}

func newUpstreamCookie(random io.Reader, id string) (methods.UpstreamCookie, error) {
	verifier, err := secret.Base62(random, verifierLength)
	if err != nil {
		return methods.UpstreamCookie{}, err
	}
	nonce, err := secret.Base62(random, nonceLength)
	if err != nil {
		return methods.UpstreamCookie{}, err
	}
	return methods.UpstreamCookie{Verifier: verifier, Nonce: nonce, State: id}, nil
}

func flowOf(r *http.Request, deps Deps, request v1alpha1.AuthRequest) methods.Flow {
	return methods.Flow{AuthRequest: request, ClientIP: ClientIP(r), Now: deps.Clock()}
}

func registered(deps Deps, name string) (methods.Method, error) {
	method, ok := deps.Methods[name]
	if !ok {
		return nil, fmt.Errorf("login: method %q is not registered", name)
	}
	return method, nil
}

func objectKey(name string) client.ObjectKey {
	return client.ObjectKey{Namespace: release.SystemNamespace, Name: name}
}

func expired() methods.Challenge {
	return failed(v1alpha1.LoginState{}, errorExpired).Challenge
}

func writeCommitError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errLoginDone):
		httpjson.Write(w, http.StatusOK, expired())
	case errors.Is(err, errLoginChanged):
		httpjson.WriteError(w, http.StatusForbidden, "csrf")
	default:
		internal(w, r, err)
	}
}

func internal(w http.ResponseWriter, r *http.Request, err error) {
	httpjson.Internal(w, r, "login request failed", err)
}
