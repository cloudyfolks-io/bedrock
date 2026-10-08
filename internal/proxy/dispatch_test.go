package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func named(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(name))
	})
}

func labHost(context.Context) (string, error) { return "lab.example", nil }

func labRoutes() Routes {
	return Routes{Console: named("console"), API: named("api"), Health: named("health")}
}

func serve(handler http.Handler, method, target, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestDispatchRoutesByHost(t *testing.T) {
	handler := Dispatch(labHost, labRoutes())
	cases := []struct {
		host string
		want string
	}{
		{"console.lab.example", "console"},
		{"CONSOLE.LAB.EXAMPLE:443", "console"},
		{"console.lab.example.", "console"},
		{"api.lab.example", "api"},
	}
	for _, tc := range cases {
		rec := serve(handler, http.MethodGet, "/x", tc.host)
		if rec.Code != http.StatusOK || rec.Body.String() != tc.want {
			t.Errorf("%s: %d %q, want %q", tc.host, rec.Code, rec.Body.String(), tc.want)
		}
	}
	for _, host := range []string{"evil.example", "console.lab.example.evil.test", "xconsole.lab.example", "lab.example.evil.test"} {
		rec := serve(handler, http.MethodGet, "/x", host)
		if rec.Code != http.StatusMisdirectedRequest || !strings.Contains(rec.Body.String(), `"misdirected_request"`) {
			t.Errorf("%s: %d %s", host, rec.Code, rec.Body.String())
		}
	}
}

func TestApexRedirectKeepsPathAndQuery(t *testing.T) {
	handler := Dispatch(labHost, labRoutes())
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := serve(handler, method, "https://lab.example/p/web/vms?x=1", "lab.example")
		if rec.Code != http.StatusTemporaryRedirect {
			t.Fatalf("%s: status %d", method, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != "https://console.lab.example/p/web/vms?x=1" {
			t.Fatalf("%s: Location %q", method, got)
		}
	}
}

func TestDispatchAnswersUnsetHost(t *testing.T) {
	unset := Dispatch(func(context.Context) (string, error) { return "", ErrHostNotSet }, labRoutes())
	rec := serve(unset, http.MethodGet, "/", "console.lab.example")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"platform_host_not_set"`) {
		t.Fatalf("unset: %d %s", rec.Code, rec.Body.String())
	}
	broken := Dispatch(func(context.Context) (string, error) { return "", errors.New("boom") }, labRoutes())
	rec = serve(broken, http.MethodGet, "/", "console.lab.example")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"upstream_unavailable"`) {
		t.Fatalf("broken: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHealthPathsSkipTheHostCheck(t *testing.T) {
	failing := Dispatch(func(context.Context) (string, error) { return "", ErrHostNotSet }, labRoutes())
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := serve(failing, http.MethodGet, path, "10.1.2.3:8443")
		if rec.Code != http.StatusOK || rec.Body.String() != "health" {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
	}
}
