package postgres

import (
	"context"

	"docmind/internal/documents"
	"docmind/internal/llm"
	"docmind/internal/rag"
	"github.com/jackc/pgx/v5"
)

type documentQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func checkDocuments(ctx context.Context, q documentQuerier, ids []string, model string) error {
	rows, err := q.Query(ctx, "SELECT id::text,status,embedding_model FROM documents WHERE id=ANY($1::uuid[])", ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := 0
	var stateError error
	for rows.Next() {
		var id, status, indexedModel string
		if err := rows.Scan(&id, &status, &indexedModel); err != nil {
			return err
		}
		found++
		if status != "ready" {
			stateError = rag.ErrNotReady
		} else if indexedModel != model && stateError == nil {
			stateError = rag.ErrModelMismatch
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if found != len(ids) {
		return documents.ErrNotFound
	}
	return stateError
}

func (s *Store) CheckDocuments(ctx context.Context, ids []string, model string) error {
	return checkDocuments(ctx, s.pool, ids, model)
}

func (s *Store) SearchChunks(ctx context.Context, ids []string, model string, vector []float32, topK int) ([]rag.Match, error) {
	if len(ids) < 1 || len(ids) > rag.MaxDocuments || topK < 1 || topK > rag.MaxTopK || llm.ValidateVector(vector) != nil {
		return nil, rag.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err := checkDocuments(ctx, tx, ids, model); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT c.document_id::text,c.id::text,c.page_number,c.content,
		(c.embedding OPERATOR(public.<=>) $2::text::public.vector)::float8 AS distance
		FROM document_chunks c JOIN documents d ON d.id=c.document_id
		WHERE c.document_id=ANY($1::uuid[]) AND d.status='ready' AND d.embedding_model=$3 AND c.embedding IS NOT NULL
		ORDER BY distance,c.document_id,c.ordinal LIMIT $4`, ids, vectorString(vector), model, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	matches := make([]rag.Match, 0)
	for rows.Next() {
		var m rag.Match
		if err := rows.Scan(&m.DocumentID, &m.ChunkID, &m.Page, &m.Snippet, &m.Distance); err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := commit(ctx, tx); err != nil {
		return nil, err
	}
	return matches, nil
}
