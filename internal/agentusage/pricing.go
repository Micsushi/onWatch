package agentusage

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

// GoogleFamilyProviderPrefixes is the union of every provider prefix the
// Gemini/Antigravity parsers may apply when looking up model pricing. The
// collector uses it to decide whether an unpriced model is genuinely absent
// from the pricing map (vs. just missing a prefix), and the backfill script
// uses it for the same gemini/antigravity lookups.
var GoogleFamilyProviderPrefixes = []string{"google", "gemini", "vertex_ai", "openrouter/google", "anthropic", "openai"}

type PricingMap struct {
	entries map[string][]pricingPeriod
}

type pricingEntry struct {
	InputCost           float64 `json:"input_cost_per_token"`
	OutputCost          float64 `json:"output_cost_per_token"`
	CacheReadCost       float64 `json:"cache_read_input_token_cost"`
	CacheCreationCost   float64 `json:"cache_creation_input_token_cost"`
	CacheCreation1hCost float64 `json:"cache_creation_1h_input_token_cost"`
}

type pricingPeriod struct {
	EffectiveFrom time.Time
	Entry         pricingEntry
}

type pricingConfig struct {
	EffectiveFrom       string           `json:"effective_from"`
	InputCost           float64          `json:"input_cost_per_token"`
	OutputCost          float64          `json:"output_cost_per_token"`
	CacheReadCost       float64          `json:"cache_read_input_token_cost"`
	CacheCreationCost   float64          `json:"cache_creation_input_token_cost"`
	CacheCreation1hCost float64          `json:"cache_creation_1h_input_token_cost"`
	History             *[]pricingConfig `json:"history"`
}

func NewPricingMapFromJSON(data []byte) (*PricingMap, error) {
	var raw map[string]pricingConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	entries := make(map[string][]pricingPeriod, len(raw))
	for model, config := range raw {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" || model == "sample_spec" {
			continue
		}
		var configs []pricingConfig
		if config.History != nil {
			if len(*config.History) == 0 {
				return nil, fmt.Errorf("agentusage: pricing %s history is empty", model)
			}
			configs = *config.History
		} else {
			configs = []pricingConfig{config}
		}
		periods := make([]pricingPeriod, 0, len(configs))
		for _, candidate := range configs {
			effectiveFrom := time.Time{}
			if strings.TrimSpace(candidate.EffectiveFrom) != "" {
				parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(candidate.EffectiveFrom))
				if err != nil {
					return nil, fmt.Errorf("agentusage: pricing %s effective_from: %w", model, err)
				}
				effectiveFrom = parsed.UTC()
			}
			periods = append(periods, pricingPeriod{
				EffectiveFrom: effectiveFrom,
				Entry: pricingEntry{
					InputCost:           candidate.InputCost,
					OutputCost:          candidate.OutputCost,
					CacheReadCost:       candidate.CacheReadCost,
					CacheCreationCost:   candidate.CacheCreationCost,
					CacheCreation1hCost: candidate.CacheCreation1hCost,
				},
			})
		}
		sort.SliceStable(periods, func(i, j int) bool {
			return periods[i].EffectiveFrom.Before(periods[j].EffectiveFrom)
		})
		entries[model] = periods
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
			"cache_creation_input_token_cost": 0.00000375,
			"cache_creation_1h_input_token_cost": 0.000006
		},
		"claude-sonnet-4-20250514": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"cache_read_input_token_cost": 0.0000003,
			"cache_creation_input_token_cost": 0.00000375,
			"cache_creation_1h_input_token_cost": 0.000006
		},
		"claude-sonnet-4-6": {
			"input_cost_per_token": 0.000003,
			"output_cost_per_token": 0.000015,
			"cache_read_input_token_cost": 0.0000003,
			"cache_creation_input_token_cost": 0.00000375,
			"cache_creation_1h_input_token_cost": 0.000006
		},
		"claude-opus-4-7": {
			"input_cost_per_token": 0.000005,
			"output_cost_per_token": 0.000025,
			"cache_read_input_token_cost": 0.0000005,
			"cache_creation_input_token_cost": 0.00000625,
			"cache_creation_1h_input_token_cost": 0.00001
		},
		"claude-opus-4-8": {
			"input_cost_per_token": 0.000005,
			"output_cost_per_token": 0.000025,
			"cache_read_input_token_cost": 0.0000005,
			"cache_creation_input_token_cost": 0.00000625,
			"cache_creation_1h_input_token_cost": 0.00001
		},
		"claude-opus-5": {
			"input_cost_per_token": 0.000005,
			"output_cost_per_token": 0.000025,
			"cache_read_input_token_cost": 0.0000005,
			"cache_creation_input_token_cost": 0.00000625,
			"cache_creation_1h_input_token_cost": 0.00001
		},
		"claude-haiku-4-5-20251001": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.000005,
			"cache_read_input_token_cost": 0.0000001,
			"cache_creation_input_token_cost": 0.00000125,
			"cache_creation_1h_input_token_cost": 0.000002
		},
		"claude-haiku-4-5": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.000005,
			"cache_read_input_token_cost": 0.0000001,
			"cache_creation_input_token_cost": 0.00000125,
			"cache_creation_1h_input_token_cost": 0.000002
		},
		"claude-sonnet-5": {
			"history": [{
				"input_cost_per_token": 0.000002,
				"output_cost_per_token": 0.00001,
				"cache_read_input_token_cost": 0.0000002,
				"cache_creation_input_token_cost": 0.0000025,
				"cache_creation_1h_input_token_cost": 0.000004
			}, {
				"effective_from": "2026-09-01T00:00:00Z",
				"input_cost_per_token": 0.000003,
				"output_cost_per_token": 0.000015,
				"cache_read_input_token_cost": 0.0000003,
				"cache_creation_input_token_cost": 0.00000375,
				"cache_creation_1h_input_token_cost": 0.000006
			}]
		},
		"claude-fable-5": {
			"input_cost_per_token": 0.00001,
			"output_cost_per_token": 0.00005,
			"cache_read_input_token_cost": 0.000001,
			"cache_creation_input_token_cost": 0.0000125,
			"cache_creation_1h_input_token_cost": 0.00002
		},
		"gpt-5.6-sol": {
			"history": [{
				"effective_from": "2026-06-26T00:00:00Z",
				"input_cost_per_token": 0.000005,
				"output_cost_per_token": 0.00003,
				"cache_read_input_token_cost": 0.0000005,
				"cache_creation_input_token_cost": 0.00000625
			}]
		},
		"gpt-5.6-terra": {
			"history": [{
				"effective_from": "2026-06-26T00:00:00Z",
				"input_cost_per_token": 0.0000025,
				"output_cost_per_token": 0.000015,
				"cache_read_input_token_cost": 0.00000025,
				"cache_creation_input_token_cost": 0.000003125
			}]
		},
		"gpt-5.6-luna": {
			"history": [{
				"effective_from": "2026-06-26T00:00:00Z",
				"input_cost_per_token": 0.000001,
				"output_cost_per_token": 0.000006,
				"cache_read_input_token_cost": 0.0000001,
				"cache_creation_input_token_cost": 0.00000125
			}]
		},
		"gpt-5.5": {
			"history": [{
				"effective_from": "2026-04-23T00:00:00Z",
				"input_cost_per_token": 0.000005,
				"output_cost_per_token": 0.00003,
				"cache_read_input_token_cost": 0.0000005,
				"cache_creation_input_token_cost": 0.000005
			}]
		},
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"output_cost_per_token": 0.000015,
			"cache_read_input_token_cost": 0.00000025,
			"cache_creation_input_token_cost": 0.0000025
		},
		"gpt-5.3-codex-spark": {
			"input_cost_per_token": 0.00000175,
			"output_cost_per_token": 0.000014,
			"cache_read_input_token_cost": 0.000000175,
			"cache_creation_input_token_cost": 0.00000175
		},
		"gpt-5.3-codex": {
			"input_cost_per_token": 0.00000175,
			"output_cost_per_token": 0.000014,
			"cache_read_input_token_cost": 0.000000175,
			"cache_creation_input_token_cost": 0.00000175
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
	return p.CalculateCostAt(model, time.Time{}, counts, opts)
}

// CalculateCostAt calculates cost using the price period active at the event
// timestamp. A zero timestamp selects the latest configured period.
func (p *PricingMap) CalculateCostAt(model string, at time.Time, counts TokenCounts, opts CostOptions) float64 {
	if p == nil {
		return 0
	}
	periods, ok := p.lookup(model, opts.ProviderPrefixes)
	if !ok {
		return 0
	}
	entry := periods[len(periods)-1].Entry
	if !at.IsZero() {
		entry = periods[0].Entry
		for _, period := range periods {
			if period.EffectiveFrom.After(at) {
				break
			}
			entry = period.Entry
		}
	}

	cacheRead := entry.CacheReadCost
	cacheCreation := entry.CacheCreationCost
	cacheCreation1h := entry.CacheCreation1hCost
	if cacheCreation1h <= 0 {
		cacheCreation1h = cacheCreation
	}
	cacheCreation5mTokens := counts.CacheCreationTokens - counts.CacheCreation1hTokens
	if cacheCreation5mTokens < 0 {
		cacheCreation5mTokens = 0
	}
	inputCost := float64(counts.InputTokens)*entry.InputCost +
		float64(counts.CachedInputTokens)*cacheRead +
		float64(cacheCreation5mTokens)*cacheCreation +
		float64(counts.CacheCreation1hTokens)*cacheCreation1h
	outputCost := float64(counts.OutputTokens) * entry.OutputCost
	if opts.ReasoningBilledAsOutput {
		outputCost += float64(counts.ReasoningTokens) * entry.OutputCost
	}
	if opts.InputMultiplier > 0 {
		inputCost *= opts.InputMultiplier
	}
	if opts.OutputMultiplier > 0 {
		outputCost *= opts.OutputMultiplier
	}
	cost := inputCost + outputCost
	if opts.CostMultiplier > 0 {
		cost *= opts.CostMultiplier
	}
	return math.Round(cost*1e12) / 1e12
}

// Known reports whether a price exists for the model under any of the given
// provider prefixes. Used to flag unpriced models so they do not silently cost $0.
func (p *PricingMap) Known(model string, prefixes []string) bool {
	if p == nil {
		return false
	}
	_, ok := p.lookup(model, prefixes)
	return ok
}

func (p *PricingMap) lookup(model string, prefixes []string) ([]pricingPeriod, bool) {
	key := strings.ToLower(strings.TrimSpace(model))
	if periods, ok := p.entries[key]; ok && len(periods) > 0 {
		return periods, true
	}
	for _, prefix := range prefixes {
		prefix = strings.Trim(strings.ToLower(strings.TrimSpace(prefix)), "/")
		if prefix == "" {
			continue
		}
		if periods, ok := p.entries[prefix+"/"+key]; ok && len(periods) > 0 {
			return periods, true
		}
	}
	for candidate, periods := range p.entries {
		if strings.HasSuffix(candidate, "/"+key) && len(periods) > 0 {
			return periods, true
		}
	}
	return nil, false
}
