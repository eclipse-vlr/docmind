package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func chatInput() ChatRequest {
	return ChatRequest{Question: "PostgreSQL", Fragments: []Fragment{{ID: "s1", Text: "PostgreSQL хранит документы."}, {ID: "s2", Text: "Cats and dogs."}}}
}

func TestFakeChat(t *testing.T) {
	in := chatInput()
	out, err := (FakeChat{}).Chat(context.Background(), in)
	if err != nil || out.InsufficientContext || out.Answer != in.Fragments[0].Text || len(out.SourceIDs) != 1 || out.SourceIDs[0] != "s1" {
		t.Fatalf("result=%+v error=%v", out, err)
	}
	in.Question = "astronomy"
	out, err = (FakeChat{}).Chat(context.Background(), in)
	if err != nil || !out.InsufficientContext || len(out.SourceIDs) != 0 {
		t.Fatal("fake invented an answer", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (FakeChat{}).Chat(ctx, in); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestChatInputValidation(t *testing.T) {
	for _, change := range []func(*ChatRequest){
		func(r *ChatRequest) { r.Question = " " }, func(r *ChatRequest) { r.Question = strings.Repeat("a", MaxInputBytes+1) },
		func(r *ChatRequest) { r.Question = "\xff" }, func(r *ChatRequest) { r.Fragments = nil },
		func(r *ChatRequest) { r.Fragments[0].ID = "invented" }, func(r *ChatRequest) { r.Fragments[1].ID = "s1" },
		func(r *ChatRequest) { r.Fragments[0].Text = "" }, func(r *ChatRequest) { r.Fragments[0].Text = "\xff" },
		func(r *ChatRequest) { r.Fragments[0].Text = strings.Repeat("x", MaxInputBytes+1) },
		func(r *ChatRequest) {
			r.Fragments = nil
			for i := 0; i < 5; i++ {
				r.Fragments = append(r.Fragments, Fragment{ID: string([]byte{'s', byte('1' + i)}), Text: strings.Repeat("x", 4000)})
			}
		},
	} {
		in := chatInput()
		change(&in)
		if _, err := (FakeChat{}).Chat(context.Background(), in); !errors.Is(err, ErrInput) {
			t.Fatal("invalid input accepted", err)
		}
	}
}

func TestChatCitationsValidation(t *testing.T) {
	for _, out := range []ChatResult{
		{Answer: "answer", SourceIDs: []string{"invented"}},
		{Answer: "answer", SourceIDs: []string{"s1", "s1"}},
		{Answer: "answer"}, {SourceIDs: []string{"s1"}},
		{Answer: strings.Repeat("a", MaxAnswerBytes+1), SourceIDs: []string{"s1"}},
		{Answer: "\xff", SourceIDs: []string{"s1"}},
		{InsufficientContext: true, SourceIDs: []string{"s1"}},
	} {
		if err := ValidateChatResult(chatInput(), out); !errors.Is(err, ErrResponse) {
			t.Fatal("invalid response accepted", err)
		}
	}
}
