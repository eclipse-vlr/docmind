package llm

import (
	"context"
	"strings"
	"unicode"
)

// FakeChat returns an unchanged excerpt with the most query-token overlap.
// It exercises retrieval and citations; it does not synthesize an LLM answer.
type FakeChat struct{}

func (FakeChat) Chat(ctx context.Context, in ChatRequest) (ChatResult, error) {
	if err := validateChatInput(in); err != nil {
		return ChatResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ChatResult{}, err
	}
	query := wordSet(in.Question)
	best, bestScore := -1, 0
	for i, f := range in.Fragments {
		score := 0
		for word := range wordSet(f.Text) {
			if query[word] {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return ChatResult{InsufficientContext: true, SourceIDs: []string{}}, nil
	}
	f := in.Fragments[best]
	return ChatResult{Answer: f.Text, SourceIDs: []string{f.ID}}, nil
}

func wordSet(text string) map[string]bool {
	out := make(map[string]bool)
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		// Ignore frequent question/function words in the deterministic demo.
		switch word {
		case "а", "в", "во", "и", "из", "к", "как", "какие", "какой", "на", "о", "по", "с", "у", "что", "это", "the", "a", "an", "and", "is", "in", "of", "to", "what", "which", "how":
			continue
		}
		out[word] = true
	}
	return out
}
