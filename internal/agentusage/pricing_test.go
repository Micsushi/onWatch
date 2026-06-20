package agentusage

import "testing"

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
