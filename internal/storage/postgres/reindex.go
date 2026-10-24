package postgres

import (
	"context"
	"errors"

	"docmind/internal/documents"
	"github.com/jackc/pgx/v5"
)

// RequeueDocument invalidates the old lease even when its attempt number will
// be reused. Repeating it while already queued is a no-op. Existing fragments
// remain inaccessible to search until atomically replaced by the worker.
func (s *Store) RequeueDocument(ctx context.Context, id string) error {
	if !documents.ValidID(id) {
		return documents.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var status string
	err = tx.QueryRow(ctx, "SELECT status FROM documents WHERE id=$1 FOR UPDATE", id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return documents.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == "queued" {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET status='queued',attempts=0,generation=generation+1,
		lease_until=NULL,next_run_at=clock_timestamp(),error_message='',updated_at=clock_timestamp() WHERE document_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE documents SET status='queued',embedding_model='',error_message='',updated_at=clock_timestamp() WHERE id=$1", id); err != nil {
		return err
	}
	return commit(ctx, tx)
}
