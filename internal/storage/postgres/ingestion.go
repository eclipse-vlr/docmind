package postgres

import (
	"context"
	"encoding/json"
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
		WHERE id=$1 RETURNING attempts,generation,index_generation`, job.ID, lease.Microseconds()).Scan(&job.Attempt, &job.Generation, &job.IndexGeneration)
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

// A read checks the same fence as a write, including cache misses.
func (s *Store) LoadEmbedding(ctx context.Context, job ingestion.Job, hash string) ([]float32, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err = s.lockJob(ctx, tx, job); err != nil {
		return nil, err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT embedding FROM embedding_checkpoints WHERE job_id=$1 AND index_generation=$2 AND model=$3 AND text_hash=$4`, job.ID, job.IndexGeneration, job.Model, hash).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, commit(ctx, tx)
	}
	if err != nil {
		return nil, err
	}
	var vector []float32
	if err = json.Unmarshal(raw, &vector); err != nil {
		return nil, err
	}
	if err = llm.ValidateVector(vector); err != nil {
		return nil, err
	}
	return vector, commit(ctx, tx)
}

func (s *Store) SaveEmbedding(ctx context.Context, job ingestion.Job, hash string, vector []float32) error {
	if err := llm.ValidateVector(vector); err != nil {
		return err
	}
	raw, err := json.Marshal(vector)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = s.lockJob(ctx, tx, job); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO embedding_checkpoints(job_id,index_generation,model,text_hash,embedding) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, job.ID, job.IndexGeneration, job.Model, hash, raw)
	if err != nil {
		return err
	}
	return commit(ctx, tx)
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

// PauseForBudget does not consume a retry attempt. Advancing generation fences
// the old owner even when a later claim reuses its attempt number.
func (s *Store) PauseForBudget(ctx context.Context, job ingestion.Job) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = s.lockJob(ctx, tx, job); err != nil {
		return err
	}
	message := "Достигнут дневной лимит AI. Обработка продолжится после 00:00 UTC."
	result, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET status='queued',lease_until=NULL,attempts=GREATEST(attempts-1,0),generation=generation+1,error_message=$2,
	 next_run_at=((date_trunc('day',clock_timestamp() AT TIME ZONE 'UTC')+interval '1 day') AT TIME ZONE 'UTC'),updated_at=clock_timestamp()
	 WHERE id=$1 AND lease_until>clock_timestamp()`, job.ID, message)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ingestion.ErrLeaseLost
	}
	if _, err = tx.Exec(ctx, `UPDATE documents SET status='queued',error_message=$2,updated_at=clock_timestamp() WHERE id=$1`, job.DocumentID, message); err != nil {
		return err
	}
	return commit(ctx, tx)
}

// Yield only after durable progress. A slow large document can span windows
// without exhausting failure retries; generation still fences its old owner.
func (s *Store) PauseForProgress(ctx context.Context, job ingestion.Job) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = s.lockJob(ctx, tx, job); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE ingestion_jobs SET status='queued',attempts=GREATEST(attempts-1,0),generation=generation+1,lease_until=NULL,next_run_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, job.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE documents SET status='queued',updated_at=clock_timestamp() WHERE id=$1`, job.DocumentID); err != nil {
		return err
	}
	return commit(ctx, tx)
}
