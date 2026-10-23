package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func NewHandler(ready func(context.Context) error, dbTimeout time.Duration, logger *slog.Logger, service DocumentService) http.Handler {
	mux := http.NewServeMux()
	if service != nil {
		registerDocuments(mux, service, dbTimeout, logger)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
		defer cancel()
		if err := ready(ctx); err != nil {
			logger.WarnContext(r.Context(), "readiness_failed", "request_id", RequestID(r.Context()))
			writeError(w, http.StatusServiceUnavailable, "not_ready", "Service is not ready")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method is not allowed")
			return
		}
		if service != nil {
			allow := ""
			if r.URL.Path == "/api/v1/documents" {
				allow = "GET, HEAD, POST"
			}
			if strings.HasPrefix(r.URL.Path, "/api/v1/documents/") && strings.Count(r.URL.Path, "/") == 4 {
				allow = "GET, HEAD, DELETE"
			}
			if allow != "" {
				w.Header().Set("Allow", allow)
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method is not allowed")
				return
			}
		}
		writeError(w, http.StatusNotFound, "not_found", "Endpoint not found")
	})
	return observe(mux, logger)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Bodies here contain only JSON-compatible values. Network write failures
	// cannot be repaired by writing a second response.
	_ = json.NewEncoder(w).Encode(body)
}
