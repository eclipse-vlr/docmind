package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		dbErr              error
		status             int
		body               string
		checks             int
	}{
		{"live", "GET", "/healthz", errors.New("offline"), 200, `{"status":"ok"}`, 0},
		{"ready", "GET", "/readyz", nil, 200, `{"status":"ready"}`, 1},
		{"offline", "GET", "/readyz", errors.New("password=secret"), 503, `{"error":{"code":"not_ready","message":"Service is not ready"}}`, 1},
		{"unknown", "GET", "/missing", nil, 404, `{"error":{"code":"not_found","message":"Endpoint not found"}}`, 0},
		{"wrong live method", "POST", "/healthz", nil, 405, `{"error":{"code":"method_not_allowed","message":"Method is not allowed"}}`, 0},
		{"wrong ready method", "DELETE", "/readyz", nil, 405, `{"error":{"code":"method_not_allowed","message":"Method is not allowed"}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			checks := 0
			h := NewHandler(func(ctx context.Context) error {
				checks++
				if _, ok := ctx.Deadline(); !ok {
					t.Error("database check has no deadline")
				}
				return tc.dbErr
			}, time.Second, slog.New(slog.NewJSONHandler(&logs, nil)), nil, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path+"?token=private", nil))
			if w.Code != tc.status || strings.TrimSpace(w.Body.String()) != tc.body {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
			if checks != tc.checks {
				t.Fatalf("database checks: %d, want %d", checks, tc.checks)
			}
			if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("X-Request-ID") == "" {
				t.Fatalf("missing response headers: %v", w.Header())
			}
			if tc.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing Allow header")
			}
			if strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "private") || strings.Contains(logs.String(), "/missing") {
				t.Fatal("logs contain sensitive request or dependency details")
			}
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			var event struct {
				RequestID string `json:"request_id"`
				Status    int    `json:"status"`
				Route     string `json:"route"`
			}
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &event); err != nil {
				t.Fatal(err)
			}
			if event.RequestID != w.Header().Get("X-Request-ID") || event.Status != tc.status {
				t.Fatalf("wrong access log: %+v", event)
			}
			if tc.status == 200 && event.Route != "GET "+tc.path {
				t.Fatalf("wrong route in log: %q", event.Route)
			}
		})
	}
}

func TestReadinessCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "caller cancellation"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			var cause error
			h := NewHandler(func(ctx context.Context) error { <-ctx.Done(); cause = ctx.Err(); return cause }, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil).WithContext(ctx))
			want := context.DeadlineExceeded
			if canceled {
				want = context.Canceled
			}
			if w.Code != 503 || !errors.Is(cause, want) {
				t.Fatalf("got status %d, cause %v", w.Code, cause)
			}
		})
	}
}

func TestRequestID(t *testing.T) {
	var ids []string
	h := observe(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids = append(ids, RequestID(r.Context()))
		if RequestID(r.Context()) == "" || RequestID(r.Context()) != w.Header().Get("X-Request-ID") {
			t.Error("missing request context ID")
		}
		w.WriteHeader(http.StatusNoContent)
	}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for range 2 {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Request-ID", "client-controlled")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	if ids[0] == ids[1] || ids[0] == "client-controlled" {
		t.Fatalf("IDs must be fresh and server-generated: %v", ids)
	}
}

func TestHeadHealth(t *testing.T) {
	h := NewHandler(func(context.Context) error { return nil }, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	server := httptest.NewServer(h)
	defer server.Close()
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := server.Client().Head(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || len(body) != 0 {
			t.Fatalf("HEAD got %d %q", resp.StatusCode, body)
		}
	}
}
