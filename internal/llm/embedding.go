package llm

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	Dimensions    = 1536
	BatchSize     = 32
	MaxInputBytes = 4000
)

var (
	ErrInput       = errors.New("invalid embedding input")
	ErrResponse    = errors.New("invalid embedding response")
	ErrUnavailable = errors.New("embedding provider unavailable")
	ErrRejected    = errors.New("embedding request rejected")
	ErrTimeout     = errors.New("embedding request timed out")
)

// EmbeddingProvider preserves input order and returns nonzero finite vectors.
// Model identifies the vector space, including the provider and model version.
type EmbeddingProvider interface {
	Embed(context.Context, []string) ([][]float32, error)
	Model() string
}

func validateInput(texts []string) error {
	if len(texts) < 1 || len(texts) > BatchSize {
		return ErrInput
	}
	for _, text := range texts {
		if len(text) > MaxInputBytes || !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
			return ErrInput
		}
	}
	return nil
}

func ValidateVector(vector []float32) error {
	if len(vector) != Dimensions {
		return ErrResponse
	}
	var norm float64
	for _, v := range vector {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return ErrResponse
		}
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return ErrResponse
	}
	return nil
}
