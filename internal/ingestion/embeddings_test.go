package ingestion

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"docmind/internal/llm"
)

type embeddingFunc func(context.Context, []string) ([][]float32, error)

func (embeddingFunc) Model() string { return "test-model" }
func (f embeddingFunc) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

func TestWorkerEmbeddingBatchesPreserveChunks(t *testing.T) {
	chunks := make([]Chunk, llm.BatchSize*2+1)
	for i := range chunks {
		chunks[i].Text = fmt.Sprintf("chunk-%d", i)
	}
	var sizes []int
	seen := 0
	w := Worker{embeddings: embeddingFunc(func(_ context.Context, texts []string) ([][]float32, error) {
		sizes = append(sizes, len(texts))
		vectors := make([][]float32, len(texts))
		for i, text := range texts {
			if text != chunks[seen].Text {
				t.Fatal("input order changed")
			}
			vectors[i] = make([]float32, llm.Dimensions)
			vectors[i][0] = float32(seen + 1)
			seen++
		}
		return vectors, nil
	})}
	if err := w.embed(context.Background(), chunks); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sizes, []int{32, 32, 1}) {
		t.Fatal(sizes)
	}
	for i, c := range chunks {
		if c.Embedding[0] != float32(i+1) {
			t.Fatal("vector attached to wrong chunk")
		}
	}
}

func TestWorkerEmbeddingStopsOnProviderError(t *testing.T) {
	for _, problem := range []error{llm.ErrUnavailable, llm.ErrRejected, context.Canceled} {
		calls := 0
		w := Worker{embeddings: embeddingFunc(func(context.Context, []string) ([][]float32, error) { calls++; return nil, problem })}
		if err := w.embed(context.Background(), make([]Chunk, llm.BatchSize+1)); !errors.Is(err, problem) || calls != 1 {
			t.Fatal("worker continued after failure", err, calls)
		}
	}
	w := Worker{embeddings: embeddingFunc(func(context.Context, []string) ([][]float32, error) { return nil, nil })}
	if err := w.embed(context.Background(), []Chunk{{Text: "text"}}); !errors.Is(err, llm.ErrResponse) {
		t.Fatal(err)
	}
}
