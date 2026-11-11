package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkspaceAndMetricsPrivacy(t *testing.T) {
	h := NewHandler(func(context.Context) error { return nil }, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	for _, path := range []string{"/", "/app.js", "/app.css", "/theme.js", "/usage.js", "/openapi.yaml"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Body.Len() == 0 || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("asset %s: %d", path, w.Code)
		}
	}
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("SECRET", "/private-filename?key=secret", nil))
		})
	}
	wg.Wait()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `docmind_http_requests_total{route="/",method="OTHER",status="4xx"} 30`) {
		t.Fatal(body)
	}
	for _, secret := range []string{"private-filename", "secret", "SECRET"} {
		if strings.Contains(body, secret) {
			t.Fatal("metric leaks request data")
		}
	}
	other := NewHandler(func(context.Context) error { return nil }, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	w = httptest.NewRecorder()
	other.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(w.Body.String(), "OTHER") {
		t.Fatal("metrics shared across handler lifetimes")
	}
}
