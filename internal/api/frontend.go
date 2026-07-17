package api

import (
	"io/fs"
	"net/http"
	"strings"
)

// mountFrontend serves the embedded SPA: exact asset paths from the
// embedded FS, and index.html for / and client-side routes (/kiosk,
// /settings).
func (s *Server) mountFrontend(mux *http.ServeMux, frontend fs.FS) {
	fileServer := http.FileServer(http.FS(frontend))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if f, err := frontend.Open(path); err == nil {
				f.Close()
				if strings.HasPrefix(path, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback
		w.Header().Set("Cache-Control", "no-cache")
		index, err := fs.ReadFile(frontend, "index.html")
		if err != nil {
			http.Error(w, "frontend not built", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
}
