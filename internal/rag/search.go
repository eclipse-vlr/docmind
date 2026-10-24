package rag

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"docmind/internal/documents"
	"docmind/internal/llm"
)

var (
	ErrInvalid       = errors.New("invalid search request")
	ErrNotReady      = errors.New("documents are not ready")
	ErrModelMismatch = errors.New("documents require reindexing with the selected embedding model")
	ErrUnavailable   = errors.New("search storage unavailable")
)

const MaxDocuments = 20
const MaxTopK = 20

type Match struct {
	DocumentID string  `json:"document_id"`
	ChunkID    string  `json:"chunk_id"`
	Page       *int    `json:"page"`
	Snippet    string  `json:"snippet"`
	Distance   float64 `json:"distance"`
}

type SearchRepository interface {
	CheckDocuments(context.Context, []string, string) error
	SearchChunks(context.Context, []string, string, []float32, int) ([]Match, error)
}

type Searcher struct {
	repo     SearchRepository
	provider llm.EmbeddingProvider
}

func NewSearcher(repo SearchRepository, provider llm.EmbeddingProvider) *Searcher {
	return &Searcher{repo: repo, provider: provider}
}

func (s *Searcher) Search(ctx context.Context, question string, ids []string, topK int) ([]Match, error) {
	question = strings.TrimSpace(question)
	if question == "" || len(question) > llm.MaxInputBytes || !utf8.ValidString(question) || len(ids) < 1 || len(ids) > MaxDocuments || topK < 1 || topK > MaxTopK {
		return nil, ErrInvalid
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !documents.ValidID(id) || seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Validate before spending a provider request; the repository rechecks within
	// its search snapshot in case DELETE/reindex races with the network call.
	if err := s.repo.CheckDocuments(ctx, ids, s.provider.Model()); err != nil {
		return nil, storageError(err)
	}
	vectors, err := s.provider.Embed(ctx, []string{question})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 || llm.ValidateVector(vectors[0]) != nil {
		return nil, llm.ErrResponse
	}
	matches, err := s.repo.SearchChunks(ctx, ids, s.provider.Model(), vectors[0], topK)
	if err != nil {
		return nil, storageError(err)
	}
	return matches, nil
}

func storageError(err error) error {
	for _, known := range []error{documents.ErrNotFound, ErrNotReady, ErrModelMismatch, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrUnavailable
}
