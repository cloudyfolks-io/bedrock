package server

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const indexPage = "<!doctype html><title>login</title>"

func TestUIFallbackAndNotBuilt(t *testing.T) {
	handler := UIHandler(fstest.MapFS{
		"index.html":         {Data: []byte(indexPage)},
		"assets/app-1a2b.js": {Data: []byte("console.log(1)")},
		"favicon.svg":        {Data: []byte("<svg/>")},
	})
	cases := []struct {
		path   string
		status int
		body   string
		cache  string
	}{
		{"/login/", http.StatusOK, indexPage, "no-cache"},
		{"/login/device", http.StatusOK, indexPage, "no-cache"},
		{"/login/account", http.StatusOK, indexPage, "no-cache"},
		{"/login/index.html", http.StatusOK, indexPage, "no-cache"},
		{"/login/assets/app-1a2b.js", http.StatusOK, "console.log(1)", "public, max-age=31536000, immutable"},
		{"/login/assets/missing.js", http.StatusNotFound, "", ""},
		{"/login/assets/", http.StatusNotFound, "", ""},
		{"/login/favicon.svg", http.StatusOK, "<svg/>", ""},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
		resp := recorder.Result()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.status {
			t.Fatalf("%s: status %d, want %d", tc.path, resp.StatusCode, tc.status)
		}
		if tc.body != "" && string(body) != tc.body {
			t.Fatalf("%s: body %q", tc.path, body)
		}
		if tc.status == http.StatusOK && resp.Header.Get("Cache-Control") != tc.cache {
			t.Fatalf("%s: cache %q, want %q", tc.path, resp.Header.Get("Cache-Control"), tc.cache)
		}
		if tc.status == http.StatusOK && (resp.Header.Get("X-Frame-Options") != "DENY" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'")) {
			t.Fatalf("%s: framing headers %v", tc.path, resp.Header)
		}
	}
	notBuilt := httptest.NewRecorder()
	UIHandler(fstest.MapFS{".keep": {}}).ServeHTTP(notBuilt, httptest.NewRequest(http.MethodGet, "/login/", nil))
	if notBuilt.Code != http.StatusServiceUnavailable || !strings.Contains(notBuilt.Body.String(), "login UI not built") {
		t.Fatalf("not built: %d %q", notBuilt.Code, notBuilt.Body.String())
	}
	embedded, err := fs.Sub(UI, "ui/dist")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(embedded, ".keep"); err != nil {
		t.Fatalf("the embedded tree must hold .keep: %v", err)
	}
}
