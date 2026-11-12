package integration

import (
	"docmind/internal/documents"
	"sync"
	"testing"
)

func TestConcurrentDuplicateUploadsCreateOneJob(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	results := make(chan documents.Document, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			d, err := f.service.Upload(ctx, "same.txt", []byte("identical source"))
			if err != nil {
				t.Error(err)
				return
			}
			results <- d
		})
	}
	wg.Wait()
	close(results)
	id := ""
	for d := range results {
		if id != "" && d.ID != id {
			t.Fatal("duplicate created")
		}
		id = d.ID
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM ingestion_jobs`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err := f.service.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	d, err := f.service.Upload(ctx, "same.txt", []byte("identical source"))
	if err != nil || d.ID == id {
		t.Fatal("deleted upload not recreated", err)
	}
}
