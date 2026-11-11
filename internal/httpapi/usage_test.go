package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"docmind/internal/usage"
)

type usageSummaryStub struct{ err error }

func (s usageSummaryStub) Summary(ctx context.Context) (usage.Summary, error) {
	if _, ok := ctx.Deadline(); !ok {
		return usage.Summary{}, errors.New("no deadline")
	}
	return usage.Summary{Day: "2026-11-12", Limit: "100.000000000", Remaining: "100.000000000"}, s.err
}
func TestUsageRoute(t *testing.T) {
	for _, tc := range []struct {
		method string
		err    error
		status int
	}{{"GET", nil, 200}, {"GET", errors.New("secret database credentials"), 503}, {"POST", nil, 405}} {
		h := NewHandler(func(context.Context) error { return nil }, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, &QuestionOptions{Usage: usageSummaryStub{tc.err}})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/v1/usage", nil))
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
		if tc.status == 200 {
			var s usage.Summary
			if json.Unmarshal(w.Body.Bytes(), &s) != nil || s.Limit != "100.000000000" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Body.String())
			}
		}
	}
}
func TestQuestionBudgetErrors(t *testing.T) {
	h := &questionHandlers{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{usage.ErrBudget, 429, "budget_exceeded"}, {usage.ErrUnavailable, 503, "accounting_unavailable"}} {
		w := httptest.NewRecorder()
		h.fail(w, httptest.NewRequest("POST", "/api/v1/questions", nil), tc.err)
		var out struct{ Error struct{ Code string } }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != tc.status || out.Error.Code != tc.code || w.Header().Get("Retry-After") == "" {
			t.Fatal(w.Code, w.Body.String(), w.Header())
		}
	}
}
