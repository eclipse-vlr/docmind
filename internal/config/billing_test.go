package config

import (
	"context"
	"docmind/internal/usage"
	"testing"
)

type billingStore struct{}

func (billingStore) ReserveUsage(context.Context, int64, usage.Request) (int64, error) { return 1, nil }
func (billingStore) FinishUsage(context.Context, int64, *usage.Tokens) error           { return nil }
func (billingStore) UsageSnapshot(context.Context, int64) (usage.Snapshot, error) {
	return usage.Snapshot{}, nil
}

type attachTest struct{ attached bool }

func (p *attachTest) SetUsageMeter(*usage.Meter) { p.attached = true }
func TestBillingConfig(t *testing.T) {
	p := &attachTest{}
	if _, err := Billing(func(string) string { return "" }, billingStore{}, p); err != nil || !p.attached {
		t.Fatal(err)
	}
	for _, setting := range []string{"AI_DAILY_BUDGET_RUB", "AI_CHAT_INPUT_RUB_PER_1K", "AI_CHAT_OUTPUT_RUB_PER_1K", "AI_EMBEDDING_RUB_PER_1K"} {
		for _, value := range []string{"-1", "0", "NaN", "1000001", "0.000000001"} {
			if setting == "AI_DAILY_BUDGET_RUB" && value == "0.000000001" {
				continue
			}
			_, err := Billing(func(name string) string {
				if name == setting {
					return value
				}
				return ""
			}, billingStore{})
			if err == nil {
				t.Fatalf("accepted %s=%s", setting, value)
			}
		}
	}
}
