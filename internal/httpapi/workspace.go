package httpapi

import (
	"embed"
	"net/http"

	"docmind/api"
)

//go:embed web/*
var workspace embed.FS

func registerWorkspace(mux *http.ServeMux) {
	for route, file := range map[string]string{"GET /{$}": "index.html", "GET /app.css": "app.css", "GET /app.js": "app.js", "GET /theme.js": "theme.js"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			body, err := workspace.ReadFile("web/" + file)
			if err != nil {
				writeError(w, 500, "internal_error", "Workspace unavailable")
				return
			}
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-cache")
			switch file {
			case "index.html":
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
			case "app.css":
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			case "app.js", "theme.js":
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			}
			_, _ = w.Write(body)
		})
	}
	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(api.Spec)
	})
}
