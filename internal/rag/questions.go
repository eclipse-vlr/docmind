package rag

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"docmind/internal/documents"
	"docmind/internal/llm"
)

const InsufficientAnswer = "В выбранных документах недостаточно информации для ответа."

type Source struct {
	DocumentID string `json:"document_id"`
	ChunkID    string `json:"chunk_id"`
	Page       *int   `json:"page"`
	Snippet    string `json:"snippet"`
}

type Answer struct {
	Answer  string   `json:"answer"`
	Sources []Source `json:"sources"`
}

type Retriever interface {
	Search(context.Context, string, []string, int) ([]Match, error)
}

type Questions struct {
	search Retriever
	chat   llm.ChatProvider
}

func NewQuestions(search Retriever, chat llm.ChatProvider) *Questions {
	return &Questions{search: search, chat: chat}
}

func (q *Questions) Ask(ctx context.Context, question string, ids []string) (Answer, error) {
	question = strings.TrimSpace(question)
	if err := ValidateQuestion(question, ids); err != nil {
		return Answer{}, err
	}
	if err := ctx.Err(); err != nil {
		return Answer{}, err
	}
	matches, err := q.search.Search(ctx, question, ids, llm.MaxChatSources)
	if err != nil {
		return Answer{}, err
	}
	if len(matches) > llm.MaxChatSources {
		return Answer{}, llm.ErrResponse
	}
	request := llm.ChatRequest{Question: question}
	sources := make(map[string]Source, len(matches))
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	seen := make(map[string]bool, len(matches))
	budget := llm.MaxContextBytes
	for _, m := range matches {
		if !allowed[m.DocumentID] || !documents.ValidID(m.ChunkID) || seen[m.ChunkID] || strings.TrimSpace(m.Snippet) == "" || len(m.Snippet) > llm.MaxInputBytes || !utf8.ValidString(m.Snippet) || math.IsNaN(m.Distance) || math.IsInf(m.Distance, 0) || (m.Page != nil && *m.Page < 1) {
			return Answer{}, llm.ErrResponse
		}
		seen[m.ChunkID] = true
		if len(m.Snippet) > budget {
			continue
		}
		budget -= len(m.Snippet)
		id := fmt.Sprintf("s%d", len(request.Fragments)+1)
		request.Fragments = append(request.Fragments, llm.Fragment{ID: id, Text: m.Snippet})
		sources[id] = Source{DocumentID: m.DocumentID, ChunkID: m.ChunkID, Page: m.Page, Snippet: m.Snippet}
	}
	if len(request.Fragments) == 0 {
		return insufficient(), nil
	}
	result, err := q.chat.Chat(ctx, request)
	if err != nil {
		return Answer{}, err
	}
	if err := ctx.Err(); err != nil {
		return Answer{}, err
	}
	if err := llm.ValidateChatResult(request, result); err != nil {
		return Answer{}, err
	}
	if result.InsufficientContext {
		return insufficient(), nil
	}
	out := Answer{Answer: strings.TrimSpace(result.Answer), Sources: make([]Source, 0, len(result.SourceIDs))}
	for _, id := range result.SourceIDs {
		out.Sources = append(out.Sources, sources[id])
	}
	return out, nil
}

func insufficient() Answer { return Answer{Answer: InsufficientAnswer, Sources: []Source{}} }

func ValidateQuestion(question string, ids []string) error {
	if strings.TrimSpace(question) == "" || len(question) > llm.MaxInputBytes || !utf8.ValidString(question) || len(ids) < 1 || len(ids) > MaxDocuments {
		return ErrInvalid
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !documents.ValidID(id) || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	return nil
}
