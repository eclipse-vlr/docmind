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

func yandexVector(value float32) []float32 { v := make([]float32, Dimensions); v[0] = value; return v }
func testYandexEmbeddings(t *testing.T, purpose EmbeddingPurpose, handler http.HandlerFunc) *YandexEmbeddings {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p, err := NewYandexEmbeddings("test-secret-key", "test-folder", purpose, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	p.endpoint = srv.URL
	return p
}
func TestYandexEmbeddingRolesAndOrder(t *testing.T) {
	for _, purpose := range []EmbeddingPurpose{DocumentEmbedding, QueryEmbedding} {
		t.Run(string(purpose), func(t *testing.T) {
			var calls atomic.Int32
			p := testYandexEmbeddings(t, purpose, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Api-Key test-secret-key" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("x-folder-id") != "test-folder" {
					t.Error("wrong method or authentication")
				}
				var body struct {
					Model      string `json:"model"`
					Input      string `json:"input"`
					Dimensions int    `json:"dimensions"`
					Encoding   string `json:"encoding_format"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "emb://test-folder/text-embeddings-v2-"+string(purpose)+"/" || body.Dimensions != 256 || body.Encoding != "float" {
					t.Error("invalid embedding request")
				}
				n := calls.Add(1)
				want := []string{"first", "second"}
				if int(n) > len(want) || body.Input != want[n-1] {
					t.Error("input order lost")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"model": body.Model, "data": []any{map[string]any{"index": 0, "embedding": yandexVector(float32(n))}}})
			})
			out, err := p.Embed(context.Background(), []string{"first", "second"})
			if err != nil || len(out) != 2 || out[0][0] != 1 || out[1][0] != 2 || calls.Load() != 2 {
				t.Fatalf("out=%v err=%v calls=%d", len(out), err, calls.Load())
			}
		})
	}
	doc, _ := NewYandexEmbeddings("key", "folder", DocumentEmbedding, time.Second)
	query, _ := NewYandexEmbeddings("key", "folder", QueryEmbedding, time.Second)
	if doc.Model() != query.Model() || doc.Model() == (Fake{}).Model() {
		t.Fatal("paired encoders must share a distinct vector space")
	}
}
func TestYandexRejectsInvalidEmbeddingResponses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model string
		data  []any
	}{
		{"missing vector", "emb://test-folder/text-embeddings-v2-doc/", nil},
		{"wrong model", "emb://test-folder/text-embeddings-v2-query/", []any{map[string]any{"index": 0, "embedding": yandexVector(1)}}},
		{"missing index", "emb://test-folder/text-embeddings-v2-doc/", []any{map[string]any{"embedding": yandexVector(1)}}},
		{"nonzero index", "emb://test-folder/text-embeddings-v2-doc/", []any{map[string]any{"index": 1, "embedding": yandexVector(1)}}},
		{"zero vector", "emb://test-folder/text-embeddings-v2-doc/", []any{map[string]any{"index": 0, "embedding": yandexVector(0)}}},
		{"wrong dimensions", "emb://test-folder/text-embeddings-v2-doc/", []any{map[string]any{"index": 0, "embedding": []float32{1}}}},
		{"duplicate vectors", "emb://test-folder/text-embeddings-v2-doc/", []any{map[string]any{"index": 0, "embedding": yandexVector(1)}, map[string]any{"index": 0, "embedding": yandexVector(1)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testYandexEmbeddings(t, DocumentEmbedding, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"model": tc.model, "data": tc.data})
			})
			if _, err := p.Embed(context.Background(), []string{"text"}); !errors.Is(err, ErrResponse) {
				t.Fatal(err)
			}
		})
	}
	for _, raw := range []string{`{`, "\xff", strings.Repeat("x", maxEmbeddingResponseBytes+1), `{"model":"emb://test-folder/text-embeddings-v2-doc/","data":[{"index":0,"embedding":[1e100]}]}`} {
		p := testYandexEmbeddings(t, DocumentEmbedding, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, raw) })
		if _, err := p.Embed(context.Background(), []string{"text"}); !errors.Is(err, ErrResponse) {
			t.Fatal(err)
		}
	}
	nullVector := make([]any, Dimensions)
	for i := range nullVector {
		nullVector[i] = float32(0)
	}
	nullVector[0] = float32(1)
	nullVector[1] = nil
	p := testYandexEmbeddings(t, DocumentEmbedding, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "emb://test-folder/text-embeddings-v2-doc/", "data": []any{map[string]any{"index": 0, "embedding": nullVector}}})
	})
	if _, err := p.Embed(context.Background(), []string{"text"}); !errors.Is(err, ErrResponse) {
		t.Fatal("null coordinate accepted", err)
	}
}
func TestYandexEmbeddingFailureStopsBatchWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{{400, ErrRejected}, {401, ErrRejected}, {403, ErrRejected}, {408, ErrTimeout}, {429, ErrUnavailable}, {500, ErrUnavailable}, {503, ErrUnavailable}, {504, ErrTimeout}} {
		var calls atomic.Int32
		p := testYandexEmbeddings(t, DocumentEmbedding, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, "test-secret-key private document")
		})
		_, err := p.Embed(context.Background(), []string{"first", "second"})
		if !errors.Is(err, tc.want) || calls.Load() != 1 || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "test-secret") {
			t.Fatal(tc.status, err, calls.Load())
		}
	}
}
func TestYandexEmbeddingTimeoutCancellationAndRedirect(t *testing.T) {
	for _, flush := range []bool{false, true} {
		p := testYandexEmbeddings(t, DocumentEmbedding, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			if flush {
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
			}
			<-r.Context().Done()
		})
		p.client.Timeout = 30 * time.Millisecond
		if _, err := p.Embed(context.Background(), []string{"text"}); !errors.Is(err, ErrTimeout) {
			t.Fatal(err)
		}
	}
	entered := make(chan struct{})
	p := testYandexEmbeddings(t, DocumentEmbedding, func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.Embed(ctx, []string{"text", "second"}); done <- err }()
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
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
	defer target.Close()
	p = testYandexEmbeddings(t, DocumentEmbedding, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	if _, err := p.Embed(context.Background(), []string{"text"}); !errors.Is(err, ErrRejected) || redirected.Load() {
		t.Fatal("redirect followed", err)
	}
}
func TestYandexConfigurationRejectsInvalidCredentials(t *testing.T) {
	for _, tc := range []struct {
		key, folder string
		timeout     time.Duration
	}{
		{"", "folder", time.Second}, {"key\r\nprivate", "folder", time.Second}, {"key", "", time.Second}, {"key", "folder/other", time.Second}, {"key", "folder\r\nprivate", time.Second}, {"key", "folder", 0}, {"key", "folder", 2 * time.Minute},
	} {
		if _, err := NewYandexEmbeddings(tc.key, tc.folder, DocumentEmbedding, tc.timeout); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe configuration", err)
		}
		if _, err := NewYandexChat(tc.key, tc.folder, tc.timeout); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe chat configuration", err)
		}
	}
	if _, err := NewYandexEmbeddings("key", "folder", EmbeddingPurpose("invalid"), time.Second); err == nil {
		t.Fatal("invalid encoder role accepted")
	}
}
