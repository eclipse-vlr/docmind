package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func NewHandler(ready func(context.Context) error, dbTimeout time.Duration, logger *slog.Logger, service DocumentService, questions *QuestionOptions) http.Handler {
	mux := http.NewServeMux()
	metrics := newHTTPMetrics()
	mux.Handle("GET /metrics", metrics)
	registerWorkspace(mux)
	if questions != nil && questions.Usage != nil {
		mux.HandleFunc("GET /api/v1/usage", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), dbTimeout)
			defer cancel()
			summary, err := questions.Usage.Summary(ctx)
			if err != nil {
				writeError(w, 503, "accounting_unavailable", "AI usage accounting is temporarily unavailable")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, 200, summary)
		})
	}
	if service != nil {
		registerDocuments(mux, service, dbTimeout, logger)
	}
	if questions != nil && questions.Service != nil {
		registerQuestions(mux, questions, logger)
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
		if questions != nil && questions.Service != nil && r.URL.Path == "/api/v1/questions" {
			w.Header().Set("Allow", "POST")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method is not allowed")
			return
		}
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method is not allowed")
			return
		}
		if questions != nil && questions.Usage != nil && r.URL.Path == "/api/v1/usage" {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, 405, "method_not_allowed", "Method is not allowed")
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
	return observe(metrics.wrap(mux), logger)
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
