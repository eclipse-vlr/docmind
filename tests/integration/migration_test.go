package integration

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"docmind/internal/documents"
	"docmind/internal/ingestion"
)

func TestEmbeddingMigrationUpgradesTextOnlyDocuments(t *testing.T) {
	f := setupSchema(t, false)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, "CREATE TABLE schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_documents.up.sql", "002_file_deletion_retries.up.sql", "003_ingestion_chunks.up.sql"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, "INSERT INTO schema_migrations(version,checksum) VALUES ($1,$2)", name, fmt.Sprintf("%x", sha256.Sum256(data))); err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	for _, state := range []string{"ready", "processing", "queued", "failed"} {
		d := queuedDocument(t, f, "source text")
		ids = append(ids, d.ID)
		jobState := state
		if state == "ready" {
			jobState = "completed"
		}
		if _, err := f.pool.Exec(ctx, "UPDATE ingestion_jobs SET status=$2,attempts=2,lease_until=CASE WHEN $2='processing' THEN clock_timestamp()+interval '1 minute' ELSE NULL END WHERE document_id=$1", d.ID, jobState); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, "UPDATE documents SET status=$2 WHERE id=$1", d.ID, state); err != nil {
			t.Fatal(err)
		}
		if state == "ready" {
			if _, err := f.pool.Exec(ctx, "INSERT INTO document_chunks(id,document_id,ordinal,content) VALUES ($1,$2,0,'old text')", documents.NewID(), d.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Migrate(ctx); err != nil {
		t.Fatal("upgrade not repeatable", err)
	}
	for i, id := range ids {
		var status string
		var attempts int
		var generation int64
		var noLease bool
		if err := f.pool.QueryRow(ctx, "SELECT d.status,j.attempts,j.generation,j.lease_until IS NULL FROM documents d JOIN ingestion_jobs j ON j.document_id=d.id WHERE d.id=$1", id).Scan(&status, &attempts, &generation, &noLease); err != nil {
			t.Fatal(err)
		}
		if i < 2 && (status != "queued" || attempts != 0 || generation != 3 || !noLease) {
			t.Fatalf("old work not invalidated: %s %d %d %t", status, attempts, generation, noLease)
		}
		if i == 3 && status != "failed" {
			t.Fatal("failed document unexpectedly requeued")
		}
	}
	for range 3 {
		if err := worker(t, f, ingestion.TextParser{}).Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM document_chunks WHERE document_id=$1 AND embedding IS NOT NULL AND content='source text'", ids[0]).Scan(&count); err != nil || count != 1 {
		t.Fatal("old chunks not replaced", count, err)
	}
	if err := f.repo.SchemaReady(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestYandexEmbeddingMigrationPreservesSourcesAndFencesOldWorkers(t *testing.T) {
	f := setupSchema(t, false)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, "CREATE TABLE schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_documents.up.sql", "002_file_deletion_retries.up.sql", "003_ingestion_chunks.up.sql", "004_embeddings.up.sql"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, "INSERT INTO schema_migrations(version,checksum) VALUES ($1,$2)", name, fmt.Sprintf("%x", sha256.Sum256(data))); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.repo.SchemaReady(ctx); err == nil {
		t.Fatal("old schema reported ready for new encoders")
	}
	oldVector := "[1," + strings.Repeat("0,", 1534) + "0]"
	var ids []string
	var stale ingestion.Job
	for _, state := range []string{"ready", "processing", "queued", "failed"} {
		d := queuedDocument(t, f, "source text")
		ids = append(ids, d.ID)
		jobState := state
		if state == "ready" {
			jobState = "completed"
		}
		if _, err := f.pool.Exec(ctx, "UPDATE documents SET status=$2,embedding_model=CASE WHEN $2='ready' THEN 'legacy-1536' ELSE '' END WHERE id=$1", d.ID, state); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, "UPDATE ingestion_jobs SET status=$2,attempts=1,lease_until=CASE WHEN $2='processing' THEN now()+interval '1 minute' ELSE NULL END WHERE document_id=$1", d.ID, jobState); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, "INSERT INTO document_chunks(id,document_id,ordinal,content,embedding) VALUES ($1,$2,0,'old fragment',$3::text::public.vector)", documents.NewID(), d.ID, oldVector); err != nil {
			t.Fatal(err)
		}
		if state == "processing" {
			stale.DocumentID, stale.Attempt, stale.Generation = d.ID, 1, 1
			if err := f.pool.QueryRow(ctx, "SELECT id::text FROM ingestion_jobs WHERE document_id=$1", d.ID).Scan(&stale.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Migrate(ctx); err != nil {
		t.Fatal("migration not repeatable", err)
	}
	if err := f.repo.SchemaReady(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM document_chunks WHERE embedding IS NOT NULL").Scan(&count); err != nil || count != 0 {
		t.Fatal("old vectors survived", count, err)
	}
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM document_chunks WHERE content='old fragment'").Scan(&count); err != nil || count != 4 {
		t.Fatal("chunk text lost", count, err)
	}
	for i, id := range ids {
		data, err := f.files.Read(ctx, id)
		if err != nil || string(data) != "source text" {
			t.Fatal("source file lost", err)
		}
		var status, model string
		var attempts int
		var generation int64
		var noLease bool
		if err := f.pool.QueryRow(ctx, "SELECT d.status,d.embedding_model,j.attempts,j.generation,j.lease_until IS NULL FROM documents d JOIN ingestion_jobs j ON j.document_id=d.id WHERE d.id=$1", id).Scan(&status, &model, &attempts, &generation, &noLease); err != nil {
			t.Fatal(err)
		}
		if i < 3 && (status != "queued" || model != "" || attempts != 0 || generation != 2 || !noLease) {
			t.Fatalf("invalid reset: %s %s %d %d %t", status, model, attempts, generation, noLease)
		}
		if i == 3 && (status != "failed" || attempts != 1 || generation != 1) {
			t.Fatal("failed document was reset")
		}
	}
	for range 3 {
		job := claim(t, f)
		if job.DocumentID == stale.DocumentID {
			if job.Attempt != stale.Attempt || job.Generation != stale.Generation+1 {
				t.Fatal("expected reused attempt with a new fence")
			}
			if err := completeChunks(f, ctx, stale, []ingestion.Chunk{{Text: "stale"}}); !errors.Is(err, ingestion.ErrLeaseLost) {
				t.Fatal("old generation completed new job", err)
			}
			if err := f.repo.FailJob(ctx, stale, "stale", true, 3); !errors.Is(err, ingestion.ErrLeaseLost) {
				t.Fatal("old generation failed new job", err)
			}
		}
		if err := completeChunks(f, ctx, job, []ingestion.Chunk{{Text: "source text"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM document_chunks WHERE embedding IS NOT NULL AND public.vector_dims(embedding)=256 AND content='source text'").Scan(&count); err != nil || count != 3 {
		t.Fatal("new vectors invalid", count, err)
	}
}
