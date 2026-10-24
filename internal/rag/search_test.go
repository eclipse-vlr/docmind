package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"docmind/internal/documents"
	"docmind/internal/llm"
)

type repositoryStub struct {
	checkErr, searchErr error
	checks, searches    int
}

func (r *repositoryStub) CheckDocuments(context.Context, []string, string) error {
	r.checks++
	return r.checkErr
}
func (r *repositoryStub) SearchChunks(context.Context, []string, string, []float32, int) ([]Match, error) {
	r.searches++
	return nil, r.searchErr
}

type providerStub struct {
	calls int
	embed func(context.Context, []string) ([][]float32, error)
}

func (*providerStub) Model() string { return "test-model" }
func (p *providerStub) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	p.calls++
	return p.embed(ctx, texts)
}

func TestSearchValidationBeforeProvider(t *testing.T) {
	id := documents.NewID()
	for _, tc := range []struct {
		question string
		ids      []string
		k        int
	}{
		{"", []string{id}, 5}, {"\xff", []string{id}, 5}, {strings.Repeat("x", llm.MaxInputBytes+1), []string{id}, 5},
		{"text", nil, 5}, {"text", []string{"invalid"}, 5}, {"text", []string{id, id}, 5}, {"text", []string{id}, 0}, {"text", []string{id}, MaxTopK + 1},
	} {
		r, p := &repositoryStub{}, &providerStub{}
		if _, err := NewSearcher(r, p).Search(context.Background(), tc.question, tc.ids, tc.k); !errors.Is(err, ErrInvalid) || r.checks != 0 || p.calls != 0 {
			t.Fatal("validation spent a request", err)
		}
	}
	for _, problem := range []error{documents.ErrNotFound, ErrNotReady, ErrModelMismatch, context.Canceled, errors.New("private SQL diagnostics")} {
		r, p := &repositoryStub{checkErr: problem}, &providerStub{}
		want := problem
		if problem.Error() == "private SQL diagnostics" {
			want = ErrUnavailable
		}
		if _, err := NewSearcher(r, p).Search(context.Background(), "text", []string{id}, 5); !errors.Is(err, want) || p.calls != 0 || r.searches != 0 {
			t.Fatal(err)
		}
	}
}

func TestSearchRechecksAfterEmbedding(t *testing.T) {
	for _, problem := range []error{documents.ErrNotFound, ErrNotReady, ErrModelMismatch} {
		r := &repositoryStub{searchErr: problem}
		p := &providerStub{embed: func(ctx context.Context, texts []string) ([][]float32, error) {
			if len(texts) != 1 || texts[0] != "query" {
				t.Error("invalid provider input")
			}
			return (llm.Fake{}).Embed(ctx, texts)
		}}
		if _, err := NewSearcher(r, p).Search(context.Background(), " query ", []string{documents.NewID()}, 5); !errors.Is(err, problem) || r.searches != 1 || p.calls != 1 {
			t.Fatal(err)
		}
	}
}

func TestSearchRejectsInvalidProviderVector(t *testing.T) {
	for _, vectors := range [][][]float32{nil, {nil}, {make([]float32, llm.Dimensions)}} {
		r := &repositoryStub{}
		p := &providerStub{embed: func(context.Context, []string) ([][]float32, error) { return vectors, nil }}
		if _, err := NewSearcher(r, p).Search(context.Background(), "query", []string{documents.NewID()}, 1); !errors.Is(err, llm.ErrResponse) || r.searches != 0 {
			t.Fatal(err)
		}
	}
}
