package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"docmind/internal/documents"
)

func TestReadDocument(t *testing.T) {
	directory := t.TempDir()
	s, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := documents.NewID()
	if err := s.Save(context.Background(), key, []byte("text")); err != nil {
		t.Fatal(err)
	}
	data, err := s.Read(context.Background(), key)
	if err != nil || string(data) != "text" {
		t.Fatal("read did not preserve contents", err)
	}
	if _, err := s.Read(context.Background(), "../outside"); !errors.Is(err, documents.ErrInvalid) {
		t.Fatal("unsafe key accepted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Read(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
	if _, err := s.Read(context.Background(), documents.NewID()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing file error lost", err)
	}
	f, err := os.OpenFile(filepath.Join(directory, key), os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(documents.MaxFileSize + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if _, err := s.Read(context.Background(), key); !errors.Is(err, documents.ErrTooLarge) {
		t.Fatal("oversized stored file accepted", err)
	}
}
