package account

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
	"github.com/cloudyfolks-io/bedrock/internal/authn/login"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/authn/store"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	csrfContext       = "bedrock-account-csrf:"
	minPasswordLength = 12
)

var errNoTOTPEnrollment = errors.New("account: the TOTP method enrolled nothing")

type Deps struct {
	Store    *store.Store
	Client   client.Client
	Methods  methods.Registry
	Settings func(context.Context) (policy.Settings, error)
	Random   io.Reader
	Clock    func() time.Time
	Limiter  *methods.RateLimiter
}

type caller struct {
	session v1alpha1.Session
	cookie  string
	user    v1alpha1.User
}

type action func(w http.ResponseWriter, r *http.Request, deps Deps, who caller)

type userView struct {
	Name        string   `json:"name"`
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	Email       string   `json:"email"`
	Source      string   `json:"source"`
	Groups      []string `json:"groups"`
}

type methodView struct {
	Method     string       `json:"method"`
	EnrolledAt *metav1.Time `json:"enrolledAt"`
	LastUsed   *metav1.Time `json:"lastUsed"`
}

type accountView struct {
	User    userView     `json:"user"`
	Methods []methodView `json:"methods"`
	CSRF    string       `json:"csrf"`
}

type sessionView struct {
	ID        string       `json:"id"`
	Current   bool         `json:"current"`
	AuthTime  metav1.Time  `json:"authTime"`
	LastSeen  *metav1.Time `json:"lastSeen"`
	UserAgent string       `json:"userAgent"`
	ClientIP  string       `json:"clientIP"`
}

type passwordBody struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

type codeBody struct {
	Code string `json:"code"`
}

func Handler(deps Deps) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/account", withSession(deps, show))
	mux.Handle("POST /api/v1/account/password", withCSRF(deps, changePassword))
	mux.Handle("POST /api/v1/account/totp", withCSRF(deps, beginTOTP))
	mux.Handle("POST /api/v1/account/totp/verify", withCSRF(deps, verifyTOTP))
	mux.Handle("DELETE /api/v1/account/totp", withCSRF(deps, removeTOTP))
	mux.Handle("POST /api/v1/account/recovery-codes", withCSRF(deps, newRecoveryCodes))
	mux.Handle("GET /api/v1/account/tokens", withSession(deps, listTokens))
	mux.Handle("POST /api/v1/account/tokens", withCSRF(deps, createToken))
	mux.Handle("DELETE /api/v1/account/tokens/{id}", withCSRF(deps, revokeToken))
	mux.Handle("GET /api/v1/account/sessions", withSession(deps, listSessions))
	mux.Handle("DELETE /api/v1/account/sessions/{id}", withCSRF(deps, revokeSession))
	mux.Handle("POST /api/v1/account/logout", withCSRF(deps, logout))
	return mux
}

func withSession(deps Deps, next action) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, ok := authenticate(w, r, deps)
		if !ok {
			return
		}
		next(w, r, deps, who)
	})
}

func withCSRF(deps Deps, next action) http.Handler {
	return withSession(deps, func(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
		if !login.CheckCSRF(secret.SHA256Hex(csrfFor(who.cookie)), r.Header.Get("X-CSRF-Token")) {
			httpjson.WriteError(w, http.StatusForbidden, "csrf")
			return
		}
		next(w, r, deps, who)
	})
}

func authenticate(w http.ResponseWriter, r *http.Request, deps Deps) (caller, bool) {
	cookie, err := r.Cookie(login.CookieSession)
	if err != nil || cookie.Value == "" {
		denySession(w)
		return caller{}, false
	}
	session, err := deps.Store.SessionByCookie(r.Context(), cookie.Value)
	if err != nil || !deps.Clock().Before(session.Spec.ExpiresAt.Time) {
		denySession(w)
		return caller{}, false
	}
	user, err := deps.Store.User(r.Context(), session.Spec.UserRef)
	if err != nil || user.Spec.Disabled {
		denySession(w)
		return caller{}, false
	}
	return caller{session: session, cookie: cookie.Value, user: user}, true
}

func denySession(w http.ResponseWriter) {
	login.ClearCookie(w, login.CookieSession)
	httpjson.WriteError(w, http.StatusUnauthorized, "no_session")
}

func show(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	subject, err := deps.Store.Subject(r.Context(), who.user.Name)
	if err != nil {
		internal(w, r, err)
		return
	}
	var credentials v1alpha1.CredentialList
	if err := deps.Client.List(r.Context(), &credentials, client.InNamespace(release.SystemNamespace), client.MatchingFields{"spec.userRef": who.user.Name}); err != nil {
		internal(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, accountView{User: viewUser(who.user, subject.Groups), Methods: viewMethods(credentials.Items), CSRF: csrfFor(who.cookie)})
}

func changePassword(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	if who.user.Spec.Source != "" {
		httpjson.WriteError(w, http.StatusConflict, "not_local")
		return
	}
	body, err := httpjson.Decode[passwordBody](w, r)
	if err != nil {
		httpjson.WriteDecodeError(w, err)
		return
	}
	if utf8.RuneCountInString(body.New) < minPasswordLength {
		httpjson.WriteError(w, http.StatusBadRequest, "weak_password")
		return
	}
	result, err := complete(r, deps, who, v1alpha1.MethodPassword, methods.Answer{Type: methods.ChallengePassword, Username: who.user.Spec.Username, Password: body.Current})
	if err != nil {
		internal(w, r, err)
		return
	}
	if result.Subject == nil {
		refuse(w, failureOf(result))
		return
	}
	if err := methods.SetPassword(r.Context(), deps.Client, deps.Random, who.user, body.New); err != nil {
		internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func beginTOTP(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	enrolled, err := hasEnrolled(r.Context(), deps.Client, who.user.Name, v1alpha1.MethodTOTP)
	if err != nil {
		internal(w, r, err)
		return
	}
	if enrolled {
		httpjson.WriteError(w, http.StatusConflict, "already_enrolled")
		return
	}
	if err := deleteCredential(r.Context(), deps.Client, who.user.Name, v1alpha1.MethodTOTP); err != nil {
		internal(w, r, err)
		return
	}
	enrollment, err := enroll(r.Context(), deps, who, v1alpha1.MethodTOTP)
	if err != nil {
		internal(w, r, err)
		return
	}
	if enrollment.TOTP == nil {
		internal(w, r, errNoTOTPEnrollment)
		return
	}
	httpjson.Write(w, http.StatusOK, enrollment.TOTP)
}

func verifyTOTP(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	body, err := httpjson.Decode[codeBody](w, r)
	if err != nil {
		httpjson.WriteDecodeError(w, err)
		return
	}
	result, err := complete(r, deps, who, v1alpha1.MethodTOTP, methods.Answer{Type: methods.ChallengeTOTP, Code: body.Code})
	if err != nil {
		internal(w, r, err)
		return
	}
	if result.Subject == nil {
		refuse(w, failureOf(result))
		return
	}
	answerRecoveryCodes(w, r, deps, who)
}

func removeTOTP(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	settings, err := deps.Settings(r.Context())
	if err != nil {
		internal(w, r, err)
		return
	}
	groups, err := deps.Store.Groups(r.Context())
	if err != nil {
		internal(w, r, err)
		return
	}
	if policy.SecondFactorRequired(settings, who.user, groups, v1alpha1.OAuthClient{}) {
		httpjson.WriteError(w, http.StatusConflict, "second_factor_required")
		return
	}
	for _, method := range []string{v1alpha1.MethodTOTP, v1alpha1.MethodRecovery} {
		if err := deleteCredential(r.Context(), deps.Client, who.user.Name, method); err != nil {
			internal(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func newRecoveryCodes(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	enrolled, err := hasEnrolled(r.Context(), deps.Client, who.user.Name, v1alpha1.MethodTOTP)
	if err != nil {
		internal(w, r, err)
		return
	}
	if !enrolled {
		httpjson.WriteError(w, http.StatusConflict, "totp_not_enrolled")
		return
	}
	answerRecoveryCodes(w, r, deps, who)
}

func answerRecoveryCodes(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	if !deps.Limiter.Allow(login.ClientIP(r), deps.Clock()) {
		refuse(w, methods.FailureRateLimited)
		return
	}
	enrollment, err := enroll(r.Context(), deps, who, v1alpha1.MethodRecovery)
	if err != nil {
		internal(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string][]string{"recoveryCodes": enrollment.RecoveryCodes})
}

func listSessions(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	var sessions v1alpha1.SessionList
	if err := deps.Client.List(r.Context(), &sessions, client.InNamespace(release.SystemNamespace), client.MatchingFields{"spec.userRef": who.user.Name}); err != nil {
		internal(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, viewSessions(sessions.Items, who.session.Name, deps.Clock()))
}

func revokeSession(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	session, found, err := ownSession(r.Context(), deps.Client, who, r.PathValue("id"))
	if err != nil {
		internal(w, r, err)
		return
	}
	if !found {
		httpjson.WriteError(w, http.StatusNotFound, "not_found")
		return
	}
	if err := endSession(r.Context(), deps.Client, session); err != nil {
		internal(w, r, err)
		return
	}
	if session.Name == who.session.Name {
		login.ClearCookie(w, login.CookieSession)
	}
	w.WriteHeader(http.StatusNoContent)
}

func logout(w http.ResponseWriter, r *http.Request, deps Deps, who caller) {
	if err := endSession(r.Context(), deps.Client, who.session); err != nil {
		internal(w, r, err)
		return
	}
	login.ClearCookie(w, login.CookieSession)
	w.WriteHeader(http.StatusNoContent)
}

func ownSession(ctx context.Context, c client.Client, who caller, id string) (v1alpha1.Session, bool, error) {
	if !validID(id) {
		return v1alpha1.Session{}, false, nil
	}
	var session v1alpha1.Session
	err := c.Get(ctx, clientKey(id), &session)
	switch {
	case apierrors.IsNotFound(err):
		return v1alpha1.Session{}, false, nil
	case err != nil:
		return v1alpha1.Session{}, false, err
	}
	return session, session.Spec.UserRef == who.user.Name, nil
}

func endSession(ctx context.Context, c client.Client, session v1alpha1.Session) error {
	var tokens v1alpha1.RefreshTokenList
	if err := c.List(ctx, &tokens, client.InNamespace(release.SystemNamespace), client.MatchingFields{"spec.userRef": session.Spec.UserRef}); err != nil {
		return err
	}
	for _, token := range tokens.Items {
		if token.Spec.Session != session.Name {
			continue
		}
		if err := client.IgnoreNotFound(c.Delete(ctx, &token)); err != nil {
			return err
		}
	}
	return client.IgnoreNotFound(c.Delete(ctx, &session))
}

func hasEnrolled(ctx context.Context, c client.Client, user, method string) (bool, error) {
	var credential v1alpha1.Credential
	err := c.Get(ctx, clientKey(v1alpha1.CredentialName(user, method)), &credential)
	switch {
	case apierrors.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, err
	}
	return credential.Status.EnrolledAt != nil, nil
}

func deleteCredential(ctx context.Context, c client.Client, user, method string) error {
	var credential v1alpha1.Credential
	err := c.Get(ctx, clientKey(v1alpha1.CredentialName(user, method)), &credential)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return err
	}
	if err := client.IgnoreNotFound(c.Delete(ctx, &credential)); err != nil {
		return err
	}
	owned := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: credential.Spec.SecretRef}}
	return client.IgnoreNotFound(c.Delete(ctx, owned))
}

func complete(r *http.Request, deps Deps, who caller, name string, given methods.Answer) (methods.Result, error) {
	method, err := registered(deps, name)
	if err != nil {
		return methods.Result{}, err
	}
	flow := methods.Flow{ClientIP: login.ClientIP(r), Now: deps.Clock()}
	if !deps.Limiter.Allow(flow.ClientIP, flow.Now) {
		return methods.Result{Failure: methods.FailureRateLimited}, nil
	}
	return method.Complete(r.Context(), flow, who.user, given)
}

func enroll(ctx context.Context, deps Deps, who caller, name string) (methods.Enrollment, error) {
	method, err := registered(deps, name)
	if err != nil {
		return methods.Enrollment{}, err
	}
	return method.Enroll(ctx, who.user, methods.Answer{})
}

func registered(deps Deps, name string) (methods.Method, error) {
	method, ok := deps.Methods[name]
	if !ok {
		return nil, fmt.Errorf("account: method %q is not registered", name)
	}
	return method, nil
}

func viewUser(user v1alpha1.User, groups []string) userView {
	return userView{
		Name:        user.Name,
		Username:    user.Spec.Username,
		DisplayName: user.Spec.DisplayName,
		Email:       user.Spec.Email,
		Source:      user.Spec.Source,
		Groups:      append([]string{}, groups...),
	}
}

func viewMethods(credentials []v1alpha1.Credential) []methodView {
	views := make([]methodView, 0, len(credentials))
	for _, credential := range credentials {
		if credential.Status.EnrolledAt != nil {
			views = append(views, methodView{Method: credential.Spec.Method, EnrolledAt: credential.Status.EnrolledAt, LastUsed: credential.Status.LastUsed})
		}
	}
	slices.SortFunc(views, func(a, b methodView) int { return cmp.Compare(a.Method, b.Method) })
	return views
}

func viewSessions(sessions []v1alpha1.Session, current string, now time.Time) []sessionView {
	views := make([]sessionView, 0, len(sessions))
	for _, session := range sessions {
		if !now.Before(session.Spec.ExpiresAt.Time) {
			continue
		}
		views = append(views, sessionView{
			ID:        session.Name,
			Current:   session.Name == current,
			AuthTime:  session.Spec.AuthTime,
			LastSeen:  session.Status.LastSeen,
			UserAgent: session.Spec.UserAgent,
			ClientIP:  session.Spec.ClientIP,
		})
	}
	return views
}

func csrfFor(cookie string) string {
	return secret.SHA256Hex(csrfContext + cookie)
}

func failureOf(result methods.Result) string {
	if result.Failure != "" {
		return result.Failure
	}
	return methods.FailureInvalidCredentials
}

func refuse(w http.ResponseWriter, failure string) {
	if failure == methods.FailureRateLimited {
		httpjson.WriteError(w, http.StatusTooManyRequests, failure)
		return
	}
	httpjson.WriteError(w, http.StatusForbidden, failure)
}

func validID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, char := range id {
		if !('0' <= char && char <= '9' || 'a' <= char && char <= 'f') {
			return false
		}
	}
	return true
}

func clientKey(name string) client.ObjectKey {
	return client.ObjectKey{Namespace: release.SystemNamespace, Name: name}
}

func internal(w http.ResponseWriter, r *http.Request, err error) {
	httpjson.Internal(w, r, "account request failed", err)
}
