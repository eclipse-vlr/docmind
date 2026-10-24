package local

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"docmind/internal/documents"
)

type Store struct {
	root    *os.Root
	syncDir func() error
}

func Open(directory string) (*Store, error) {
	if err := createDirectory(directory); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	return &Store{root: root, syncDir: func() error { return syncDirectory(root) }}, nil
}

// Newly created storage directories need durable names in their parents too.
func createDirectory(directory string) error {
	var parents []string
	for current := filepath.Clean(directory); ; {
		if _, err := os.Stat(current); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return fs.ErrNotExist
		}
		parents = append(parents, parent)
		current = parent
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	for _, parent := range parents {
		root, err := os.OpenRoot(parent)
		if err != nil {
			return err
		}
		if err := errors.Join(syncDirectory(root), root.Close()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.root.Close() }

// Read bounds allocations even if a stored file was replaced or corrupted.
func (s *Store) Read(ctx context.Context, key string) ([]byte, error) {
	if !documents.ValidID(key) {
		return nil, documents.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := s.root.Open(key)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, documents.ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(f, documents.MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > documents.MaxFileSize {
		return nil, documents.ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

// Save syncs the file and its directory entry before publishing to the database.
func (s *Store) Save(ctx context.Context, key string, data []byte) (err error) {
	if !documents.ValidID(key) {
		return documents.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := s.root.OpenFile(key, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
		if err != nil {
			err = errors.Join(err, s.root.Remove(key), s.syncDir())
		}
	}()
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		size := min(len(data), 64<<10)
		n, err := f.Write(data[:size])
		if err != nil {
			return err
		}
		data = data[n:]
	}
	if err := f.Sync(); err != nil {
		return err
	}
	err = f.Close()
	f = nil
	if err != nil {
		return err
	}
	return s.syncDir()
}

// Delete is idempotent so crash recovery and concurrent janitors are safe.
func (s *Store) Delete(ctx context.Context, key string) error {
	if !documents.ValidID(key) {
		return documents.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.root.Remove(key)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Also sync an absent entry: a previous removal may have failed to sync.
	return s.syncDir()
}
