package llm

import (
	"context"
	"docmind/internal/usage"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

type meterStore struct {
	deny     bool
	tokens   *usage.Tokens
	finished int
	requests int
}

func (s *meterStore) ReserveUsage(context.Context, int64, usage.Request) (int64, error) {
	s.requests++
	if s.deny {
		return 0, usage.ErrBudget
	}
	return 1, nil
}
func (s *meterStore) FinishUsage(_ context.Context, _ int64, t *usage.Tokens) error {
	s.tokens = t
	s.finished++
	return nil
}
func (s *meterStore) UsageSnapshot(context.Context, int64) (usage.Snapshot, error) {
	return usage.Snapshot{}, nil
}

func TestMeterBudgetBlocksNetworkAndKeepsUnknown(t *testing.T) {
	for _, deny := range []bool{true, false} {
		store := &meterStore{deny: deny}
		meter, _ := usage.New(store, 100*usage.NanoRub, usage.Rates{Input: 1, Output: 1, Embedding: 1})
		var calls atomic.Int32
		p := testYandexEmbeddings(t, DocumentEmbedding, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) })
		p.SetUsageMeter(meter)
		_, err := p.Embed(context.Background(), []string{"small text"})
		if deny {
			if !errors.Is(err, usage.ErrBudget) || calls.Load() != 0 || store.finished != 0 {
				t.Fatal(err, calls.Load(), store)
			}
		} else {
			if !errors.Is(err, ErrUnavailable) || calls.Load() != 1 || store.finished != 1 || store.tokens != nil {
				t.Fatal(err, calls.Load(), store)
			}
		}
	}
}

func TestMeterAccountsBeforeSemanticValidation(t *testing.T) {
	store := &meterStore{}
	meter, _ := usage.New(store, 100*usage.NanoRub, usage.Rates{Input: 1, Output: 1, Embedding: 1})
	p := testYandexEmbeddings(t, DocumentEmbedding, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":7,"total_tokens":7},"data":[]}`))
	})
	p.SetUsageMeter(meter)
	_, err := p.Embed(context.Background(), []string{"text"})
	if !errors.Is(err, ErrResponse) || store.finished != 1 || store.tokens == nil || store.tokens.Input != 7 {
		t.Fatal(err, store)
	}
}
