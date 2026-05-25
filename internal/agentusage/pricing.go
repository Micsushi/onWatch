package agentusage

import (
	"encoding/json"
	"math"
	"os"
	"strings"
)

type PricingMap struct {
	entries map[string]pricingEntry
}

type pricingEntry struct {
	InputCost         float64 `json:"input_cost_per_token"`
	OutputCost        float64 `json:"output_cost_per_token"`
	CacheReadCost     float64 `json:"cache_read_input_token_cost"`
	CacheCreationCost float64 `json:"cache_creation_input_token_cost"`
}

func NewPricingMapFromJSON(data []byte) (*PricingMap, error) {
	var raw map[string]pricingEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	entries := make(map[string]pricingEntry, len(raw))
	for model, entry := range raw {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" || model == "sample_spec" {
			continue
		}
		entries[model] = entry
	}
	return &PricingMap{entries: entries}, nil
}

func LoadPricingMapFromEnv() (*PricingMap, error) {
	if path := strings.TrimSpace(os.Getenv("ONWATCH_AGENT_USAGE_PRICING_JSON")); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return NewPricingMapFromJSON(data)
	}
	return DefaultPricingMap()
}

func DefaultPricingMap() (*PricingMap, error) {
	return NewPricingMapFromJSON([]byte(`{
		"claude-sonnet-4-5": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"cache_read_input_token_cost": 0.0000003,
			"cache_creation_input_token_cost": 0.00000375
		},
		"claude-sonnet-4-20250514": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"cache_read_input_token_cost": 0.0000003,
			"cache_creation_input_token_cost": 0.00000375
		},
		"claude-sonnet-4-6": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"cache_read_input_token_cost": 0.0000003,
			"cache_creation_input_token_cost": 0.00000375
		},
		"gpt-5.5": {
			"input_cost_per_token": 0.000005,
			"output_cost_per_token": 0.00003,
			"cache_read_input_token_cost": 0.0000005,
			"cache_creation_input_token_cost": 0.000005
		},
		"gpt-5.2-codex": {
			"input_cost_per_token": 0.00000175,
			"output_cost_per_token": 0.000014,
			"cache_read_input_token_cost": 0.000000175,
			"cache_creation_input_token_cost": 0.00000175
		},
		"google/gemini-2.5-pro": {
			"input_cost_per_token": 0.00000125,
			"output_cost_per_token": 0.00001,
			"cache_read_input_token_cost": 0.000000125,
			"cache_creation_input_token_cost": 0.0000015625
		},
		"gemini-2.5-flash": {
			"input_cost_per_token": 0.0000003,
			"output_cost_per_token": 0.0000025,
			"cache_read_input_token_cost": 0.00000003,
			"cache_creation_input_token_cost": 0.000000375
		}
	}`))
}

func (p *PricingMap) CalculateCost(model string, counts TokenCounts, opts CostOptions) float64 {
	if p == nil {
		return 0
	}
	entry, ok := p.lookup(model, opts.ProviderPrefixes)
	if !ok {
		return 0
	}

	cacheRead := entry.CacheReadCost
	cacheCreation := entry.CacheCreationCost
	cost := float64(counts.InputTokens)*entry.InputCost +
		float64(counts.CachedInputTokens)*cacheRead +
		float64(counts.CacheCreationTokens)*cacheCreation +
		float64(counts.OutputTokens)*entry.OutputCost
	if opts.ReasoningBilledAsOutput {
		cost += float64(counts.ReasoningTokens) * entry.OutputCost
	}
	if opts.CostMultiplier > 0 {
		cost *= opts.CostMultiplier
	}
	return math.Round(cost*1e12) / 1e12
}

func (p *PricingMap) lookup(model string, prefixes []string) (pricingEntry, bool) {
	key := strings.ToLower(strings.TrimSpace(model))
	if entry, ok := p.entries[key]; ok {
		return entry, true
	}
	for _, prefix := range prefixes {
		prefix = strings.Trim(strings.ToLower(strings.TrimSpace(prefix)), "/")
		if prefix == "" {
			continue
		}
		if entry, ok := p.entries[prefix+"/"+key]; ok {
			return entry, true
		}
	}
	for candidate, entry := range p.entries {
		if strings.HasSuffix(candidate, "/"+key) {
			return entry, true
		}
	}
	return pricingEntry{}, false
}
