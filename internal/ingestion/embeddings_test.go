package ingestion

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"docmind/internal/llm"
)

type embeddingFunc func(context.Context, []string) ([][]float32, error)

func (embeddingFunc) Model() string { return "test-model" }
func (f embeddingFunc) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

func TestWorkerEmbeddingCheckpointsPreserveChunkOrder(t *testing.T) {
	chunks := make([]Chunk, llm.BatchSize*2+1)
	for i := range chunks {
		chunks[i].Text = fmt.Sprintf("chunk-%d", i)
	}
	var sizes []int
	seen := 0
	w := Worker{repo: checkpointStub{}, settings: Settings{DBTimeout: time.Second}, embeddings: embeddingFunc(func(_ context.Context, texts []string) ([][]float32, error) {
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
	if err := w.embedResumable(context.Background(), Job{Model: "test-model"}, chunks); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != len(chunks) {
		t.Fatal(sizes)
	}
	for _, size := range sizes {
		if size != 1 {
			t.Fatal("success not independently checkpointed", sizes)
		}
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
		w := Worker{repo: checkpointStub{}, settings: Settings{DBTimeout: time.Second}, embeddings: embeddingFunc(func(context.Context, []string) ([][]float32, error) { calls++; return nil, problem })}
		if err := w.embedResumable(context.Background(), Job{Model: "test-model"}, make([]Chunk, llm.BatchSize+1)); !errors.Is(err, problem) || calls != 1 {
			t.Fatal("worker continued after failure", err, calls)
		}
	}
	w := Worker{repo: checkpointStub{}, settings: Settings{DBTimeout: time.Second}, embeddings: embeddingFunc(func(context.Context, []string) ([][]float32, error) { return nil, nil })}
	if err := w.embedResumable(context.Background(), Job{Model: "test-model"}, []Chunk{{Text: "text"}}); !errors.Is(err, llm.ErrResponse) {
		t.Fatal(err)
	}
}

// Persistence and fencing are exercised with PostgreSQL in integration tests.
type checkpointStub struct{}

func (checkpointStub) ClaimJob(context.Context, time.Duration, int) (Job, error) {
	return Job{}, ErrNoJob
}
func (checkpointStub) CompleteJob(context.Context, Job, []Chunk) error               { return nil }
func (checkpointStub) FailJob(context.Context, Job, string, bool, int) error         { return nil }
func (checkpointStub) LoadEmbedding(context.Context, Job, string) ([]float32, error) { return nil, nil }
func (checkpointStub) SaveEmbedding(context.Context, Job, string, []float32) error   { return nil }
func (checkpointStub) PauseForProgress(context.Context, Job) error                   { return nil }
