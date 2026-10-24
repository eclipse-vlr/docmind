package llm

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Fake uses deterministic token hashing. It tests the pipeline without network
// access or API keys; it does not model semantic similarity or synonyms.
type Fake struct{}

func (Fake) Model() string { return "fake-hash-v1" }

func (Fake) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := validateInput(texts); err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if len(words) == 0 {
			words = []string{strings.TrimSpace(text)}
		}
		v := make([]float32, Dimensions)
		for _, word := range words {
			h := fnv.New64a()
			_, _ = h.Write([]byte(word))
			v[h.Sum64()%Dimensions]++
		}
		var norm float64
		for _, n := range v {
			norm += float64(n) * float64(n)
		}
		norm = math.Sqrt(norm)
		for j := range v {
			v[j] = float32(float64(v[j]) / norm)
		}
		out[i] = v
	}
	return out, nil
}
