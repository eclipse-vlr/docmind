package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"docmind/internal/documents"
	"docmind/internal/llm"
)

type retrieveFunc func(context.Context, string, []string, int) ([]Match, error)

func (f retrieveFunc) Search(ctx context.Context, q string, ids []string, k int) ([]Match, error) {
	return f(ctx, q, ids, k)
}

type chatFunc func(context.Context, llm.ChatRequest) (llm.ChatResult, error)

func (f chatFunc) Chat(ctx context.Context, in llm.ChatRequest) (llm.ChatResult, error) {
	return f(ctx, in)
}

func TestQuestionCitationsComeFromRetrievedContext(t *testing.T) {
	id, chunkID := documents.NewID(), documents.NewID()
	page := 2
	retrieval := retrieveFunc(func(_ context.Context, q string, ids []string, k int) ([]Match, error) {
		if q != "question" || len(ids) != 1 || ids[0] != id || k != 5 {
			t.Fatal("wrong retrieval scope")
		}
		return []Match{{DocumentID: id, ChunkID: chunkID, Page: &page, Snippet: "Actual document text"}}, nil
	})
	chat := chatFunc(func(_ context.Context, in llm.ChatRequest) (llm.ChatResult, error) {
		if in.Question != "question" || len(in.Fragments) != 1 || in.Fragments[0].Text != "Actual document text" {
			t.Fatal("wrong context")
		}
		return llm.ChatResult{Answer: "Supported answer", SourceIDs: []string{in.Fragments[0].ID}}, nil
	})
	answer, err := NewQuestions(retrieval, chat).Ask(context.Background(), " question ", []string{id})
	if err != nil || answer.Answer != "Supported answer" || len(answer.Sources) != 1 {
		t.Fatalf("answer=%+v error=%v", answer, err)
	}
	s := answer.Sources[0]
	if s.DocumentID != id || s.ChunkID != chunkID || s.Page == nil || *s.Page != 2 || s.Snippet != "Actual document text" {
		t.Fatal("citation metadata not preserved", s)
	}
}

func TestQuestionInsufficientContext(t *testing.T) {
	id := documents.NewID()
	for _, empty := range []bool{false, true} {
		calls := 0
		retrieval := retrieveFunc(func(context.Context, string, []string, int) ([]Match, error) {
			if empty {
				return nil, nil
			}
			return []Match{{DocumentID: id, ChunkID: documents.NewID(), Snippet: "text"}}, nil
		})
		chat := chatFunc(func(context.Context, llm.ChatRequest) (llm.ChatResult, error) {
			calls++
			return llm.ChatResult{Answer: "ignore this generated text", InsufficientContext: true}, nil
		})
		answer, err := NewQuestions(retrieval, chat).Ask(context.Background(), "question", []string{id})
		if err != nil || answer.Answer != InsufficientAnswer || answer.Sources == nil || len(answer.Sources) != 0 {
			t.Fatal("insufficient answer invented sources", answer, err)
		}
		if empty && calls != 0 {
			t.Fatal("empty context spent a model request")
		}
	}
}

func TestQuestionRejectsUnsupportedCitations(t *testing.T) {
	id := documents.NewID()
	retrieval := retrieveFunc(func(context.Context, string, []string, int) ([]Match, error) {
		return []Match{{DocumentID: id, ChunkID: documents.NewID(), Snippet: "text"}}, nil
	})
	for _, result := range []llm.ChatResult{
		{Answer: "invented", SourceIDs: []string{"unknown"}},
		{Answer: "unsupported"},
		{Answer: "duplicated", SourceIDs: []string{"s1", "s1"}},
		{Answer: "contradictory", InsufficientContext: true, SourceIDs: []string{"s1"}},
	} {
		chat := chatFunc(func(context.Context, llm.ChatRequest) (llm.ChatResult, error) { return result, nil })
		if _, err := NewQuestions(retrieval, chat).Ask(context.Background(), "question", []string{id}); !errors.Is(err, llm.ErrResponse) {
			t.Fatal("invalid result published", err)
		}
	}
}

func TestQuestionContextBudget(t *testing.T) {
	id := documents.NewID()
	retrieval := retrieveFunc(func(context.Context, string, []string, int) ([]Match, error) {
		matches := make([]Match, 5)
		for i := range matches {
			matches[i] = Match{DocumentID: id, ChunkID: documents.NewID(), Snippet: strings.Repeat("я", 2000)}
		}
		return matches, nil
	})
	chat := chatFunc(func(_ context.Context, in llm.ChatRequest) (llm.ChatResult, error) {
		total := 0
		for _, f := range in.Fragments {
			total += len(f.Text)
		}
		if total > llm.MaxContextBytes || len(in.Fragments) != 4 {
			t.Fatal("context budget exceeded", total, len(in.Fragments))
		}
		return llm.ChatResult{Answer: "answer", SourceIDs: []string{"s4"}}, nil
	})
	if answer, err := NewQuestions(retrieval, chat).Ask(context.Background(), "question", []string{id}); err != nil || len(answer.Sources) != 1 {
		t.Fatal(answer, err)
	}
}

func TestQuestionValidationAndErrors(t *testing.T) {
	id := documents.NewID()
	for _, tc := range []struct {
		q   string
		ids []string
	}{
		{"", []string{id}}, {"\xff", []string{id}}, {strings.Repeat("x", llm.MaxInputBytes+1), []string{id}},
		{"question", nil}, {"question", []string{"invalid"}}, {"question", []string{id, id}},
	} {
		retrieval := retrieveFunc(func(context.Context, string, []string, int) ([]Match, error) {
			t.Fatal("invalid question reached retrieval")
			return nil, nil
		})
		if _, err := NewQuestions(retrieval, nil).Ask(context.Background(), tc.q, tc.ids); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	for _, problem := range []error{documents.ErrNotFound, ErrNotReady, ErrModelMismatch, ErrUnavailable, context.DeadlineExceeded} {
		retrieval := retrieveFunc(func(context.Context, string, []string, int) ([]Match, error) { return nil, problem })
		if _, err := NewQuestions(retrieval, nil).Ask(context.Background(), "question", []string{id}); !errors.Is(err, problem) {
			t.Fatal(err)
		}
	}
	retrieval := retrieveFunc(func(context.Context, string, []string, int) ([]Match, error) {
		return []Match{{DocumentID: id, ChunkID: documents.NewID(), Snippet: "text"}}, nil
	})
	for _, problem := range []error{llm.ErrTimeout, llm.ErrUnavailable, llm.ErrRejected, llm.ErrResponse, context.Canceled} {
		chat := chatFunc(func(context.Context, llm.ChatRequest) (llm.ChatResult, error) { return llm.ChatResult{}, problem })
		if _, err := NewQuestions(retrieval, chat).Ask(context.Background(), "question", []string{id}); !errors.Is(err, problem) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewQuestions(nil, nil).Ask(ctx, "question", []string{id}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestQuestionRejectsOutOfScopeRetrieval(t *testing.T) {
	id := documents.NewID()
	retrieval := retrieveFunc(func(context.Context, string, []string, int) ([]Match, error) {
		return []Match{{DocumentID: documents.NewID(), ChunkID: documents.NewID(), Snippet: "private"}}, nil
	})
	if _, err := NewQuestions(retrieval, nil).Ask(context.Background(), "question", []string{id}); !errors.Is(err, llm.ErrResponse) {
		t.Fatal("out-of-scope source accepted", err)
	}
}
