// Package usage accounts for paid requests without storing prompts or credentials.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var ErrBudget = errors.New("daily AI budget exceeded")
var ErrUnavailable = errors.New("AI usage accounting unavailable")

const NanoRub int64 = 1_000_000_000
const MaxTokens int64 = 1_000_000

type Rates struct{ Input, Output, Embedding int64 }
type Request struct {
	Kind                            string
	Reserved, InputRate, OutputRate int64
}
type Tokens struct{ Input, Output int64 }
type Snapshot struct {
	Day                                                                                     string
	Limit, Estimated, Reserved                                                              int64
	Calls, Recorded, Unknown, Pending, InputTokens, OutputTokens, EmbeddingTokens, Overruns int64
}
type Summary struct {
	Day             string `json:"day_utc"`
	Limit           string `json:"limit_rub"`
	Estimated       string `json:"estimated_rub"`
	Reserved        string `json:"reserved_rub"`
	Remaining       string `json:"remaining_rub"`
	Calls           int64  `json:"calls"`
	Recorded        int64  `json:"recorded_calls"`
	Unknown         int64  `json:"unknown_calls"`
	Pending         int64  `json:"pending_calls"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	EmbeddingTokens int64  `json:"embedding_tokens"`
	Overruns        int64  `json:"reservation_overruns"`
}

type Store interface {
	ReserveUsage(context.Context, int64, Request) (int64, error)
	FinishUsage(context.Context, int64, *Tokens) error
	UsageSnapshot(context.Context, int64) (Snapshot, error)
}

type Meter struct {
	store Store
	limit int64
	rates Rates
}

// Observe stores only bounded technical metadata; accounting is independent.
func (m *Meter) Observe(id int64, model, requestID string, elapsed time.Duration, status int) {
	if m == nil {
		return
	}
	if store, ok := m.store.(interface {
		ObserveUsage(context.Context, int64, string, string, time.Duration, int) error
	}); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = store.ObserveUsage(ctx, id, model, requestID, elapsed, status)
	}
}

func New(store Store, limit int64, rates Rates) (*Meter, error) {
	if store == nil || limit <= 0 || limit > 1_000_000*NanoRub {
		return nil, errors.New("invalid AI budget")
	}
	for _, rate := range []int64{rates.Input, rates.Output, rates.Embedding} {
		if rate <= 0 || rate > 1_000_000_000 {
			return nil, errors.New("invalid AI price")
		}
	}
	return &Meter{store: store, limit: limit, rates: rates}, nil
}

// Begin reserves an intentionally conservative input allowance. It is not a
// guarantee about a provider's tokenizer, hidden protocol or future price changes.
func (m *Meter) Begin(ctx context.Context, kind string, bodyBytes int) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var request Request
	switch kind {
	case "chat":
		if bodyBytes < 0 || bodyBytes > 256<<10 {
			return 0, ErrUnavailable
		}
		request = Request{Kind: kind, InputRate: m.rates.Input, OutputRate: m.rates.Output}
		request.Reserved = (int64(bodyBytes)+4096)*request.InputRate + 2048*request.OutputRate
	case "embedding_doc", "embedding_query":
		request = Request{Kind: kind, InputRate: m.rates.Embedding, Reserved: 8192 * m.rates.Embedding}
	default:
		return 0, ErrUnavailable
	}
	id, err := m.store.ReserveUsage(ctx, m.limit, request)
	if err != nil && ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if errors.Is(err, ErrBudget) {
		return 0, err
	}
	if err != nil {
		return 0, ErrUnavailable
	}
	return id, nil
}

// Finish uses its own bounded context: request cancellation must not discard the
// cost of a response already received. Failed persistence keeps the durable hold.
func (m *Meter) Finish(id int64, kind string, raw []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.store.FinishUsage(ctx, id, decodeTokens(kind, raw)); err != nil {
		return ErrUnavailable
	}
	return nil
}

func decodeTokens(kind string, raw []byte) *Tokens {
	var response struct {
		Usage *struct {
			Input  *int64 `json:"input_tokens"`
			Output *int64 `json:"output_tokens"`
			Prompt *int64 `json:"prompt_tokens"`
			Total  *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Usage == nil {
		return nil
	}
	u := response.Usage
	var tokens Tokens
	if kind == "chat" {
		if u.Input == nil || u.Output == nil {
			return nil
		}
		tokens = Tokens{*u.Input, *u.Output}
	} else {
		if u.Prompt == nil {
			return nil
		}
		tokens.Input = *u.Prompt
	}
	if tokens.Input < 0 || tokens.Output < 0 || tokens.Input > MaxTokens || tokens.Output > MaxTokens || (u.Total != nil && *u.Total != tokens.Input+tokens.Output) {
		return nil
	}
	return &tokens
}

func (m *Meter) Summary(ctx context.Context) (Summary, error) {
	s, err := m.store.UsageSnapshot(ctx, m.limit)
	if err != nil {
		return Summary{}, ErrUnavailable
	}
	remaining := max(s.Limit-s.Estimated-s.Reserved, 0)
	if s.Overruns > 0 {
		remaining = 0
	}
	return Summary{Day: s.Day, Limit: FormatRub(s.Limit), Estimated: FormatRub(s.Estimated), Reserved: FormatRub(s.Reserved), Remaining: FormatRub(remaining), Calls: s.Calls, Recorded: s.Recorded, Unknown: s.Unknown, Pending: s.Pending, InputTokens: s.InputTokens, OutputTokens: s.OutputTokens, EmbeddingTokens: s.EmbeddingTokens, Overruns: s.Overruns}, nil
}

// ParseRub accepts a decimal amount, never binary floating point or exponents.
func ParseRub(value string) (int64, error) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 7 {
		return 0, errors.New("invalid ruble amount")
	}
	digits := parts[0]
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if len(fraction) == 0 || len(fraction) > 9 {
			return 0, errors.New("invalid ruble amount")
		}
		digits += fraction
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid ruble amount")
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > 1_000_000 {
		return 0, errors.New("invalid ruble amount")
	}
	frac := int64(0)
	if fraction != "" {
		frac, _ = strconv.ParseInt(fraction+strings.Repeat("0", 9-len(fraction)), 10, 64)
	}
	result := whole*NanoRub + frac
	if result > 1_000_000*NanoRub {
		return 0, errors.New("invalid ruble amount")
	}
	return result, nil
}

func FormatRub(amount int64) string { return fmt.Sprintf("%d.%09d", amount/NanoRub, amount%NanoRub) }
