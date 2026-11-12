package ingestion

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNoJob     = errors.New("no ingestion job available")
	ErrLeaseLost = errors.New("ingestion lease lost")
	ErrInvalid   = errors.New("invalid or unreadable document")
	ErrEmpty     = errors.New("document has no text layer")
	ErrLimit     = errors.New("document extraction limit exceeded")
	ErrProgress  = errors.New("job deadline reached after saving progress")
)

const (
	MaxTextBytes = 2 << 20
	MaxChunks    = 4096
	ChunkRunes   = 1000
	OverlapRunes = 200
)

type Job struct {
	ID              string
	DocumentID      string
	StorageKey      string
	MIMEType        string
	Attempt         int
	Generation      int64
	Model           string
	IndexGeneration int64
}

type Chunk struct {
	Text      string
	Page      *int
	Embedding []float32
}

// Repository owns atomic claims and fenced finalization, including document status.
type Repository interface {
	ClaimJob(context.Context, time.Duration, int) (Job, error)
	CompleteJob(context.Context, Job, []Chunk) error
	FailJob(context.Context, Job, string, bool, int) error
	LoadEmbedding(context.Context, Job, string) ([]float32, error)
	SaveEmbedding(context.Context, Job, string, []float32) error
	PauseForProgress(context.Context, Job) error
}

type Files interface {
	Read(context.Context, string) ([]byte, error)
}

type Parser interface {
	Parse(context.Context, string, []byte) ([]Chunk, error)
}
