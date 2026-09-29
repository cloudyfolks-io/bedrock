package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
)

func HostGuard(host func(context.Context) (string, error), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hostExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		current, err := host(r.Context())
		if err != nil {
			unavailable(w, r, "settings_unavailable", err)
			return
		}
		if r.Host != "sso."+current {
			httpjson.WriteError(w, http.StatusMisdirectedRequest, "misdirected_request")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostExempt(path string) bool {
	return path == pathHealthz || path == pathReadyz || path == pathWebhook
}

func unavailable(w http.ResponseWriter, r *http.Request, code string, err error) {
	slog.ErrorContext(r.Context(), "authn server unavailable", "code", code, "method", r.Method, "path", r.URL.Path, "error", err)
	httpjson.WriteError(w, http.StatusServiceUnavailable, code)
}
