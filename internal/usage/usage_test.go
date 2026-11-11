package usage

import (
	"context"
	"errors"
	"testing"
)

type memoryStore struct {
	request  Request
	tokens   *Tokens
	failure  error
	finished int
}

func (s *memoryStore) ReserveUsage(_ context.Context, _ int64, r Request) (int64, error) {
	s.request = r
	return 1, s.failure
}
func (s *memoryStore) FinishUsage(_ context.Context, _ int64, t *Tokens) error {
	s.tokens = t
	s.finished++
	return s.failure
}
func (s *memoryStore) UsageSnapshot(context.Context, int64) (Snapshot, error) {
	return Snapshot{Day: "2026-11-12", Limit: 100 * NanoRub, Estimated: 5 * NanoRub, Reserved: 3 * NanoRub}, s.failure
}

func TestMoneyAndMeter(t *testing.T) {
	for _, text := range []string{"0", "0.0101", "0.5", "100", "1000000"} {
		n, err := ParseRub(text)
		if err != nil {
			t.Fatal(text, err)
		}
		back, err := ParseRub(FormatRub(n))
		if err != nil || back != n {
			t.Fatal(text, n, back, err)
		}
	}
	for _, text := range []string{"-1", "+1", "1e2", "1,5", ".5", "1.", "NaN", "1000000.1", "999999999999999999999", "0.1234567890"} {
		if _, err := ParseRub(text); err == nil {
			t.Fatal("accepted", text)
		}
	}
	store := &memoryStore{}
	m, err := New(store, 100*NanoRub, Rates{Input: 500000, Output: 1200000, Embedding: 10100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Begin(context.Background(), "embedding_doc", 30); err != nil || store.request.Reserved != 8192*10100 {
		t.Fatal(store.request, err)
	}
	if _, err = m.Begin(context.Background(), "chat", 100); err != nil || store.request.Reserved != (100+4096)*500000+2048*1200000 {
		t.Fatal(store.request, err)
	}
	summary, err := m.Summary(context.Background())
	if err != nil || summary.Remaining != "92.000000000" {
		t.Fatal(summary, err)
	}
	store.failure = ErrBudget
	if _, err = m.Begin(context.Background(), "chat", 100); !errors.Is(err, ErrBudget) {
		t.Fatal(err)
	}
}

func TestUsageMetadataIsNeverGuessed(t *testing.T) {
	for _, tc := range []struct {
		kind, body    string
		input, output int64
		valid         bool
	}{
		{"chat", `{"usage":{"input_tokens":14,"output_tokens":3,"total_tokens":17,"input_tokens_details":{"cached_tokens":4}}}`, 14, 3, true},
		{"embedding_doc", `{"usage":{"prompt_tokens":20,"total_tokens":20}}`, 20, 0, true},
		{"embedding_query", `{"usage":{"prompt_tokens":0}}`, 0, 0, true},
		{"chat", `{}`, 0, 0, false}, {"chat", `{"usage":{"input_tokens":null,"output_tokens":3}}`, 0, 0, false},
		{"chat", `{"usage":{"input_tokens":-1,"output_tokens":3}}`, 0, 0, false},
		{"chat", `{"usage":{"input_tokens":1,"output_tokens":3,"total_tokens":5}}`, 0, 0, false},
		{"embedding_doc", `{"usage":{"prompt_tokens":1000001}}`, 0, 0, false},
		{"embedding_doc", `{"usage":{"prompt_tokens":2.5}}`, 0, 0, false},
	} {
		got := decodeTokens(tc.kind, []byte(tc.body))
		if (got != nil) != tc.valid || (got != nil && (got.Input != tc.input || got.Output != tc.output)) {
			t.Fatalf("%s: %+v", tc.body, got)
		}
	}
}
