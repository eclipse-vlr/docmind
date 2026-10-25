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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"docmind/internal/documents"
	"docmind/internal/llm"
	"docmind/internal/rag"
)

type questionFunc func(context.Context, string, []string) (rag.Answer, error)

func (f questionFunc) Ask(ctx context.Context, q string, ids []string) (rag.Answer, error) {
	return f(ctx, q, ids)
}

func questionHandler(service QuestionService, timeout time.Duration, logs io.Writer) http.Handler {
	return NewHandler(func(context.Context) error { return nil }, time.Second, slog.New(slog.NewJSONHandler(logs, nil)), nil, &QuestionOptions{Service: service, Timeout: timeout})
}

func questionBody() string {
	b, _ := json.Marshal(map[string]any{"question": "PostgreSQL", "document_ids": []string{documents.NewID()}})
	return string(b)
}

func questionRequest(body string) *http.Request {
	r := httptest.NewRequest("POST", "/api/v1/questions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestQuestionHTTPJSONAndLimits(t *testing.T) {
	id := documents.NewID()
	for _, tc := range []struct {
		name, body, contentType, encoding string
		status                            int
	}{
		{"valid", questionBody(), "application/json", "", 200},
		{"valid charset", questionBody(), "application/json; charset=UTF-8", "", 200},
		{"wrong media", questionBody(), "text/plain", "", 415},
		{"wrong charset", questionBody(), "application/json; charset=latin1", "", 415},
		{"compressed", questionBody(), "application/json", "gzip", 415},
		{"malformed", "{", "application/json", "", 400},
		{"null", "null", "application/json", "", 400},
		{"multiple objects", questionBody() + " {}", "application/json", "", 400},
		{"unknown field", `{"question":"q","document_ids":["` + id + `"],"secret":"x"}`, "application/json", "", 400},
		{"missing question", `{"document_ids":["` + id + `"]}`, "application/json", "", 400},
		{"missing documents", `{"question":"q"}`, "application/json", "", 400},
		{"invalid id", `{"question":"q","document_ids":["invalid"]}`, "application/json", "", 400},
		{"duplicate id", `{"question":"q","document_ids":["` + id + `","` + id + `"]}`, "application/json", "", 400},
		{"long question", `{"question":"` + strings.Repeat("x", llm.MaxInputBytes+1) + `","document_ids":["` + id + `"]}`, "application/json", "", 400},
		{"invalid utf8", "{\"question\":\"\xff\",\"document_ids\":[\"" + id + "\"]}", "application/json", "", 400},
		{"oversized", strings.Repeat(" ", maxQuestionBody+1), "application/json", "", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			service := questionFunc(func(ctx context.Context, q string, ids []string) (rag.Answer, error) {
				calls++
				if _, ok := ctx.Deadline(); !ok {
					t.Error("no deadline")
				}
				return rag.Answer{Answer: "answer", Sources: []rag.Source{}}, nil
			})
			r := questionRequest(tc.body)
			r.ContentLength = -1
			r.Header.Set("Content-Type", tc.contentType)
			r.Header.Set("Content-Encoding", tc.encoding)
			w := httptest.NewRecorder()
			questionHandler(service, time.Second, io.Discard).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if (calls == 1) != (tc.status == 200) {
				t.Fatal("invalid request reached service", calls)
			}
			if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing JSON or request ID")
			}
			if tc.status >= 400 {
				var payload struct {
					Error struct{ Code, Message string }
				}
				if json.Unmarshal(w.Body.Bytes(), &payload) != nil || payload.Error.Code == "" || payload.Error.Message == "" {
					t.Fatal("invalid error envelope")
				}
			}
		})
	}
}

func TestQuestionHTTPErrorsAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		problem error
		status  int
		code    string
	}{
		{rag.ErrInvalid, 400, "invalid_question"}, {documents.ErrNotFound, 404, "document_not_found"},
		{rag.ErrNotReady, 409, "documents_not_ready"}, {rag.ErrModelMismatch, 409, "embedding_model_mismatch"},
		{llm.ErrUnavailable, 503, "provider_unavailable"}, {rag.ErrUnavailable, 503, "service_unavailable"},
		{llm.ErrTimeout, 504, "question_timeout"}, {context.DeadlineExceeded, 504, "question_timeout"},
		{llm.ErrRejected, 502, "provider_error"}, {llm.ErrResponse, 502, "provider_error"},
		{context.Canceled, 408, "request_canceled"}, {errors.New("private-diagnostics secret-key"), 500, "internal_error"},
	} {
		t.Run(tc.code+tc.problem.Error(), func(t *testing.T) {
			var logs bytes.Buffer
			s := questionFunc(func(context.Context, string, []string) (rag.Answer, error) { return rag.Answer{}, tc.problem })
			w := httptest.NewRecorder()
			questionHandler(s, time.Second, &logs).ServeHTTP(w, questionRequest(questionBody()))
			var payload struct{ Error struct{ Code string } }
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || w.Code != tc.status || payload.Error.Code != tc.code {
				t.Fatal(w.Code, w.Body.String(), err)
			}
			if strings.Contains(logs.String()+w.Body.String(), "secret-key") || strings.Contains(logs.String()+w.Body.String(), "private-diagnostics") || strings.Contains(logs.String(), "PostgreSQL") {
				t.Fatal("sensitive data leaked")
			}
			if tc.status == 503 && w.Header().Get("Retry-After") == "" {
				t.Fatal("missing retry guidance")
			}
		})
	}
}

func TestQuestionHTTPTimeoutAndMethods(t *testing.T) {
	s := questionFunc(func(ctx context.Context, _ string, _ []string) (rag.Answer, error) {
		<-ctx.Done()
		return rag.Answer{}, ctx.Err()
	})
	h := questionHandler(s, time.Millisecond, io.Discard)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, questionRequest(questionBody()))
	if w.Code != 504 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, method := range []string{"GET", "HEAD", "DELETE", "PUT"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/questions", nil))
		if w.Code != 405 || w.Header().Get("Allow") != "POST" {
			t.Fatal("wrong method handling", method, w.Code, w.Header())
		}
	}
}

func TestQuestionHTTPAdmissionIsBounded(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var active, maxActive atomic.Int32
	s := questionFunc(func(ctx context.Context, _ string, _ []string) (rag.Answer, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for previous := maxActive.Load(); n > previous && !maxActive.CompareAndSwap(previous, n); previous = maxActive.Load() {
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return rag.Answer{}, ctx.Err()
		}
		return rag.Answer{Answer: "answer", Sources: []rag.Source{}}, nil
	})
	h := questionHandler(s, 10*time.Second, io.Discard)
	var wg sync.WaitGroup
	defer func() { close(release); wg.Wait() }()
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); h.ServeHTTP(httptest.NewRecorder(), questionRequest(questionBody())) }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("requests did not enter")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, questionRequest(questionBody()))
	if w.Code != 503 || maxActive.Load() != 2 || w.Header().Get("Retry-After") == "" {
		t.Fatal("unbounded admission", w.Code, maxActive.Load())
	}
}
