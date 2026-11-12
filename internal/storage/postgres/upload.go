package postgres

import (
	"context"
	"errors"

	"docmind/internal/documents"
)

func (s *Store) CreateUnique(ctx context.Context, d documents.Document, hash string) (documents.Document, error) {
	if len(hash) != 64 {
		return documents.Document{}, documents.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return documents.Document{}, err
	}
	defer rollback(tx)
	created, err := scanDocument(tx.QueryRow(ctx, `INSERT INTO documents(id,original_name,mime_type,size_bytes,storage_key,content_hash) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(content_hash) WHERE content_hash IS NOT NULL DO NOTHING RETURNING `+documentColumns, d.ID, d.OriginalName, d.MIMEType, d.SizeBytes, d.StorageKey, hash))
	if errors.Is(err, documents.ErrNotFound) {
		created, err = scanDocument(tx.QueryRow(ctx, `SELECT `+documentColumns+` FROM documents WHERE content_hash=$1 FOR SHARE`, hash))
		if errors.Is(err, documents.ErrNotFound) {
			return documents.Document{}, documents.ErrUnavailable
		}
		if err != nil {
			return documents.Document{}, err
		}
		// A failed compensation remains recoverable by the existing janitor.
		if _, err = tx.Exec(ctx, `INSERT INTO file_deletions(storage_key) VALUES($1)`, d.StorageKey); err != nil {
			return documents.Document{}, err
		}
	} else if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO ingestion_jobs(id,document_id) VALUES($1,$2)`, documents.NewID(), d.ID)
	}
	if err != nil {
		return documents.Document{}, err
	}
	if err = commit(ctx, tx); err != nil {
		return documents.Document{}, err
	}
	return created, nil
}
