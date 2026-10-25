package llm

import (
	"context"
	"strings"
	"unicode/utf8"
)

const (
	MaxChatSources  = 5
	MaxContextBytes = 16 << 10
	MaxAnswerBytes  = 8 << 10
)

type Fragment struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type ChatRequest struct {
	Question  string     `json:"question"`
	Fragments []Fragment `json:"fragments"`
}

type ChatResult struct {
	Answer              string   `json:"answer"`
	SourceIDs           []string `json:"source_ids"`
	InsufficientContext bool     `json:"insufficient_context"`
}

// ChatProvider selects only IDs from the supplied context. The application
// owns document/chunk metadata and verifies the selection before publishing it.
type ChatProvider interface {
	Chat(context.Context, ChatRequest) (ChatResult, error)
}

func validateChatInput(in ChatRequest) error {
	if strings.TrimSpace(in.Question) == "" || len(in.Question) > MaxInputBytes || !utf8.ValidString(in.Question) || len(in.Fragments) < 1 || len(in.Fragments) > MaxChatSources {
		return ErrInput
	}
	total := 0
	seen := make(map[string]bool, len(in.Fragments))
	for _, f := range in.Fragments {
		if len(f.ID) != 2 || f.ID[0] != 's' || f.ID[1] < '1' || f.ID[1] > '5' || seen[f.ID] || strings.TrimSpace(f.Text) == "" || len(f.Text) > MaxInputBytes || !utf8.ValidString(f.Text) {
			return ErrInput
		}
		seen[f.ID] = true
		total += len(f.Text)
	}
	if total > MaxContextBytes {
		return ErrInput
	}
	return nil
}

// ValidateChatResult is also called by the RAG service, including for custom
// providers. A valid schema alone cannot prove citation IDs are real.
func ValidateChatResult(in ChatRequest, out ChatResult) error {
	if !utf8.ValidString(out.Answer) || len(out.Answer) > MaxAnswerBytes {
		return ErrResponse
	}
	if out.InsufficientContext {
		if len(out.SourceIDs) != 0 {
			return ErrResponse
		}
		return nil
	}
	if strings.TrimSpace(out.Answer) == "" || len(out.SourceIDs) < 1 || len(out.SourceIDs) > len(in.Fragments) {
		return ErrResponse
	}
	allowed := make(map[string]bool, len(in.Fragments))
	for _, f := range in.Fragments {
		allowed[f.ID] = true
	}
	for _, id := range out.SourceIDs {
		if !allowed[id] {
			return ErrResponse
		}
		delete(allowed, id)
	}
	return nil
}
