package integration

import (
	"context"
	"docmind/internal/ingestion"
	"docmind/internal/llm"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
)

type checkpointProvider struct {
	model string
	call  func(context.Context, []string) ([][]float32, error)
}

func (p checkpointProvider) Model() string { return p.model }
func (p checkpointProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return p.call(ctx, texts)
}
func checkpointWorker(t *testing.T, f *fixture, parser ingestion.Parser, p llm.EmbeddingProvider, timeout time.Duration) *ingestion.Worker {
	t.Helper()
	w, err := ingestion.NewWorker(f.repo, f.files, parser, p, ingestion.Settings{PollInterval: time.Second, JobTimeout: timeout, LeaseDuration: time.Minute, DBTimeout: time.Second, MaxAttempts: 3}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestCheckpointResumeAndIndexGenerations(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	d := queuedDocument(t, f, "document")
	chunks := make([]ingestion.Chunk, 45)
	for i := range chunks {
		chunks[i].Text = fmt.Sprintf("unique chunk %d", i)
	}
	parser := parserFunc(func(context.Context, string, []byte) ([]ingestion.Chunk, error) {
		return append([]ingestion.Chunk(nil), chunks...), nil
	})
	calls := map[string]int{}
	first := true
	p := checkpointProvider{"revision-1", func(ctx context.Context, texts []string) ([][]float32, error) {
		if len(texts) != 1 {
			t.Fatal("provider successes must be checkpointed individually")
		}
		if first && len(calls) == 32 {
			first = false
			return nil, llm.ErrUnavailable
		}
		calls[texts[0]]++
		return (llm.Fake{}).Embed(ctx, texts)
	}}
	w := checkpointWorker(t, f, parser, p, time.Second*10)
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM document_chunks WHERE document_id=$1`, d.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("partial index published", err, n)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM embedding_checkpoints`).Scan(&n); err != nil || n != 32 {
		t.Fatal("successes not durable", err, n)
	}
	_, _ = f.pool.Exec(ctx, `UPDATE ingestion_jobs SET next_run_at=clock_timestamp()`)
	// A new Worker instance simulates process restart; only PostgreSQL survives.
	if err := checkpointWorker(t, f, parser, p, 10*time.Second).Step(ctx); err != nil {
		t.Fatal(err)
	}
	for text, count := range calls {
		if count != 1 {
			t.Fatalf("repaid %q %d times", text, count)
		}
	}
	if len(calls) != 45 {
		t.Fatal(calls)
	}
	if err := f.repo.RequeueDocument(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	for _, count := range calls {
		if count != 2 {
			t.Fatal("explicit reindex reused old generation")
		}
	}
	if err := f.service.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM embedding_checkpoints`).Scan(&n); err != nil || n != 0 {
		t.Fatal("orphan checkpoints", err, n)
	}
}

func TestCheckpointFencesDeletionAndModelChange(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	d := queuedDocument(t, f, "document")
	j := claim(t, f)
	j.Model = "revision-1"
	vectors, _ := (llm.Fake{}).Embed(ctx, []string{"text"})
	hash := fmt.Sprintf("%064d", 1)
	if err := f.repo.SaveEmbedding(ctx, j, hash, vectors[0]); err != nil {
		t.Fatal(err)
	}
	j.Model = "revision-2"
	if v, err := f.repo.LoadEmbedding(ctx, j, hash); err != nil || v != nil {
		t.Fatal("cross-model checkpoint reused", v, err)
	}
	expire(t, f, j)
	if _, err := f.repo.LoadEmbedding(ctx, j, hash); !errors.Is(err, ingestion.ErrLeaseLost) {
		t.Fatal(err)
	}
	if err := f.service.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.SaveEmbedding(ctx, j, hash, vectors[0]); !errors.Is(err, ingestion.ErrLeaseLost) {
		t.Fatal(err)
	}
}

func TestSlowJobYieldsWithoutLosingProgress(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	queuedDocument(t, f, "source")
	p := checkpointProvider{"model", func(ctx context.Context, texts []string) ([][]float32, error) {
		<-ctx.Done()
		return (llm.Fake{}).Embed(context.Background(), texts)
	}}
	if err := checkpointWorker(t, f, ingestion.TextParser{}, p, 50*time.Millisecond).Step(ctx); err != nil {
		t.Fatal(err)
	}
	var attempts, n int
	if err := f.pool.QueryRow(ctx, `SELECT attempts FROM ingestion_jobs`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatal(attempts, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM embedding_checkpoints`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	p.call = func(context.Context, []string) ([][]float32, error) {
		t.Fatal("successful text sent again")
		return nil, nil
	}
	if err := checkpointWorker(t, f, ingestion.TextParser{}, p, time.Second).Step(ctx); err != nil {
		t.Fatal(err)
	}
}
