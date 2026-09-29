package server

import (
	"io/fs"
	"net/http"
	"strings"
)

const (
	uiPrefix     = "/login/"
	assetsPrefix = "assets/"
	indexFile    = "index.html"
)

func UIHandler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index, err := fs.ReadFile(fsys, indexFile)
		if err != nil {
			http.Error(w, "login UI not built", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		name := strings.TrimPrefix(r.URL.Path, uiPrefix)
		switch {
		case strings.HasPrefix(name, assetsPrefix) && isFile(fsys, name):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			http.ServeFileFS(w, r, fsys, name)
		case strings.HasPrefix(name, assetsPrefix):
			http.NotFound(w, r)
		case name != "" && name != indexFile && isFile(fsys, name):
			http.ServeFileFS(w, r, fsys, name)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(index)
		}
	})
}

func isFile(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}
