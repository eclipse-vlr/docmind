package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"docmind/internal/ingestion"
	"docmind/internal/storage/postgres"
	"docmind/internal/usage"
)

func TestUsageConcurrentBudgetAndSettlement(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	request := usage.Request{Kind: "chat", Reserved: 100, InputRate: 2, OutputRate: 3}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	ids := make(chan int64, 30)
	for range 30 {
		wg.Go(func() {
			id, err := f.repo.ReserveUsage(ctx, 1000, request)
			if err == nil {
				accepted.Add(1)
				ids <- id
			} else if !errors.Is(err, usage.ErrBudget) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	close(ids)
	if accepted.Load() != 10 {
		t.Fatal("overspending or lost admission", accepted.Load())
	}
	s, err := f.repo.UsageSnapshot(ctx, 1000)
	if err != nil || s.Reserved != 1000 || s.Calls != 10 {
		t.Fatal(s, err)
	}
	id := <-ids
	for range 2 {
		if err := f.repo.FinishUsage(ctx, id, &usage.Tokens{Input: 10, Output: 1}); err != nil {
			t.Fatal(err)
		}
	}
	s, _ = f.repo.UsageSnapshot(ctx, 1000)
	if s.Estimated != 23 || s.Reserved != 900 || s.Recorded != 1 {
		t.Fatal(s)
	}
	unknown := <-ids
	if err := f.repo.FinishUsage(ctx, unknown, nil); err != nil {
		t.Fatal(err)
	}
	s, _ = f.repo.UsageSnapshot(ctx, 1000)
	if s.Unknown != 1 || s.Reserved != 900 {
		t.Fatal("unknown charge released", s)
	}
	if _, err := f.repo.ReserveUsage(ctx, 1000, request); !errors.Is(err, usage.ErrBudget) {
		t.Fatal(err)
	}
	// A restart/new policy cannot raise today's allowance; lowering applies now.
	s, _ = f.repo.UsageSnapshot(ctx, 2000)
	if s.Limit != 1000 {
		t.Fatal(s)
	}
	s, _ = f.repo.UsageSnapshot(ctx, 500)
	if s.Limit != 500 {
		t.Fatal(s)
	}
}

type budgetOnlyEmbeddings struct{}

func (budgetOnlyEmbeddings) Model() string { return "budget-test" }
func (budgetOnlyEmbeddings) Embed(context.Context, []string) ([][]float32, error) {
	return nil, usage.ErrBudget
}

func TestWorkerWaitsForNextBudgetDay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := queuedDocument(t, f, "budget worker test")
	w, err := ingestion.NewWorker(f.repo, f.files, ingestion.TextParser{}, budgetOnlyEmbeddings{}, ingestion.Settings{PollInterval: time.Second, JobTimeout: 30 * time.Second, LeaseDuration: time.Minute, DBTimeout: 3 * time.Second, MaxAttempts: 3}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := f.service.Get(ctx, d.ID)
	if err != nil || stored.Status != "queued" || stored.ErrorMessage == "" {
		t.Fatal(stored, err)
	}
	var attempts int
	var deferred bool
	if err = f.pool.QueryRow(ctx, `SELECT attempts,next_run_at>clock_timestamp() FROM ingestion_jobs WHERE document_id=$1`, d.ID).Scan(&attempts, &deferred); err != nil || attempts != 0 || !deferred {
		t.Fatal(attempts, deferred, err)
	}
}

func TestUsagePersistsAcrossStoreReopenAndDocumentDeletion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := queuedDocument(t, f, "small text")
	id, err := f.repo.ReserveUsage(ctx, 100, usage.Request{Kind: "embedding_query", Reserved: 80, InputRate: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.repo.FinishUsage(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.service.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := postgres.Open(ctx, f.pool.Config().ConnString(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s, err := reopened.UsageSnapshot(ctx, 100)
	if err != nil || s.Reserved != 80 || s.Unknown != 1 {
		t.Fatal(s, err)
	}
	if _, err = reopened.ReserveUsage(ctx, 100, usage.Request{Kind: "embedding_doc", Reserved: 30, InputRate: 1}); !errors.Is(err, usage.ErrBudget) {
		t.Fatal("restart erased budget", err)
	}
}

func TestUsageUTCResetAndOverrun(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO ai_usage_days(day,limit_nanorub,reserved_nanorub) VALUES ((clock_timestamp() AT TIME ZONE 'UTC')::date-1,100,100)`); err != nil {
		t.Fatal(err)
	}
	id, err := f.repo.ReserveUsage(ctx, 100, usage.Request{Kind: "embedding_doc", Reserved: 50, InputRate: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.repo.FinishUsage(ctx, id, &usage.Tokens{Input: 6}); err != nil {
		t.Fatal(err)
	}
	s, err := f.repo.UsageSnapshot(ctx, 100)
	if err != nil || s.Estimated != 60 || s.Reserved != 0 || s.Overruns != 1 || s.EmbeddingTokens != 6 {
		t.Fatal(s, err)
	}
	if _, err = f.repo.ReserveUsage(ctx, 100, usage.Request{Kind: "chat", Reserved: 1, InputRate: 1}); !errors.Is(err, usage.ErrBudget) {
		t.Fatal(err)
	}
}

func TestBudgetPausePreservesAttemptsAndFencesOwner(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := queuedDocument(t, f, "budget test")
	job := claim(t, f)
	if err := f.repo.PauseForBudget(ctx, job); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var status string
	var future bool
	if err := f.pool.QueryRow(ctx, `SELECT attempts,status,next_run_at>clock_timestamp() FROM ingestion_jobs WHERE id=$1`, job.ID).Scan(&attempts, &status, &future); err != nil || attempts != 0 || status != "queued" || !future {
		t.Fatal(attempts, status, future, err)
	}
	if err := f.repo.FailJob(ctx, job, "stale", false, 3); !errors.Is(err, ingestion.ErrLeaseLost) {
		t.Fatal("stale owner accepted", err)
	}
	if _, err := f.repo.ClaimJob(ctx, time.Minute, 3); !errors.Is(err, ingestion.ErrNoJob) {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE ingestion_jobs SET next_run_at=clock_timestamp() WHERE document_id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	next := claim(t, f)
	if next.Attempt != job.Attempt || next.Generation <= job.Generation {
		t.Fatal(next, job)
	}
	if err := f.repo.FailJob(ctx, job, "stale", false, 3); !errors.Is(err, ingestion.ErrLeaseLost) {
		t.Fatal("old generation accepted", err)
	}
}
