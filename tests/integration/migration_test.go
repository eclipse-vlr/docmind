package integration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
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
		if i < 2 && (status != "queued" || attempts != 0 || generation != 2 || !noLease) {
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
