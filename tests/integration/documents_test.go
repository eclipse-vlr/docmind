package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docmind/internal/documents"
	"docmind/internal/httpapi"
	"docmind/internal/storage/local"
	"docmind/internal/storage/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	repo      *postgres.Store
	pool      *pgxpool.Pool
	service   *documents.Service
	files     *local.Store
	directory string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	schema := "docmind_test_" + strings.ReplaceAll(documents.NewID(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal("create isolated test schema failed")
	}
	t.Cleanup(func() {
		defer admin.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Error("test schema cleanup failed")
		}
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("test database must be a URI")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	repo, err := postgres.Open(ctx, u.String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repo.Close)
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal("migration was not repeatable", err)
	}
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal("test pool failed")
	}
	t.Cleanup(pool.Close)
	directory := t.TempDir()
	files, err := local.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.Close() })
	return &fixture{repo: repo, pool: pool, files: files, directory: directory, service: documents.NewService(repo, files)}
}

func upload(t *testing.T, h http.Handler, name string) documents.Document {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	p, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("Document contents")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	r := httptest.NewRequest("POST", "/api/v1/documents", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	res := httptest.NewRecorder()
	h.ServeHTTP(res, r)
	if res.Code != 202 {
		t.Fatalf("upload=%d %s", res.Code, res.Body.String())
	}
	var d documents.Document
	if err := json.Unmarshal(res.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDocumentLifecycle(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := httpapi.NewHandler(f.repo.SchemaReady, time.Second, logger, f.service)
	a := upload(t, h, "first.txt")
	b := upload(t, h, "second.md")
	for _, id := range []string{a.ID, b.ID} {
		var n int
		if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM ingestion_jobs WHERE document_id=$1 AND status='queued'", id).Scan(&n); err != nil || n != 1 {
			t.Fatal("job missing", err)
		}
		if _, err := os.Stat(filepath.Join(f.directory, id)); err != nil {
			t.Fatal(err)
		}
	}
	// Re-open storage and reconstruct the service over persisted metadata.
	restartedFiles, err := local.Open(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer restartedFiles.Close()
	service := documents.NewService(f.repo, restartedFiles)
	items, err := service.List(ctx, 1, 0)
	if err != nil || len(items) != 1 || items[0].ID != b.ID {
		t.Fatal("pagination/order mismatch", err)
	}
	items, err = service.List(ctx, 1, 1)
	if err != nil || len(items) != 1 || items[0].ID != a.ID {
		t.Fatal("offset mismatch", err)
	}
	d, err := service.Get(ctx, a.ID)
	if err != nil || d.Status != "queued" || d.OriginalName != "first.txt" {
		t.Fatal("metadata did not persist", err)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest("DELETE", "/api/v1/documents/"+a.ID, nil))
	if res.Code != 204 {
		t.Fatalf("delete=%d %s", res.Code, res.Body.String())
	}
	if _, err := os.Stat(filepath.Join(f.directory, a.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source file survived deletion")
	}
	var n int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM ingestion_jobs WHERE document_id=$1", a.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("job survived deletion", err)
	}
	if _, err := service.Get(ctx, a.ID); !errors.Is(err, documents.ErrNotFound) {
		t.Fatal("document survived deletion", err)
	}
	if err := service.Delete(ctx, a.ID); !errors.Is(err, documents.ErrNotFound) {
		t.Fatal("second delete should be not found", err)
	}
}

func TestUploadTransactionRollback(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `CREATE FUNCTION reject_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected job insert failure'; END $$; CREATE TRIGGER reject_job BEFORE INSERT ON ingestion_jobs FOR EACH ROW EXECUTE FUNCTION reject_job();`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Upload(ctx, "a.txt", []byte("text")); err == nil {
		t.Fatal("expected job insert failure")
	}
	var n int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM documents").Scan(&n); err != nil || n != 0 {
		t.Fatal("partial document row committed", err)
	}
	entries, err := os.ReadDir(f.directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("file compensation failed", err)
	}
}

func TestDeletionOutboxRecovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d, err := f.service.Upload(ctx, "a.txt", []byte("text"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	} // Emulate a crash after commit, before file removal.
	var pending int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM file_deletions").Scan(&pending); err != nil || pending != 1 {
		t.Fatal("cleanup intent missing", err)
	}
	if err := documents.NewService(f.repo, f.files).Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM file_deletions").Scan(&pending); err != nil || pending != 0 {
		t.Fatal("cleanup intent not acknowledged", err)
	}
	if _, err := os.Stat(filepath.Join(f.directory, d.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file not cleaned")
	}
}

func TestDeleteSerializesWithProcessing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d, err := f.service.Upload(ctx, "a.txt", []byte("text"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT id FROM documents WHERE id=$1 FOR UPDATE", d.ID); err != nil {
		t.Fatal(err)
	}
	blocked, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := f.service.Delete(blocked, d.ID); err == nil {
		t.Fatal("delete bypassed worker row lock")
	}
	var active int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND datname=current_database() AND state='active' AND query LIKE '%FOR UPDATE%'").Scan(&active); err != nil || active != 0 {
		t.Fatal("canceled locking query still active", err)
	}
	if _, err := os.Stat(filepath.Join(f.directory, d.ID)); err != nil {
		t.Fatal("blocked deletion removed source", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	// A stale worker cannot create a job after the parent document has gone.
	if _, err := f.pool.Exec(ctx, "INSERT INTO ingestion_jobs(id,document_id) VALUES ($1,$2)", documents.NewID(), d.ID); err == nil {
		t.Fatal("foreign key allowed resurrection")
	}
}

func TestConcurrentDeletion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d, err := f.service.Upload(ctx, "a.txt", []byte("text"))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() { <-start; results <- f.service.Delete(ctx, d.ID) }()
	}
	close(start)
	a, b := <-results, <-results
	if !(a == nil && errors.Is(b, documents.ErrNotFound) || b == nil && errors.Is(a, documents.ErrNotFound)) {
		t.Fatalf("unexpected deletion outcomes: %v, %v", a, b)
	}
	if err := f.service.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.directory, d.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file survived concurrent deletion")
	}
}

func TestMigrationChecksumProtection(t *testing.T) {
	f := setup(t)
	if _, err := f.pool.Exec(context.Background(), "UPDATE schema_migrations SET checksum='tampered'"); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Migrate(context.Background()); err == nil {
		t.Fatal("changed migration checksum was accepted")
	}
}
