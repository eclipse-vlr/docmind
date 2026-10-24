package ingestion

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"docmind/internal/documents"
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
	repo     Repository
	files    Files
	parser   Parser
	settings Settings
	logger   *slog.Logger
}

func NewWorker(repo Repository, files Files, parser Parser, settings Settings, logger *slog.Logger) (*Worker, error) {
	if settings.PollInterval <= 0 || settings.PollInterval > time.Hour || settings.JobTimeout <= 0 || settings.JobTimeout > time.Hour || settings.DBTimeout <= 0 || settings.DBTimeout > time.Hour || settings.LeaseDuration > time.Hour || settings.LeaseDuration <= settings.JobTimeout+2*settings.DBTimeout || settings.MaxAttempts < 1 || settings.MaxAttempts > 10 {
		return nil, errors.New("invalid worker settings")
	}
	return &Worker{repo: repo, files: files, parser: parser, settings: settings, logger: logger}, nil
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
	cancelJob()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	finishCtx, cancelFinish := context.WithTimeout(ctx, w.settings.DBTimeout)
	defer cancelFinish()
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
	}
	if err := w.repo.FailJob(finishCtx, job, message, permanent, w.settings.MaxAttempts); err != nil {
		return err
	}
	w.logger.Warn("ingestion_attempt_failed", "document_id", job.DocumentID, "attempt", job.Attempt, "permanent", permanent)
	return nil
}
