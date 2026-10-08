package proxy

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
)

const (
	pathHealthz = "/healthz"
	pathReadyz  = "/readyz"
)

type Routes struct {
	Console http.Handler
	API     http.Handler
	Health  http.Handler
}

func Dispatch(host func(context.Context) (string, error), routes Routes) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathHealthz || r.URL.Path == pathReadyz {
			routes.Health.ServeHTTP(w, r)
			return
		}
		current, err := host(r.Context())
		if err != nil {
			hostUnavailable(w, r, err)
			return
		}
		current = strings.ToLower(current)
		switch requestHost(r.Host) {
		case "console." + current:
			routes.Console.ServeHTTP(w, r)
		case "api." + current:
			routes.API.ServeHTTP(w, r)
		case current:
			http.Redirect(w, r, "https://console."+current+r.URL.RequestURI(), http.StatusTemporaryRedirect)
		default:
			httpjson.WriteError(w, http.StatusMisdirectedRequest, "misdirected_request")
		}
	})
}

func requestHost(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	if stripped, _, err := net.SplitHostPort(name); err == nil {
		name = stripped
	}
	return strings.TrimSuffix(name, ".")
}

func hostUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrHostNotSet) {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "platform_host_not_set")
		return
	}
	slog.ErrorContext(r.Context(), "proxy host unavailable", "method", r.Method, "path", r.URL.Path, "error", err)
	httpjson.WriteError(w, http.StatusServiceUnavailable, "upstream_unavailable")
}
