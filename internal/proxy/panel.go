package proxy

import (
	"context"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const (
	assetsPrefix = "assets/"
	indexFile    = "index.html"
	notBuiltPage = "<!doctype html><title>Console</title><p>The panel is not built. Run the UI build and rebuild the image.</p>"
)

var reservedPrefixes = []string{"/api", "/apis", "/openapi", "/proxy", "/oauth"}

func PanelHandler(fsys fs.FS, host func(context.Context) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current, err := host(r.Context())
		if err != nil {
			hostUnavailable(w, r, err)
			return
		}
		setPanelHeaders(w, current)
		switch {
		case r.Method != http.MethodGet && r.Method != http.MethodHead:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		case isReservedPath(r.URL.Path):
			http.NotFound(w, r)
		default:
			servePanelFile(w, r, fsys)
		}
	})
}

func servePanelFile(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	index, err := fs.ReadFile(fsys, indexFile)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(notBuiltPage))
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	switch {
	case strings.HasPrefix(name, assetsPrefix) && isPanelFile(fsys, name):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFileFS(w, r, fsys, name)
	case strings.HasPrefix(name, assetsPrefix):
		http.NotFound(w, r)
	case name != "" && name != indexFile && isPanelFile(fsys, name):
		http.ServeFileFS(w, r, fsys, name)
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	}
}

func setPanelHeaders(w http.ResponseWriter, host string) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	w.Header().Set("Content-Security-Policy", panelCSP(host))
}

func panelCSP(host string) string {
	return "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; " +
		"connect-src 'self' wss://console." + host + " https://upload." + host + "; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
}

func isReservedPath(p string) bool {
	for _, prefix := range reservedPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

func isPanelFile(fsys fs.FS, name string) bool {
	if strings.HasPrefix(path.Base(name), ".") {
		return false
	}
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}
