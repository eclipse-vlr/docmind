package postgres

import (
	"context"
	"errors"
	"time"

	"docmind/internal/documents"
	"docmind/internal/ingestion"
	"docmind/internal/llm"
	"github.com/jackc/pgx/v5"
)

// ClaimJob locks the document first, matching Delete and finalization. A claim's
// increasing attempt number fences writes from an expired worker. No DB locks
// or connections are held while reading files or invoking the PDF process.
func (s *Store) ClaimJob(ctx context.Context, lease time.Duration, maxAttempts int) (ingestion.Job, error) {
	if lease <= 0 || lease > time.Hour || maxAttempts < 1 || maxAttempts > 10 {
		return ingestion.Job{}, errors.New("invalid claim settings")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ingestion.Job{}, err
	}
	defer rollback(tx)
	var job ingestion.Job
	err = tx.QueryRow(ctx, `SELECT j.id::text, d.id::text, d.storage_key, d.mime_type
		FROM documents d JOIN ingestion_jobs j ON j.document_id=d.id
		WHERE (j.status='queued' AND j.next_run_at <= clock_timestamp())
		   OR (j.status='processing' AND j.lease_until <= clock_timestamp())
		ORDER BY j.next_run_at, j.id LIMIT 1 FOR UPDATE OF d SKIP LOCKED`).Scan(&job.ID, &job.DocumentID, &job.StorageKey, &job.MIMEType)
	if errors.Is(err, pgx.ErrNoRows) {
		return job, ingestion.ErrNoJob
	}
	if err != nil {
		return job, err
	}
	err = tx.QueryRow(ctx, `SELECT attempts FROM ingestion_jobs WHERE id=$1 AND
		((status='queued' AND next_run_at<=clock_timestamp()) OR
		 (status='processing' AND lease_until<=clock_timestamp())) FOR UPDATE`, job.ID).Scan(&job.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return job, ingestion.ErrNoJob
	}
	if err != nil {
		return job, err
	}
	if job.Attempt >= maxAttempts {
		message := "Исчерпаны попытки обработки документа."
		if _, err := tx.Exec(ctx, "UPDATE ingestion_jobs SET status='failed', lease_until=NULL, error_message=$2, updated_at=clock_timestamp() WHERE id=$1", job.ID, message); err != nil {
			return job, err
		}
		if _, err := tx.Exec(ctx, "UPDATE documents SET status='failed', error_message=$2, updated_at=clock_timestamp() WHERE id=$1", job.DocumentID, message); err != nil {
			return job, err
		}
		if err := commit(ctx, tx); err != nil {
			return job, err
		}
		return ingestion.Job{}, ingestion.ErrNoJob
	}
	err = tx.QueryRow(ctx, `UPDATE ingestion_jobs SET status='processing', attempts=attempts+1,
		lease_until=clock_timestamp()+$2*interval '1 microsecond', error_message='', updated_at=clock_timestamp()
		WHERE id=$1 RETURNING attempts,generation`, job.ID, lease.Microseconds()).Scan(&job.Attempt, &job.Generation)
	if err != nil {
		return job, err
	}
	if _, err := tx.Exec(ctx, "UPDATE documents SET status='processing', embedding_model='', error_message='', updated_at=clock_timestamp() WHERE id=$1", job.DocumentID); err != nil {
		return job, err
	}
	if err := commit(ctx, tx); err != nil {
		return job, err
	}
	return job, nil
}

func (s *Store) lockJob(ctx context.Context, tx pgx.Tx, job ingestion.Job) error {
	var id string
	err := tx.QueryRow(ctx, "SELECT id::text FROM documents WHERE id=$1 FOR UPDATE", job.DocumentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.ErrLeaseLost
	}
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `SELECT id::text FROM ingestion_jobs WHERE id=$1 AND document_id=$2
		AND status='processing' AND attempts=$3 AND generation=$4 AND lease_until>clock_timestamp() FOR UPDATE`, job.ID, job.DocumentID, job.Attempt, job.Generation).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.ErrLeaseLost
	}
	return err
}

func (s *Store) CompleteJob(ctx context.Context, job ingestion.Job, chunks []ingestion.Chunk) error {
	if len(chunks) == 0 || len(chunks) > ingestion.MaxChunks {
		return ingestion.ErrLimit
	}
	if job.Model == "" || len(job.Model) > 128 {
		return llm.ErrResponse
	}
	for _, chunk := range chunks {
		if err := llm.ValidateVector(chunk.Embedding); err != nil {
			return err
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := s.lockJob(ctx, tx, job); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM document_chunks WHERE document_id=$1", job.DocumentID); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for i, chunk := range chunks {
		batch.Queue(`INSERT INTO document_chunks(id,document_id,ordinal,page_number,content,embedding)
			VALUES ($1,$2,$3,$4,$5,$6::text::public.vector)`, documents.NewID(), job.DocumentID, i, chunk.Page, chunk.Text, vectorString(chunk.Embedding))
	}
	results := tx.SendBatch(ctx, batch)
	for range chunks {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return err
		}
	}
	if err := results.Close(); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET status='completed', lease_until=NULL, error_message='', updated_at=clock_timestamp()
		WHERE id=$1 AND lease_until>clock_timestamp()`, job.ID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ingestion.ErrLeaseLost
	}
	if _, err := tx.Exec(ctx, "UPDATE documents SET status='ready', embedding_model=$2, error_message='', updated_at=clock_timestamp() WHERE id=$1", job.DocumentID, job.Model); err != nil {
		return err
	}
	return commit(ctx, tx)
}

func (s *Store) FailJob(ctx context.Context, job ingestion.Job, message string, permanent bool, maxAttempts int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := s.lockJob(ctx, tx, job); err != nil {
		return err
	}
	status, docStatus := "queued", "queued"
	if permanent || job.Attempt >= maxAttempts {
		status, docStatus = "failed", "failed"
	}
	backoff := time.Second << min(max(job.Attempt-1, 0), 5)
	result, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET status=$2, lease_until=NULL, error_message=$3,
		next_run_at=clock_timestamp()+$4*interval '1 microsecond', updated_at=clock_timestamp()
		WHERE id=$1 AND lease_until>clock_timestamp()`, job.ID, status, message, backoff.Microseconds())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ingestion.ErrLeaseLost
	}
	if _, err := tx.Exec(ctx, "UPDATE documents SET status=$2, error_message=$3, updated_at=clock_timestamp() WHERE id=$1", job.DocumentID, docStatus, message); err != nil {
		return err
	}
	return commit(ctx, tx)
}
