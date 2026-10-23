package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"docmind/internal/documents"
)

type blockedFiles struct {
	documents.Storage
	blocked map[string]bool
}

func (s blockedFiles) Delete(ctx context.Context, key string) error {
	if s.blocked[key] {
		return errors.New("injected persistent removal failure")
	}
	return s.Storage.Delete(ctx, key)
}

func TestCleanupContinuesPastFailedBatch(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	blocked := make(map[string]bool)
	for range 100 {
		key := documents.NewID()
		blocked[key] = true
		if _, err := f.pool.Exec(ctx, `INSERT INTO file_deletions(storage_key,next_attempt_at)
			VALUES ($1,now()-interval '1 hour')`, key); err != nil {
			t.Fatal(err)
		}
	}
	d, err := f.service.Upload(ctx, "recoverable.txt", []byte("contents"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	service := documents.NewService(f.repo, blockedFiles{Storage: f.files, blocked: blocked})
	if err := service.Cleanup(ctx); err == nil {
		t.Fatal("expected removal failures")
	}
	// A restarted janitor must continue past the failed batch without waiting.
	service = documents.NewService(f.repo, blockedFiles{Storage: f.files, blocked: blocked})
	if err := service.Cleanup(ctx); err != nil {
		t.Fatal("failed batch starved recoverable deletion", err)
	}
	if _, err := os.Stat(filepath.Join(f.directory, d.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recoverable file survived: %v", err)
	}
	var pending int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM file_deletions").Scan(&pending); err != nil || pending != 100 {
		t.Fatal("failed removals were acknowledged", pending, err)
	}
	// Advance persisted deadlines instead of waiting for wall-clock backoff.
	if _, err := f.pool.Exec(ctx, "UPDATE file_deletions SET next_attempt_at=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Cleanup(ctx); err != nil {
		t.Fatal("expired retries did not recover", err)
	}
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM file_deletions").Scan(&pending); err != nil || pending != 0 {
		t.Fatal("retries left pending entries", pending, err)
	}
}

func TestCleanupClaimsAreDisjointAndRecoverable(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for range 12 {
		if _, err := f.pool.Exec(ctx, "INSERT INTO file_deletions(storage_key) VALUES ($1)", documents.NewID()); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		keys []string
		err  error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			keys, err := f.repo.ClaimDeletions(ctx, 6)
			results <- result{keys, err}
		}()
	}
	close(start)
	seen := make(map[string]bool)
	for range 2 {
		r := <-results
		if r.err != nil || len(r.keys) != 6 {
			t.Fatal("unexpected claim", len(r.keys), r.err)
		}
		for _, key := range r.keys {
			if seen[key] {
				t.Fatal("janitors claimed the same entry", key)
			}
			seen[key] = true
		}
	}
	if keys, err := f.repo.ClaimDeletions(ctx, 12); err != nil || len(keys) != 0 {
		t.Fatal("unexpired claims were returned", err)
	}
	// Simulate both claimants dying before removing any files.
	if _, err := f.pool.Exec(ctx, "UPDATE file_deletions SET next_attempt_at=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Cleanup(ctx); err != nil {
		t.Fatal("abandoned claims did not recover", err)
	}
	var pending int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM file_deletions").Scan(&pending); err != nil || pending != 0 {
		t.Fatal("abandoned claims survived", pending, err)
	}
}

func TestCleanupMigrationPreservesPendingEntries(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	key := documents.NewID()
	// Reconstruct the old schema inside this test's isolated namespace.
	if _, err := f.pool.Exec(ctx, `ALTER TABLE file_deletions DROP COLUMN next_attempt_at;
		DELETE FROM schema_migrations WHERE version='002_file_deletion_retries.up.sql'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "INSERT INTO file_deletions(storage_key) VALUES ($1)", key); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.SchemaReady(ctx); err == nil {
		t.Fatal("old schema reported ready")
	}
	if err := f.repo.Migrate(ctx); err != nil {
		t.Fatal("upgrade failed", err)
	}
	if err := f.repo.Migrate(ctx); err != nil {
		t.Fatal("upgrade was not repeatable", err)
	}
	if err := f.repo.SchemaReady(ctx); err != nil {
		t.Fatal("upgraded schema not ready", err)
	}
	keys, err := f.repo.ClaimDeletions(ctx, 100)
	if err != nil || len(keys) != 1 || keys[0] != key {
		t.Fatal("upgrade lost pending entry", keys, err)
	}
}
