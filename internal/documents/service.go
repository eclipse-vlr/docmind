package documents

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Repository interface {
	Create(context.Context, Document) (Document, error)
	Get(context.Context, string) (Document, error)
	List(context.Context, int, int) ([]Document, error)
	Delete(context.Context, string) (string, error)
	ClaimDeletions(context.Context, int) ([]string, error)
	CompleteDeletion(context.Context, string) error
}

type Storage interface {
	Save(context.Context, string, []byte) error
	Delete(context.Context, string) error
}

type Service struct {
	repo  Repository
	files Storage
}

func NewService(repo Repository, files Storage) *Service { return &Service{repo: repo, files: files} }

func (s *Service) Upload(ctx context.Context, filename string, body []byte) (Document, error) {
	name, mime, err := ValidateUpload(filename, body)
	if err != nil {
		return Document{}, err
	}
	id := NewID()
	d := Document{ID: id, OriginalName: name, MIMEType: mime, SizeBytes: int64(len(body)), StorageKey: id, Status: "queued"}
	if err := s.files.Save(ctx, id, body); err != nil {
		return Document{}, fmt.Errorf("save document: %w", err)
	}
	created, err := s.repo.Create(ctx, d)
	if err == nil {
		return created, nil
	}
	if errors.Is(err, ErrCommitUnknown) {
		// Never remove a file that a possibly committed row may reference.
		return Document{}, err
	}
	// Compensation must still run after caller cancellation.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if cleanupErr := s.files.Delete(cleanupCtx, id); cleanupErr != nil {
		return Document{}, errors.Join(err, fmt.Errorf("compensate file: %w", cleanupErr))
	}
	return Document{}, err
}

func (s *Service) Get(ctx context.Context, id string) (Document, error) {
	if !ValidID(id) {
		return Document{}, ErrInvalid
	}
	return s.repo.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, limit, offset int) ([]Document, error) {
	if limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return nil, ErrInvalid
	}
	return s.repo.List(ctx, limit, offset)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if !ValidID(id) {
		return ErrInvalid
	}
	key, err := s.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	// The durable cleanup entry was committed with the metadata deletion.
	return s.clean(ctx, key)
}

func (s *Service) clean(ctx context.Context, key string) error {
	if err := s.files.Delete(ctx, key); err != nil {
		return fmt.Errorf("remove document file: %w", err)
	}
	return s.repo.CompleteDeletion(ctx, key)
}

func (s *Service) Cleanup(ctx context.Context) error {
	keys, err := s.repo.ClaimDeletions(ctx, 100)
	if err != nil {
		return err
	}
	var failures []error
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := s.clean(ctx, key); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
