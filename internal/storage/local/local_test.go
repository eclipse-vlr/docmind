package local

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"docmind/internal/documents"
)

func TestPrivateFileLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "nested", "documents")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := documents.NewID()
	ctx := context.Background()
	if err := s.Save(ctx, id, []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, id, []byte("overwrite")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("overwrite allowed: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, id))
	if err != nil || string(body) != "original" {
		t.Fatal("file changed", err)
	}
	for _, key := range []string{"../escape", "/absolute", id + "/../escape"} {
		if err := s.Delete(ctx, key); err == nil {
			t.Fatal("invalid key accepted")
		}
		if err := s.Save(ctx, key, nil); err == nil {
			t.Fatal("invalid save key accepted")
		}
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal("delete not idempotent", err)
	}
	if _, err := os.Stat(filepath.Join(dir, id)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("file not removed")
	}
}

func TestCanceledSave(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Save(ctx, documents.NewID(), []byte("body")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("canceled save left file", err)
	}
}

func TestSaveDirectorySyncFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	syncErr := errors.New("injected directory sync failure")
	s.syncDir = func() error { return syncErr }
	id := documents.NewID()
	if err := s.Save(context.Background(), id, []byte("contents")); !errors.Is(err, syncErr) {
		t.Fatalf("directory sync failure lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, id)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed save left a file: %v", err)
	}
}

func TestDeleteRetriesDirectorySyncForAbsentFile(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	id := documents.NewID()
	if err := s.Save(ctx, id, []byte("contents")); err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("injected directory sync failure")
	syncCalls := 0
	realSync := s.syncDir
	s.syncDir = func() error {
		syncCalls++
		if syncCalls == 1 {
			return syncErr
		}
		return realSync()
	}
	if err := s.Delete(ctx, id); !errors.Is(err, syncErr) {
		t.Fatalf("unsynced deletion acknowledged: %v", err)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if syncCalls != 2 {
		t.Fatalf("absent file bypassed directory sync; calls=%d", syncCalls)
	}
}
