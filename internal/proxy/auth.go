package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
)

const (
	maxBearerLength = 8192
	fetchSiteHeader = "Sec-Fetch-Site"
	fetchCrossSite  = "cross-site"
)

var errBearerChallenge = fmt.Errorf("%w: bearer token required", ErrUnauthenticated)

type SessionSource interface {
	Resolve(ctx context.Context, cookieValue string) (Principal, error)
}

type Caller struct {
	Token     string
	Principal *Principal
}

type Authenticator func(*http.Request) (Caller, error)

type callerKey struct{}

func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

func CookieAuth(sessions SessionSource) Authenticator {
	return func(r *http.Request) (Caller, error) {
		cookie, err := r.Cookie(CookieSession)
		if err != nil {
			return Caller{}, ErrUnauthenticated
		}
		principal, err := sessions.Resolve(r.Context(), cookie.Value)
		if err != nil {
			return Caller{}, err
		}
		return Caller{Token: principal.Access, Principal: &principal}, nil
	}
}

func BearerAuth() Authenticator {
	return func(r *http.Request) (Caller, error) {
		scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
		if !found || !strings.EqualFold(scheme, "Bearer") || !ValidBearer(token) {
			return Caller{}, errBearerChallenge
		}
		return Caller{Token: token}, nil
	}
}

func ValidBearer(token string) bool {
	if len(token) == 0 || len(token) > maxBearerLength {
		return false
	}
	if secret.IsAPIToken(token) {
		return true
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.ContainsFunc(part, notBase64URL) {
			return false
		}
	}
	return true
}

func notBase64URL(r rune) bool {
	return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
}

func Authenticate(auth Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, err := auth(r)
		if err != nil {
			writeAuthError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), caller)))
	})
}

func writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrSessionEnded):
		httpjson.WriteError(w, http.StatusUnauthorized, "session_ended")
	case errors.Is(err, ErrUnauthenticated):
		if errors.Is(err, errBearerChallenge) {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		httpjson.WriteError(w, http.StatusUnauthorized, "unauthenticated")
	default:
		slog.ErrorContext(r.Context(), "authenticate request", "method", r.Method, "path", r.URL.Path, "error", err)
		httpjson.WriteError(w, http.StatusBadGateway, "upstream_unavailable")
	}
}

func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, found := CallerFrom(r.Context())
		if found && caller.Principal != nil && unsafeMethod(r.Method) && !secret.Equal(r.Header.Get(HeaderCSRF), caller.Principal.CSRF) {
			httpjson.WriteError(w, http.StatusForbidden, "csrf")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func RequireOrigin(host func(context.Context) (string, error), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, found := CallerFrom(r.Context())
		if !found || caller.Principal == nil {
			next.ServeHTTP(w, r)
			return
		}
		current, err := host(r.Context())
		if err != nil {
			hostUnavailable(w, r, err)
			return
		}
		origin := r.Header.Get("Origin")
		mustCarryOrigin := isWebSocketUpgrade(r) || unsafeMethod(r.Method)
		if origin != "" && origin != "https://console."+current || origin == "" && mustCarryOrigin {
			httpjson.WriteError(w, http.StatusForbidden, "origin")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func RefuseCrossSite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, cookieErr := r.Cookie(CookieSession)
		if cookieErr == nil && unsafeMethod(r.Method) && r.Header.Get(fetchSiteHeader) == fetchCrossSite {
			httpjson.WriteError(w, http.StatusForbidden, "csrf")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func isWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, value := range r.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}
