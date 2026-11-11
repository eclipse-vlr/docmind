package config

import (
	"fmt"

	"docmind/internal/usage"
)

// Billing attaches one shared durable accounting policy to every paid adapter.
// Prices are nanorubles/token internally; environment prices are RUB/1000 tokens.
func Billing(getenv func(string) string, store usage.Store, providers ...any) (*usage.Meter, error) {
	settings := []struct{ name, fallback string }{
		{"AI_DAILY_BUDGET_RUB", "100"},
		{"AI_CHAT_INPUT_RUB_PER_1K", "0.5"},
		{"AI_CHAT_OUTPUT_RUB_PER_1K", "1.2"},
		{"AI_EMBEDDING_RUB_PER_1K", "0.0101"},
	}
	amounts := make([]int64, len(settings))
	for i, s := range settings {
		value := getenv(s.name)
		if value == "" {
			value = s.fallback
		}
		amount, err := usage.ParseRub(value)
		if err != nil || amount <= 0 {
			return nil, fmt.Errorf("%s must be a positive decimal ruble amount", s.name)
		}
		if i > 0 {
			if amount%1000 != 0 {
				return nil, fmt.Errorf("%s allows at most six decimal places", s.name)
			}
			amount /= 1000
		}
		amounts[i] = amount
	}
	meter, err := usage.New(store, amounts[0], usage.Rates{Input: amounts[1], Output: amounts[2], Embedding: amounts[3]})
	if err != nil {
		return nil, err
	}
	for _, provider := range providers {
		if p, ok := provider.(interface{ SetUsageMeter(*usage.Meter) }); ok {
			p.SetUsageMeter(meter)
		}
	}
	return meter, nil
}
