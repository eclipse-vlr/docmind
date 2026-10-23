package documents

import (
	"context"
	"errors"
	"testing"
)

type fakeRepo struct {
	createErr error
	created   int
	pending   []string
	completed []string
}

func (r *fakeRepo) Create(_ context.Context, d Document) (Document, error) {
	r.created++
	return d, r.createErr
}
func (*fakeRepo) Get(context.Context, string) (Document, error)           { return Document{}, ErrNotFound }
func (*fakeRepo) List(context.Context, int, int) ([]Document, error)      { return nil, nil }
func (*fakeRepo) Delete(context.Context, string) (string, error)          { return "", ErrNotFound }
func (r *fakeRepo) ClaimDeletions(context.Context, int) ([]string, error) { return r.pending, nil }
func (r *fakeRepo) CompleteDeletion(_ context.Context, key string) error {
	r.completed = append(r.completed, key)
	return nil
}

type fakeFiles struct {
	saveErr, deleteErr error
	saved, deleted     int
	cleanupCanceled    bool
}

func (f *fakeFiles) Save(context.Context, string, []byte) error { f.saved++; return f.saveErr }
func (f *fakeFiles) Delete(ctx context.Context, _ string) error {
	f.deleted++
	f.cleanupCanceled = ctx.Err() != nil
	return f.deleteErr
}

func TestUploadCompensation(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		saveErr, dbErr, cleanupErr error
		saves, creates, deletes    int
	}{
		{"success", nil, nil, nil, 1, 1, 0},
		{"file failure", errors.New("disk"), nil, nil, 1, 0, 0},
		{"database rollback", nil, errors.New("database"), nil, 1, 1, 1},
		{"canceled database", nil, context.Canceled, nil, 1, 1, 1},
		{"unknown commit retains file", nil, ErrCommitUnknown, nil, 1, 1, 0},
		{"failed compensation surfaced", nil, errors.New("database"), errors.New("disk"), 1, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRepo{createErr: tc.dbErr}
			f := &fakeFiles{saveErr: tc.saveErr, deleteErr: tc.cleanupErr}
			s := NewService(r, f)
			ctx, cancel := context.WithCancel(context.Background())
			if errors.Is(tc.dbErr, context.Canceled) {
				cancel()
			}
			defer cancel()
			d, err := s.Upload(ctx, "a.txt", []byte("hello"))
			wantErr := tc.saveErr != nil || tc.dbErr != nil
			if (err != nil) != wantErr || f.saved != tc.saves || r.created != tc.creates || f.deleted != tc.deletes {
				t.Fatalf("err=%v saved=%d created=%d deleted=%d", err, f.saved, r.created, f.deleted)
			}
			if err == nil && (!ValidID(d.ID) || d.StorageKey != d.ID || d.Status != "queued") {
				t.Fatalf("bad document: %+v", d)
			}
			if f.cleanupCanceled {
				t.Fatal("compensation inherited cancellation")
			}
			if tc.cleanupErr != nil && !errors.Is(err, tc.cleanupErr) {
				t.Fatal("cleanup error lost")
			}
		})
	}
}

func TestCleanupRetry(t *testing.T) {
	r := &fakeRepo{pending: []string{NewID()}}
	f := &fakeFiles{deleteErr: errors.New("temporary failure")}
	s := NewService(r, f)
	if err := s.Cleanup(context.Background()); err == nil || len(r.completed) != 0 {
		t.Fatal("failed removal acknowledged")
	}
	f.deleteErr = nil
	if err := s.Cleanup(context.Background()); err != nil || len(r.completed) != 1 {
		t.Fatal("cleanup did not recover")
	}
}

func TestInvalidUploadHasNoEffects(t *testing.T) {
	r, f := &fakeRepo{}, &fakeFiles{}
	_, err := NewService(r, f).Upload(context.Background(), "a.exe", []byte("hello"))
	if !errors.Is(err, ErrUnsupported) || r.created != 0 || f.saved != 0 {
		t.Fatal("invalid upload had effects")
	}
}
