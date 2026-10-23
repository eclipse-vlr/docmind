package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"docmind/internal/documents"
	"github.com/jackc/pgx/v5"
)

const documentColumns = "id::text, original_name, mime_type, size_bytes, storage_key, status, error_message, created_at, updated_at"

func scanDocument(row pgx.Row) (documents.Document, error) {
	var d documents.Document
	err := row.Scan(&d.ID, &d.OriginalName, &d.MIMEType, &d.SizeBytes, &d.StorageKey, &d.Status, &d.ErrorMessage, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, documents.ErrNotFound
	}
	return d, err
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func commit(ctx context.Context, tx pgx.Tx) error {
	err := tx.Commit(ctx)
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrTxCommitRollback) {
		return fmt.Errorf("transaction rolled back: %w", err)
	}
	return errors.Join(documents.ErrCommitUnknown, err)
}

func (s *Store) Create(ctx context.Context, d documents.Document) (documents.Document, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return documents.Document{}, err
	}
	defer rollback(tx)
	created, err := scanDocument(tx.QueryRow(ctx,
		"INSERT INTO documents(id, original_name, mime_type, size_bytes, storage_key) VALUES ($1,$2,$3,$4,$5) RETURNING "+documentColumns,
		d.ID, d.OriginalName, d.MIMEType, d.SizeBytes, d.StorageKey))
	if err != nil {
		return documents.Document{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO ingestion_jobs(id,document_id) VALUES ($1,$2)", documents.NewID(), d.ID); err != nil {
		return documents.Document{}, err
	}
	if err := commit(ctx, tx); err != nil {
		return documents.Document{}, err
	}
	return created, nil
}

func (s *Store) Get(ctx context.Context, id string) (documents.Document, error) {
	return scanDocument(s.pool.QueryRow(ctx, "SELECT "+documentColumns+" FROM documents WHERE id=$1", id))
}

func (s *Store) List(ctx context.Context, limit, offset int) ([]documents.Document, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+documentColumns+" FROM documents ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]documents.Document, 0)
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, d)
	}
	return items, rows.Err()
}

func (s *Store) Delete(ctx context.Context, id string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollback(tx)
	var key string
	// Serialize deletion with worker finalization, which must lock this same row.
	err = tx.QueryRow(ctx, "SELECT storage_key FROM documents WHERE id=$1 FOR UPDATE", id).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", documents.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO file_deletions(storage_key) VALUES ($1)", key); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM documents WHERE id=$1", id); err != nil {
		return "", err
	}
	if err := commit(ctx, tx); err != nil {
		return "", err
	}
	return key, nil
}

// ClaimDeletions atomically postpones retries before returning a bounded batch.
// Failed or interrupted batches cannot starve newer work or be immediately
// claimed by another janitor. File removal itself remains idempotent.
func (s *Store) ClaimDeletions(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		WITH candidates AS (
			SELECT storage_key FROM file_deletions
			WHERE next_attempt_at <= now()
			ORDER BY next_attempt_at, storage_key
			LIMIT $1 FOR UPDATE SKIP LOCKED
		)
		UPDATE file_deletions AS deletions
		SET next_attempt_at = clock_timestamp() + interval '30 seconds'
		FROM candidates
		WHERE deletions.storage_key = candidates.storage_key
		RETURNING deletions.storage_key`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) CompleteDeletion(ctx context.Context, key string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM file_deletions WHERE storage_key=$1", key)
	return err
}
