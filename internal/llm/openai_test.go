package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type embeddingItem struct {
	Index     *int      `json:"index,omitempty"`
	Embedding []float32 `json:"embedding"`
}

func responseItem(index int, value float32) embeddingItem {
	v := make([]float32, Dimensions)
	v[0] = value
	return embeddingItem{Index: &index, Embedding: v}
}

func testOpenAI(t *testing.T, handler http.HandlerFunc) *OpenAI {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p, err := NewOpenAI("test-secret-key", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	p.endpoint = srv.URL
	return p
}

func TestOpenAIRequestAndResponseOrder(t *testing.T) {
	p := testOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-secret-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("invalid request headers or method")
		}
		var body struct {
			Model      string   `json:"model"`
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
			Encoding   string   `json:"encoding_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != OpenAIModel || body.Dimensions != Dimensions || body.Encoding != "float" || len(body.Input) != 2 || body.Input[0] != "first" || body.Input[1] != "second" {
			t.Error("invalid embedding request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": OpenAIModel, "data": []embeddingItem{responseItem(1, 2), responseItem(0, 1)}})
	})
	v, err := p.Embed(context.Background(), []string{"first", "second"})
	if err != nil || len(v) != 2 || v[0][0] != 1 || v[1][0] != 2 {
		t.Fatalf("order lost: %v", err)
	}
}

func TestOpenAIRejectsMalformedResponses(t *testing.T) {
	missingIndex := responseItem(0, 1)
	missingIndex.Index = nil
	short := responseItem(0, 1)
	short.Embedding = short.Embedding[:Dimensions-1]
	for _, tc := range []struct {
		name  string
		model string
		items []embeddingItem
	}{
		{"missing index", OpenAIModel, []embeddingItem{missingIndex}},
		{"wrong index", OpenAIModel, []embeddingItem{responseItem(1, 1)}},
		{"wrong dimensions", OpenAIModel, []embeddingItem{short}},
		{"zero vector", OpenAIModel, []embeddingItem{responseItem(0, 0)}},
		{"wrong model", "different-model", []embeddingItem{responseItem(0, 1)}},
		{"missing vector", OpenAIModel, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"model": tc.model, "data": tc.items})
			})
			if _, err := p.Embed(context.Background(), []string{"text"}); !errors.Is(err, ErrResponse) {
				t.Fatal(err)
			}
		})
	}
	t.Run("duplicate index", func(t *testing.T) {
		p := testOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"model": OpenAIModel, "data": []embeddingItem{responseItem(0, 1), responseItem(0, 2)}})
		})
		if _, err := p.Embed(context.Background(), []string{"one", "two"}); !errors.Is(err, ErrResponse) {
			t.Fatal(err)
		}
	})
	for _, raw := range []string{`{"model":`, strings.Repeat("x", maxResponseBytes+1), `{"model":"` + OpenAIModel + `","data":[{"index":0,"embedding":[1e100]}]}`} {
		p := testOpenAI(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, raw) })
		if _, err := p.Embed(context.Background(), []string{"text"}); !errors.Is(err, ErrResponse) {
			t.Fatal(err)
		}
	}
}

func TestOpenAIStatusErrorsDoNotLeakOrRetry(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{400, ErrRejected}, {401, ErrRejected}, {403, ErrRejected}, {408, ErrTimeout}, {429, ErrUnavailable}, {500, ErrUnavailable}, {503, ErrUnavailable}, {504, ErrTimeout},
	} {
		var calls atomic.Int32
		p := testOpenAI(t, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, "test-secret-key private-document")
		})
		_, err := p.Embed(context.Background(), []string{"private-document"})
		if !errors.Is(err, tc.want) || calls.Load() != 1 || strings.Contains(err.Error(), "test-secret-key") || strings.Contains(err.Error(), "private-document") {
			t.Fatalf("status %d: %v calls=%d", tc.status, err, calls.Load())
		}
	}
}

func TestOpenAITimeoutCancellationAndRedirect(t *testing.T) {
	t.Run("HTTP timeout", func(t *testing.T) {
		p := testOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
		})
		p.client.Timeout = 30 * time.Millisecond
		if _, err := p.Embed(context.Background(), []string{"text"}); !errors.Is(err, ErrTimeout) {
			t.Fatal(err)
		}
	})
	t.Run("body timeout", func(t *testing.T) {
		p := testOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})
		p.client.Timeout = 30 * time.Millisecond
		if _, err := p.Embed(context.Background(), []string{"text"}); !errors.Is(err, ErrTimeout) {
			t.Fatal(err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		entered := make(chan struct{})
		p := testOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			close(entered)
			<-r.Context().Done()
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := p.Embed(ctx, []string{"text"}); done <- err }()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request did not start")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation blocked")
		}
	})
	t.Run("redirect", func(t *testing.T) {
		var leaked atomic.Bool
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
		defer target.Close()
		p := testOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		})
		if _, err := p.Embed(context.Background(), []string{"text"}); !errors.Is(err, ErrRejected) || leaked.Load() {
			t.Fatal("redirect followed", err)
		}
	})
}
