package ingestion

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"docmind/internal/documents"
	"docmind/internal/llm"
	"docmind/internal/usage"
)

type Settings struct {
	PollInterval  time.Duration
	JobTimeout    time.Duration
	LeaseDuration time.Duration
	DBTimeout     time.Duration
	MaxAttempts   int
}

// Worker processes one job at a time. Run owns its loop; cancellation stops
// parsing and leaves unacknowledged work recoverable after the persisted lease.
type Worker struct {
	repo       Repository
	files      Files
	parser     Parser
	embeddings llm.EmbeddingProvider
	settings   Settings
	logger     *slog.Logger
}

func NewWorker(repo Repository, files Files, parser Parser, embeddings llm.EmbeddingProvider, settings Settings, logger *slog.Logger) (*Worker, error) {
	if settings.PollInterval <= 0 || settings.PollInterval > time.Hour || settings.JobTimeout <= 0 || settings.JobTimeout > time.Hour || settings.DBTimeout <= 0 || settings.DBTimeout > time.Hour || settings.LeaseDuration > time.Hour || settings.LeaseDuration <= settings.JobTimeout+2*settings.DBTimeout || settings.MaxAttempts < 1 || settings.MaxAttempts > 10 {
		return nil, errors.New("invalid worker settings")
	}
	if embeddings == nil || embeddings.Model() == "" {
		return nil, errors.New("embedding provider is required")
	}
	return &Worker{repo: repo, files: files, parser: parser, embeddings: embeddings, settings: settings, logger: logger}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		err := w.Step(ctx)
		if ctx.Err() != nil {
			break
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, ErrNoJob) && !errors.Is(err, ErrLeaseLost) {
			w.logger.Warn("ingestion_operation_failed")
		}
		timer := time.NewTimer(w.settings.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

// Step is also used by integration tests and one-shot maintenance callers.
func (w *Worker) Step(ctx context.Context) error {
	claimCtx, cancel := context.WithTimeout(ctx, w.settings.DBTimeout)
	job, err := w.repo.ClaimJob(claimCtx, w.settings.LeaseDuration, w.settings.MaxAttempts)
	cancel()
	if err != nil {
		return err
	}
	jobCtx, cancelJob := context.WithTimeout(ctx, w.settings.JobTimeout)
	data, err := w.files.Read(jobCtx, job.StorageKey)
	var chunks []Chunk
	if err == nil {
		chunks, err = w.parser.Parse(jobCtx, job.MIMEType, data)
	}
	job.Model = w.embeddings.Model()
	if err == nil {
		err = w.embedResumable(jobCtx, job, chunks)
	}
	cancelJob()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	finishCtx, cancelFinish := context.WithTimeout(ctx, w.settings.DBTimeout)
	defer cancelFinish()
	if errors.Is(err, ErrProgress) {
		return w.repo.PauseForProgress(finishCtx, job)
	}
	if errors.Is(err, usage.ErrBudget) {
		if paused, ok := w.repo.(interface {
			PauseForBudget(context.Context, Job) error
		}); ok {
			return paused.PauseForBudget(finishCtx, job)
		}
	}
	if err == nil {
		if err := w.repo.CompleteJob(finishCtx, job, chunks); err != nil {
			return err
		}
		w.logger.Info("ingestion_completed", "document_id", job.DocumentID, "attempt", job.Attempt, "chunks", len(chunks))
		return nil
	}
	message := "Не удалось обработать документ; повторите загрузку позже."
	permanent := false
	switch {
	case errors.Is(err, ErrEmpty):
		message, permanent = "Документ не содержит текста; OCR не поддерживается.", true
	case errors.Is(err, ErrLimit), errors.Is(err, documents.ErrTooLarge):
		message, permanent = "Документ превышает ограничения извлечения текста.", true
	case errors.Is(err, ErrInvalid):
		message, permanent = "Не удалось прочитать текст документа.", true
	case errors.Is(err, llm.ErrRejected), errors.Is(err, llm.ErrInput):
		message, permanent = "Провайдер embeddings отклонил запрос; проверьте настройки.", true
	}
	if err := w.repo.FailJob(finishCtx, job, message, permanent, w.settings.MaxAttempts); err != nil {
		return err
	}
	w.logger.Warn("ingestion_attempt_failed", "document_id", job.DocumentID, "attempt", job.Attempt, "permanent", permanent)
	return nil
}

// Each provider success is persisted before the next paid request. Checkpoint
// access also verifies ownership, so deletion or a newer claim stops this loop.
func (w *Worker) embedResumable(ctx context.Context, job Job, chunks []Chunk) (result error) {
	saved := 0
	defer func() {
		if saved > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result = ErrProgress
		}
	}()
	for i := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte("chunk-v1\x00"+chunks[i].Text)))
		dbCtx, cancel := context.WithTimeout(ctx, w.settings.DBTimeout)
		vector, err := w.repo.LoadEmbedding(dbCtx, job, hash)
		cancel()
		if err != nil {
			return err
		}
		if vector == nil {
			vectors, err := w.embeddings.Embed(ctx, []string{chunks[i].Text})
			if err != nil {
				return err
			}
			if len(vectors) != 1 {
				return llm.ErrResponse
			}
			vector = vectors[0]
			if err := llm.ValidateVector(vector); err != nil {
				return err
			}
			// Keep a successful result even if the job deadline expires just
			// after HTTP returns; the DB fence still rejects a stale owner.
			dbCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), w.settings.DBTimeout)
			err = w.repo.SaveEmbedding(dbCtx, job, hash, vector)
			cancel()
			if err != nil {
				return err
			}
			saved++
		}
		if err := llm.ValidateVector(vector); err != nil {
			return err
		}
		chunks[i].Embedding = vector
	}
	return ctx.Err()
}
