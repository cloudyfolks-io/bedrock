package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
	"github.com/cloudyfolks-io/bedrock/internal/authn/login"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	randomLength   = 43
	maxReturnTo    = 512
	defaultReturn  = "/"
	signInFailedUI = `<!doctype html><title>Sign-in failed</title><p>Sign-in failed.</p><p><a href="` + PathLogin + `">Try again</a></p>`
)

type LoginDeps struct {
	Store   *Store
	OIDC    *OIDC
	Reader  client.Reader
	Random  io.Reader
	Clock   func() time.Time
	Limiter *methods.RateLimiter
	Host    func(context.Context) (string, error)
}

func LoginHandlers(d LoginDeps) (http.Handler, http.Handler, http.Handler) {
	return http.HandlerFunc(d.start), http.HandlerFunc(d.callback), http.HandlerFunc(d.logout)
}

func (d LoginDeps) start(w http.ResponseWriter, r *http.Request) {
	if !d.Limiter.Allow(login.ClientIP(r), d.Clock()) {
		httpjson.WriteError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	state, nonce, err := d.randomPair()
	if err != nil {
		httpjson.Internal(w, r, "generate login values", err)
		return
	}
	verifier, challenge, err := PKCE(d.Random)
	if err != nil {
		httpjson.Internal(w, r, "generate pkce pair", err)
		return
	}
	target, err := d.OIDC.AuthorizeURL(r.Context(), state, nonce, challenge)
	if err != nil {
		upstreamFailure(w, r, "build authorize url", err)
		return
	}
	pending := LoginState{Verifier: verifier, Nonce: nonce, ReturnTo: SafeReturnTo(r.URL.Query().Get("returnTo")), ExpiresAt: d.Clock().Add(loginCookieLife)}
	if err := d.Store.CreateLogin(r.Context(), secret.SHA256Hex(state), pending); err != nil {
		upstreamFailure(w, r, "store login state", err)
		return
	}
	setLoginCookie(w, state)
	http.Redirect(w, r, target, http.StatusFound)
}

func (d LoginDeps) callback(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("error") != "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(signInFailedUI))
		return
	}
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(CookieLogin)
	code := r.URL.Query().Get("code")
	if err != nil || state == "" || code == "" || !secret.Equal(state, cookie.Value) {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	pending, err := d.Store.TakeLogin(r.Context(), secret.SHA256Hex(state))
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	tokens, err := d.OIDC.Exchange(r.Context(), code, pending.Verifier, pending.Nonce)
	if err != nil {
		upstreamFailure(w, r, "exchange the authorization code", err)
		return
	}
	var authnSession v1alpha1.Session
	key := client.ObjectKey{Namespace: release.SystemNamespace, Name: tokens.Identity.SessionName}
	if err := d.Reader.Get(r.Context(), key, &authnSession); err != nil {
		upstreamFailure(w, r, "read the authn session", err)
		return
	}
	now := d.Clock()
	life := authnSession.Spec.ExpiresAt.Sub(now)
	if life <= 0 {
		upstreamFailure(w, r, "read the authn session", errors.New("session already expired"))
		return
	}
	cookieValue, csrf, err := d.randomPair()
	if err != nil {
		httpjson.Internal(w, r, "generate session values", err)
		return
	}
	rec := Record{
		Subject: tokens.Identity.Subject, DisplayName: tokens.Identity.Name, Email: tokens.Identity.Email,
		Groups: tokens.Identity.Groups, SessionName: tokens.Identity.SessionName,
		Access: tokens.Access, AccessExpiry: tokens.AccessExpiry, Refresh: tokens.Refresh,
		CSRF: csrf, CreatedAt: now, ExpiresAt: authnSession.Spec.ExpiresAt.Time,
	}
	if err := d.Store.CreateSession(r.Context(), secret.SHA256Hex(cookieValue), rec); err != nil {
		upstreamFailure(w, r, "store the session", err)
		return
	}
	clearLoginCookie(w)
	SetSessionCookie(w, cookieValue, life)
	http.Redirect(w, r, pending.ReturnTo, http.StatusFound)
}

func (d LoginDeps) logout(w http.ResponseWriter, r *http.Request) {
	ClearSessionCookie(w)
	cookie, err := r.Cookie(CookieSession)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	hash := secret.SHA256Hex(cookie.Value)
	if rec, err := d.Store.Session(r.Context(), hash); err == nil {
		if err := d.OIDC.Revoke(r.Context(), rec.Refresh); err != nil {
			slog.WarnContext(r.Context(), "revoke the refresh token", "error", err)
		}
	}
	if err := d.Store.DeleteSession(r.Context(), hash); err != nil {
		upstreamFailure(w, r, "delete the session", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d LoginDeps) randomPair() (string, string, error) {
	first, err := secret.Base62(d.Random, randomLength)
	if err != nil {
		return "", "", err
	}
	second, err := secret.Base62(d.Random, randomLength)
	return first, second, err
}

func SafeReturnTo(raw string) string {
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return defaultReturn
	}
	for _, candidate := range []string{raw, decoded} {
		if !plainLocalPath(candidate) {
			return defaultReturn
		}
	}
	return raw
}

func plainLocalPath(candidate string) bool {
	if len(candidate) == 0 || len(candidate) > maxReturnTo || !strings.HasPrefix(candidate, "/") || strings.HasPrefix(candidate, "//") {
		return false
	}
	if strings.Contains(candidate, `\`) {
		return false
	}
	return !strings.ContainsFunc(candidate, unicode.IsControl)
}

func upstreamFailure(w http.ResponseWriter, r *http.Request, message string, err error) {
	slog.ErrorContext(r.Context(), "proxy request failed: "+message, "method", r.Method, "path", r.URL.Path, "error", err)
	httpjson.WriteError(w, http.StatusBadGateway, "upstream_unavailable")
}
