package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
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
			writeError(w, http.StatusMisdirectedRequest, "misdirected_request")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostExempt(path string) bool {
	return path == pathHealthz || path == pathReadyz || path == pathWebhook
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func unavailable(w http.ResponseWriter, r *http.Request, code string, err error) {
	slog.ErrorContext(r.Context(), "authn server unavailable", "code", code, "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusServiceUnavailable, code)
}
