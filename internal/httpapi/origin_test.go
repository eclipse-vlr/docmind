package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCrossSiteMutationsRejected(t *testing.T) {
	h := NewHandler(func(context.Context) error { return nil }, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	for _, path := range []string{"/api/v1/documents", "/api/v1/questions", "/api/v1/documents/id/reindex"} {
		for _, metadata := range []string{"", "cross-site"} {
			r := httptest.NewRequest("POST", "http://localhost:8080"+path, strings.NewReader("untrusted"))
			r.Header.Set("Origin", "https://untrusted.example")
			if metadata != "" {
				r.Header.Set("Sec-Fetch-Site", metadata)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("%s %q: %d", path, metadata, w.Code)
			}
		}
	}
	for _, origin := range []string{"", "http://localhost:8080"} {
		r := httptest.NewRequest("POST", "http://localhost:8080/api/v1/documents", nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == 403 {
			t.Fatal("same-origin or CLI rejected")
		}
	}
}
