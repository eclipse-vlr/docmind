package integration

import (
	"context"
	"errors"
	"testing"

	"docmind/internal/documents"
	"docmind/internal/ingestion"
	"docmind/internal/llm"
	"docmind/internal/rag"
)

func TestSearchRanksCorpusAndFiltersDocuments(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := queuedDocument(t, f, "PostgreSQL transactions database")
	b := queuedDocument(t, f, "cats dogs animals")
	c := queuedDocument(t, f, "PostgreSQL transactions database")
	for range 3 {
		if err := worker(t, f, ingestion.TextParser{}).Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	s := rag.NewSearcher(f.repo, llm.Fake{})
	matches, err := s.Search(ctx, "PostgreSQL transactions", []string{a.ID, b.ID}, 2)
	if err != nil || len(matches) != 2 {
		t.Fatalf("matches=%v error=%v", matches, err)
	}
	if matches[0].DocumentID != a.ID || matches[0].Distance >= matches[1].Distance || matches[0].Snippet != "PostgreSQL transactions database" || matches[0].Page != nil || !documents.ValidID(matches[0].ChunkID) {
		t.Fatal("ranking or metadata invalid", matches)
	}
	for _, m := range matches {
		if m.DocumentID == c.ID {
			t.Fatal("document filter leaked excluded content")
		}
	}
	matches, err = s.Search(ctx, "PostgreSQL", []string{b.ID}, 1)
	if err != nil || len(matches) != 1 || matches[0].DocumentID != b.ID {
		t.Fatal("filter ignored", err)
	}
}

func TestSearchDocumentStatesAndModels(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := queuedDocument(t, f, "database")
	s := rag.NewSearcher(f.repo, llm.Fake{})
	if _, err := s.Search(ctx, "database", []string{d.ID}, 1); !errors.Is(err, rag.ErrNotReady) {
		t.Fatal(err)
	}
	if err := worker(t, f, ingestion.TextParser{}).Step(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE documents SET embedding_model='other-model' WHERE id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search(ctx, "database", []string{d.ID}, 1); !errors.Is(err, rag.ErrModelMismatch) {
		t.Fatal(err)
	}
	if _, err := s.Search(ctx, "database", []string{d.ID, documents.NewID()}, 1); !errors.Is(err, documents.ErrNotFound) {
		t.Fatal(err)
	}
	if err := f.service.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search(ctx, "database", []string{d.ID}, 1); !errors.Is(err, documents.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestReindexGenerationFencesReusedAttempt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := queuedDocument(t, f, "database")
	old := claim(t, f)
	if err := f.repo.RequeueDocument(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	// A second command while queued must not advance the generation again.
	if err := f.repo.RequeueDocument(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	current := claim(t, f)
	if current.Attempt != old.Attempt || current.Generation != old.Generation+1 {
		t.Fatalf("old=%+v new=%+v", old, current)
	}
	if err := completeChunks(f, ctx, old, []ingestion.Chunk{{Text: "stale"}}); !errors.Is(err, ingestion.ErrLeaseLost) {
		t.Fatal("old generation completed", err)
	}
	if err := f.repo.FailJob(ctx, old, "stale", true, 3); !errors.Is(err, ingestion.ErrLeaseLost) {
		t.Fatal("old generation failed new job", err)
	}
	if err := completeChunks(f, ctx, current, []ingestion.Chunk{{Text: "database"}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.repo.RequeueDocument(ctx, d.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := rag.NewSearcher(f.repo, llm.Fake{}).Search(ctx, "database", []string{d.ID}, 1); !errors.Is(err, rag.ErrNotReady) {
			t.Fatal("old chunks searchable", err)
		}
		if err := worker(t, f, ingestion.TextParser{}).Step(ctx); err != nil {
			t.Fatal(err)
		}
		var total, embedded int
		if err := f.pool.QueryRow(ctx, "SELECT count(*),count(embedding) FROM document_chunks WHERE document_id=$1", d.ID).Scan(&total, &embedded); err != nil || total != 1 || embedded != 1 {
			t.Fatal("reindex duplicated or lost vectors", total, embedded, err)
		}
	}
}

type deletingProvider struct {
	f  *fixture
	id string
}

func (p deletingProvider) Model() string { return (llm.Fake{}).Model() }
func (p deletingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := p.f.service.Delete(ctx, p.id); err != nil {
		return nil, err
	}
	return (llm.Fake{}).Embed(ctx, texts)
}

func TestDeletionBetweenSearchPreflightAndQuery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := queuedDocument(t, f, "private content")
	if err := worker(t, f, ingestion.TextParser{}).Step(ctx); err != nil {
		t.Fatal(err)
	}
	if matches, err := rag.NewSearcher(f.repo, deletingProvider{f, d.ID}).Search(ctx, "content", []string{d.ID}, 1); !errors.Is(err, documents.ErrNotFound) || len(matches) != 0 {
		t.Fatal("deleted content returned", matches, err)
	}
}
