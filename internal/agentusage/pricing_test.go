package agentusage

import (
	"strings"
	"testing"
	"time"
)

func TestPricingMapCalculateCostAtUsesEffectivePeriod(t *testing.T) {
	pricing, err := NewPricingMapFromJSON([]byte(`{
		"gpt-test": {
			"history": [
				{
					"effective_from": "2026-01-01T00:00:00Z",
					"input_cost_per_token": 0.000010
				},
				{
					"effective_from": "2026-07-01T00:00:00Z",
					"input_cost_per_token": 0.000005
				}
			]
		}
	}`))
	if err != nil {
		t.Fatalf("NewPricingMapFromJSON() error = %v", err)
	}

	counts := TokenCounts{InputTokens: 100}
	tests := []struct {
		name string
		at   time.Time
		want float64
	}{
		{name: "before earliest uses earliest", at: time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), want: 0.001},
		{name: "old period", at: time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC), want: 0.001},
		{name: "new period", at: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), want: 0.0005},
		{name: "zero uses latest", at: time.Time{}, want: 0.0005},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := pricing.CalculateCostAt("gpt-test", test.at, counts, CostOptions{})
			if got != test.want {
				t.Fatalf("cost = %.8f, want %.8f", got, test.want)
			}
		})
	}

	if got := pricing.CalculateCost("gpt-test", counts, CostOptions{}); got != 0.0005 {
		t.Fatalf("latest cost = %.8f, want %.8f", got, 0.0005)
	}
}

func TestPricingMapRejectsEmptyHistory(t *testing.T) {
	_, err := NewPricingMapFromJSON([]byte(`{"gpt-test":{"history":[]}}`))
	if err == nil || !strings.Contains(err.Error(), "history is empty") {
		t.Fatalf("error = %v, want empty history error", err)
	}
}

func TestPricingMapRejectsInvalidEffectiveDate(t *testing.T) {
	_, err := NewPricingMapFromJSON([]byte(`{
		"gpt-test": {
			"history": [{
				"effective_from": "not-a-date",
				"input_cost_per_token": 0.000005
			}]
		}
	}`))
	if err == nil || !strings.Contains(err.Error(), "effective_from") {
		t.Fatalf("error = %v, want effective_from error", err)
	}
}

func TestDefaultPricingMapVersionsGPTPricingByLaunchDate(t *testing.T) {
	pricing, err := DefaultPricingMap()
	if err != nil {
		t.Fatalf("DefaultPricingMap() error = %v", err)
	}

	tests := []struct {
		model string
		want  time.Time
	}{
		{model: "gpt-5.5", want: time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC)},
		{model: "gpt-5.6-sol", want: time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)},
		{model: "gpt-5.6-terra", want: time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)},
	}
	for _, test := range tests {
		periods, ok := pricing.lookup(test.model, nil)
		if !ok || len(periods) != 1 {
			t.Fatalf("%s periods = %v, want one period", test.model, periods)
		}
		if !periods[0].EffectiveFrom.Equal(test.want) {
			t.Fatalf("%s effective_from = %s, want %s", test.model, periods[0].EffectiveFrom, test.want)
		}
	}
}

func TestPricingMapCalculateCostUsesInputOutputCacheAndReasoning(t *testing.T) {
	pricing, err := NewPricingMapFromJSON([]byte(`{
		"gpt-5.2-codex": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.00001,
			"cache_read_input_token_cost": 0.0000001,
			"cache_creation_input_token_cost": 0.00000125
		}
	}`))
	if err != nil {
		t.Fatalf("NewPricingMapFromJSON() error = %v", err)
	}

	got := pricing.CalculateCost("gpt-5.2-codex", TokenCounts{
		InputTokens:         100,
		CachedInputTokens:   50,
		CacheCreationTokens: 20,
		OutputTokens:        10,
		ReasoningTokens:     5,
	}, CostOptions{ReasoningBilledAsOutput: true})

	want := 0.00028
	if got != want {
		t.Fatalf("cost = %.8f, want %.8f", got, want)
	}
}

func TestPricingMapMatchesProviderPrefixedModel(t *testing.T) {
	pricing, err := NewPricingMapFromJSON([]byte(`{
		"google/gemini-2.5-pro": {
			"input_cost_per_token": 0.00000125,
			"output_cost_per_token": 0.00001
		}
	}`))
	if err != nil {
		t.Fatalf("NewPricingMapFromJSON() error = %v", err)
	}

	got := pricing.CalculateCost("gemini-2.5-pro", TokenCounts{
		InputTokens:  1000,
		OutputTokens: 100,
	}, CostOptions{ProviderPrefixes: []string{"google"}})

	want := 0.00225
	if got != want {
		t.Fatalf("cost = %.8f, want %.8f", got, want)
	}
}

func TestPricingMapKnownReportsMissingModel(t *testing.T) {
	pricing, err := NewPricingMapFromJSON([]byte(`{
		"claude-opus-4-8": {"input_cost_per_token": 0.000005, "output_cost_per_token": 0.000025},
		"google/gemini-2.5-pro": {"input_cost_per_token": 0.00000125, "output_cost_per_token": 0.00001}
	}`))
	if err != nil {
		t.Fatalf("NewPricingMapFromJSON() error = %v", err)
	}
	if !pricing.Known("claude-opus-4-8", nil) {
		t.Fatal("expected claude-opus-4-8 to be known")
	}
	if !pricing.Known("gemini-2.5-pro", []string{"google"}) {
		t.Fatal("expected prefixed gemini-2.5-pro to be known")
	}
	if pricing.Known("claude-opus-9-9", []string{"google", "anthropic"}) {
		t.Fatal("expected unknown model to be reported missing")
	}
}

func TestPricingMapAppliesCostMultiplier(t *testing.T) {
	pricing, err := NewPricingMapFromJSON([]byte(`{
		"gpt-5.5": {
			"input_cost_per_token": 0.000005,
			"output_cost_per_token": 0.00003,
			"cache_read_input_token_cost": 0.0000005
		}
	}`))
	if err != nil {
		t.Fatalf("NewPricingMapFromJSON() error = %v", err)
	}

	got := pricing.CalculateCost("gpt-5.5", TokenCounts{
		InputTokens:       100,
		CachedInputTokens: 100,
		OutputTokens:      10,
	}, CostOptions{CostMultiplier: 2.5})

	want := 0.002125
	if got != want {
		t.Fatalf("cost = %.8f, want %.8f", got, want)
	}
}
