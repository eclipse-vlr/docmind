package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"docmind/internal/documents"
	"docmind/internal/ingestion"
)

func queuedDocument(t *testing.T, f *fixture, text string) documents.Document {
	t.Helper()
	d, err := f.service.Upload(context.Background(), "document.txt", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func claim(t *testing.T, f *fixture) ingestion.Job {
	t.Helper()
	j, err := f.repo.ClaimJob(context.Background(), time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func expire(t *testing.T, f *fixture, job ingestion.Job) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), "UPDATE ingestion_jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
}

func worker(t *testing.T, f *fixture, parser ingestion.Parser) *ingestion.Worker {
	t.Helper()
	w, err := ingestion.NewWorker(f.repo, f.files, parser, ingestion.Settings{
		PollInterval: time.Second, JobTimeout: 30 * time.Second, LeaseDuration: time.Minute, DBTimeout: 3 * time.Second, MaxAttempts: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestIngestionLifecycle(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := queuedDocument(t, f, strings.Repeat("содержимое ", 300))
	if err := worker(t, f, ingestion.TextParser{}).Step(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := f.service.Get(ctx, d.ID)
	if err != nil || stored.Status != "ready" {
		t.Fatalf("document=%+v error=%v", stored, err)
	}
	var chunks, distinct, minOrdinal int
	if err := f.pool.QueryRow(ctx, "SELECT count(*),count(DISTINCT ordinal),min(ordinal) FROM document_chunks WHERE document_id=$1", d.ID).Scan(&chunks, &distinct, &minOrdinal); err != nil || chunks < 2 || chunks != distinct || minOrdinal != 0 {
		t.Fatalf("chunks=%d distinct=%d min=%d error=%v", chunks, distinct, minOrdinal, err)
	}
	if err := worker(t, f, ingestion.TextParser{}).Step(ctx); !errors.Is(err, ingestion.ErrNoJob) {
		t.Fatalf("completed job reclaimed: %v", err)
	}
	if err := f.service.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM document_chunks WHERE document_id=$1", d.ID).Scan(&chunks); err != nil || chunks != 0 {
		t.Fatal("chunks survived deletion", err)
	}
}

func TestConcurrentIngestionClaims(t *testing.T) {
	f := setup(t)
	for range 12 {
		queuedDocument(t, f, "text")
	}
	type result struct {
		j   ingestion.Job
		err error
	}
	results := make(chan result, 2)
	seen := make(map[string]bool)
	for range 6 {
		start := make(chan struct{})
		for range 2 {
			go func() {
				<-start
				j, err := f.repo.ClaimJob(context.Background(), time.Minute, 3)
				results <- result{j, err}
			}()
		}
		close(start)
		for range 2 {
			r := <-results
			if r.err != nil {
				t.Fatal(r.err)
			}
			if seen[r.j.ID] {
				t.Fatal("workers claimed same unexpired job")
			}
			seen[r.j.ID] = true
		}
	}
	if _, err := f.repo.ClaimJob(context.Background(), time.Minute, 3); !errors.Is(err, ingestion.ErrNoJob) {
		t.Fatal("unexpired job reclaimed", err)
	}
}

func TestLeaseRecoveryAndFencing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := queuedDocument(t, f, "text")
	old := claim(t, f)
	expire(t, f, old)
	if err := f.repo.CompleteJob(ctx, old, []ingestion.Chunk{{Text: "stale"}}); !errors.Is(err, ingestion.ErrLeaseLost) {
		t.Fatal("expired owner committed", err)
	}
	current := claim(t, f)
	if current.Attempt != old.Attempt+1 {
		t.Fatal("attempt did not advance")
	}
	if err := f.repo.FailJob(ctx, old, "stale error", true, 3); !errors.Is(err, ingestion.ErrLeaseLost) {
		t.Fatal("stale failure changed document", err)
	}
	if err := f.repo.CompleteJob(ctx, current, []ingestion.Chunk{{Text: "current"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.CompleteJob(ctx, current, []ingestion.Chunk{{Text: "duplicate"}}); !errors.Is(err, ingestion.ErrLeaseLost) {
		t.Fatal("duplicate completion accepted", err)
	}
	var text string
	if err := f.pool.QueryRow(ctx, "SELECT content FROM document_chunks WHERE document_id=$1", d.ID).Scan(&text); err != nil || text != "current" {
		t.Fatal("recovered content incorrect", text, err)
	}
}

func TestRetriesAndCrashExhaustion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := queuedDocument(t, f, "text")
	for attempt := 1; attempt <= 3; attempt++ {
		j := claim(t, f)
		if j.Attempt != attempt {
			t.Fatalf("attempt=%d want %d", j.Attempt, attempt)
		}
		if attempt == 3 {
			expire(t, f, j)
			break
		}
		if err := f.repo.FailJob(ctx, j, "temporary failure", false, 3); err != nil {
			t.Fatal(err)
		}
		var delayed bool
		if err := f.pool.QueryRow(ctx, "SELECT next_run_at>updated_at FROM ingestion_jobs WHERE id=$1", j.ID).Scan(&delayed); err != nil || !delayed {
			t.Fatal("retry has no backoff", err)
		}
		if _, err := f.repo.ClaimJob(ctx, time.Minute, 3); !errors.Is(err, ingestion.ErrNoJob) {
			t.Fatal("backoff ignored", err)
		}
		if _, err := f.pool.Exec(ctx, "UPDATE ingestion_jobs SET next_run_at=clock_timestamp()-interval '1 second' WHERE id=$1", j.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.repo.ClaimJob(ctx, time.Minute, 3); !errors.Is(err, ingestion.ErrNoJob) {
		t.Fatal(err)
	}
	stored, err := f.service.Get(ctx, d.ID)
	if err != nil || stored.Status != "failed" {
		t.Fatal("crashed final attempt not failed", stored, err)
	}
}

func TestFinalizationRollback(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := queuedDocument(t, f, "text")
	j := claim(t, f)
	if err := f.repo.CompleteJob(ctx, j, []ingestion.Chunk{{Text: "valid"}, {Text: ""}}); err == nil {
		t.Fatal("expected invalid chunk rejection")
	}
	var n int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM document_chunks WHERE document_id=$1", d.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("partial chunks committed", err)
	}
	stored, err := f.service.Get(ctx, d.ID)
	if err != nil || stored.Status != "processing" {
		t.Fatal("failed finalization published ready", stored, err)
	}
	if err := f.repo.CompleteJob(ctx, j, []ingestion.Chunk{{Text: "valid"}}); err != nil {
		t.Fatal("retry after rollback failed", err)
	}
}

func TestDeletionDuringIngestion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := queuedDocument(t, f, "text")
	j := claim(t, f)
	start := make(chan struct{})
	completed := make(chan error, 1)
	deleted := make(chan error, 1)
	go func() { <-start; completed <- f.repo.CompleteJob(ctx, j, []ingestion.Chunk{{Text: "text"}}) }()
	go func() { <-start; deleted <- f.service.Delete(ctx, d.ID) }()
	close(start)
	cErr, dErr := <-completed, <-deleted
	if cErr != nil && !errors.Is(cErr, ingestion.ErrLeaseLost) {
		t.Fatal(cErr)
	}
	if dErr != nil {
		t.Fatal(dErr)
	}
	if err := f.repo.CompleteJob(ctx, j, []ingestion.Chunk{{Text: "resurrect"}}); !errors.Is(err, ingestion.ErrLeaseLost) {
		t.Fatal("deleted document resurrected", err)
	}
	var n int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM document_chunks WHERE document_id=$1", d.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("orphan chunks", err)
	}
}

type parserFunc func(context.Context, string, []byte) ([]ingestion.Chunk, error)

func (p parserFunc) Parse(ctx context.Context, mime string, data []byte) ([]ingestion.Chunk, error) {
	return p(ctx, mime, data)
}

func TestWorkerShutdownLeavesRecoverableLease(t *testing.T) {
	f := setup(t)
	d := queuedDocument(t, f, "text")
	entered := make(chan struct{})
	w := worker(t, f, parserFunc(func(ctx context.Context, _ string, _ []byte) ([]ingestion.Chunk, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop")
	}
	var j ingestion.Job
	if err := f.pool.QueryRow(context.Background(), "SELECT id::text FROM ingestion_jobs WHERE document_id=$1 AND status='processing'", d.ID).Scan(&j.ID); err != nil {
		t.Fatal("shutdown acknowledged work", err)
	}
	expire(t, f, j)
	if err := worker(t, f, ingestion.TextParser{}).Step(context.Background()); err != nil {
		t.Fatal("replacement worker failed", err)
	}
}

func TestWorkerPermanentFailure(t *testing.T) {
	f := setup(t)
	d := queuedDocument(t, f, " \n\t")
	if err := worker(t, f, ingestion.TextParser{}).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := f.service.Get(context.Background(), d.ID)
	if err != nil || stored.Status != "failed" || stored.ErrorMessage == "" {
		t.Fatal("empty document was not failed", stored, err)
	}
}
