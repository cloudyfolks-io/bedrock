package proxy

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func builtPanel() fstest.MapFS {
	return fstest.MapFS{
		"index.html":         {Data: []byte("<div id=\"root\"></div>")},
		"assets/app-1a2b.js": {Data: []byte("console.log(1)")},
		"favicon.svg":        {Data: []byte("<svg/>")},
		".keep":              {Data: nil},
	}
}

func TestPanelFallbackAndNotBuilt(t *testing.T) {
	handler := PanelHandler(builtPanel(), labHost)
	for _, target := range []string{"/", "/p/web/vms", "/admin/hosts"} {
		rec := serve(handler, http.MethodGet, target, "console.lab.example")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="root"`) || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %q %q", target, rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
		}
	}
	rec := serve(handler, http.MethodGet, "/assets/app-1a2b.js", "console.lab.example")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("asset: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := serve(handler, http.MethodGet, "/assets/missing.js", "console.lab.example"); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset: %d", rec.Code)
	}
	if rec := serve(handler, http.MethodGet, "/favicon.svg", "console.lab.example"); rec.Code != http.StatusOK || rec.Body.String() != "<svg/>" {
		t.Errorf("favicon: %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve(handler, http.MethodGet, "/.keep", "console.lab.example"); strings.Contains(rec.Header().Get("Content-Type"), "octet") || rec.Body.Len() == 0 {
		t.Errorf("keep file served: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	unbuilt := PanelHandler(fstest.MapFS{".keep": {}}, labHost)
	rec = serve(unbuilt, http.MethodGet, "/", "console.lab.example")
	if rec.Code != http.StatusServiceUnavailable || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "The panel is not built") {
		t.Errorf("not built: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestPanelRefusesAPIPaths(t *testing.T) {
	handler := PanelHandler(builtPanel(), labHost)
	for _, target := range []string{"/api/x", "/apis/x", "/openapi/v3", "/proxy/v1/session", "/oauth/login", "/api", "/proxy"} {
		rec := serve(handler, http.MethodGet, target, "console.lab.example")
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "root") {
			t.Errorf("%s: %d %q", target, rec.Code, rec.Body.String())
		}
	}
}

func TestPanelSecurityHeaders(t *testing.T) {
	handler := PanelHandler(builtPanel(), labHost)
	want := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self' wss://console.lab.example https://upload.lab.example; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	for _, target := range []string{"/", "/assets/app-1a2b.js", "/assets/none.js", "/api/x"} {
		header := serve(handler, http.MethodGet, target, "console.lab.example").Header()
		checks := map[string]string{
			"X-Frame-Options":           "DENY",
			"X-Content-Type-Options":    "nosniff",
			"Referrer-Policy":           "no-referrer",
			"Strict-Transport-Security": "max-age=31536000",
			"Content-Security-Policy":   want,
		}
		for name, value := range checks {
			if got := header.Get(name); got != value {
				t.Errorf("%s %s = %q, want %q", target, name, got, value)
			}
		}
	}
}

func TestPanelOnlyAnswersGetAndHead(t *testing.T) {
	handler := PanelHandler(builtPanel(), labHost)
	if rec := serve(handler, http.MethodPost, "/", "console.lab.example"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodHead, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD: %d", rec.Code)
	}
}

func TestEmbeddedPanelHoldsTheKeepFile(t *testing.T) {
	sub, err := fs.Sub(UI, "ui/dist")
	if err != nil {
		t.Fatal(err)
	}
	file, err := sub.Open(".keep")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
}
