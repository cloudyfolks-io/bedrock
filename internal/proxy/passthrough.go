package proxy

import (
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
	"github.com/cloudyfolks-io/bedrock/internal/authn/login"
)

var kubernetesRoots = []string{"/api", "/apis", "/openapi", "/version"}

func ConsolePassThrough(u *Upstream) http.Handler {
	return passThrough(u, stripBrowserHeaders)
}

func APIPassThrough(u *Upstream) http.Handler {
	return passThrough(u, stripUntrustedHeaders)
}

func passThrough(u *Upstream, strip func(http.Header)) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite:        rewriteRequest(u, strip),
		Transport:      u.transport,
		FlushInterval:  -1,
		ModifyResponse: sanitizeResponse,
		ErrorLog:       log.New(io.Discard, "", 0),
		ErrorHandler:   upstreamDown,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, found := CallerFrom(r.Context())
		if !found || caller.Token == "" {
			httpjson.WriteError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		if !AllowedPath(r.URL.Path) || strings.Contains(strings.ToLower(r.URL.RawPath), "%2f") {
			httpjson.WriteError(w, http.StatusNotFound, "not_found")
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func AllowedPath(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." || segment == "." {
			return false
		}
	}
	for _, root := range kubernetesRoots {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func rewriteRequest(u *Upstream, strip func(http.Header)) func(*httputil.ProxyRequest) {
	return func(pr *httputil.ProxyRequest) {
		caller, _ := CallerFrom(pr.In.Context())
		pr.SetURL(u.target)
		strip(pr.Out.Header)
		pr.Out.Header.Set("Authorization", "Bearer "+caller.Token)
		pr.Out.Header.Set("X-Forwarded-For", login.ClientIP(pr.In))
		pr.Out.Header.Set("User-Agent", proxyUserAgent)
	}
}

func stripBrowserHeaders(h http.Header) {
	stripUntrustedHeaders(h)
	for name := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), "Impersonate-") {
			delete(h, name)
		}
	}
}

func stripUntrustedHeaders(h http.Header) {
	for name := range h {
		canonical := http.CanonicalHeaderKey(name)
		if canonical == "Authorization" || canonical == "Cookie" || canonical == "Forwarded" || strings.HasPrefix(canonical, "X-Remote-") {
			delete(h, name)
		}
	}
}

func sanitizeResponse(res *http.Response) error {
	res.Header.Del("Set-Cookie")
	if res.Header.Get("Cache-Control") == "" {
		res.Header.Set("Cache-Control", "no-store")
	}
	return nil
}

func upstreamDown(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "pass-through failed", "method", r.Method, "path", r.URL.Path, "error", err)
	httpjson.WriteError(w, http.StatusBadGateway, "upstream_unavailable")
}
