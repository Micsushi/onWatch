package subscription

import "github.com/onllm-dev/onwatch/v2/internal/agentusage"

// Standard API rates are dated in docs/subscription-value.md.
// API basis is current Standard short-context, intentionally independent of Codex speed.
func Prices() *agentusage.PricingMap {
	p, _ := agentusage.NewPricingMapFromJSON([]byte(`{
"claude-fable-5-2":{"input_cost_per_token":0.000008,"cache_read_input_token_cost":0.0000002,"cache_creation_input_token_cost":0.00001,"cache_creation_1h_input_token_cost":0.000016,"output_cost_per_token":0.00004},
"claude-opus-5-5":{"input_cost_per_token":0.000004,"cache_read_input_token_cost":0.0000002,"cache_creation_input_token_cost":0.000005,"cache_creation_1h_input_token_cost":0.000008,"output_cost_per_token":0.00002},
"claude-haiku-5-5":{"input_cost_per_token":0.0000001,"cache_read_input_token_cost":0.00000001,"cache_creation_input_token_cost":0.000000125,"cache_creation_1h_input_token_cost":0.0000002,"output_cost_per_token":0.0000005},
"claude-sonnet-5-5":{"input_cost_per_token":0.000002,"cache_read_input_token_cost":0.0000001,"cache_creation_input_token_cost":0.0000025,"cache_creation_1h_input_token_cost":0.000004,"output_cost_per_token":0.00001},
"gemini-4-hydrogen":{"input_cost_per_token":0.0000015,"cache_read_input_token_cost":0.000000075,"output_cost_per_token":0.0000075},
"gemini-4-hydrogen-medium":{"input_cost_per_token":0.0000015,"cache_read_input_token_cost":0.000000075,"output_cost_per_token":0.0000075},
"gemini-4-hydrogen-high":{"input_cost_per_token":0.0000015,"cache_read_input_token_cost":0.000000075,"output_cost_per_token":0.0000075},
"gemini-4-neon":{"input_cost_per_token":0.000003,"cache_read_input_token_cost":0.00000015,"output_cost_per_token":0.000015},
"gpt-6-luna":{"input_cost_per_token":0.0000001,"cache_read_input_token_cost":0.00000001,"cache_creation_input_token_cost":0.000000125,"output_cost_per_token":0.0000005},
"gpt-6-sol":{"input_cost_per_token":0.000002,"cache_read_input_token_cost":0.0000002,"cache_creation_input_token_cost":0.0000025,"output_cost_per_token":0.00001},
"gpt-6.1-sol":{"input_cost_per_token":0.000002,"cache_read_input_token_cost":0.0000001,"cache_creation_input_token_cost":0.0000025,"output_cost_per_token":0.00001},
"gpt-6.1-terra":{"input_cost_per_token":0.000001,"cache_read_input_token_cost":0.00000005,"cache_creation_input_token_cost":0.00000125,"output_cost_per_token":0.000005},
"gpt-6.2-sol":{"input_cost_per_token":0.000002,"cache_read_input_token_cost":0.0000001,"cache_creation_input_token_cost":0.0000025,"output_cost_per_token":0.00001},
"gpt-6.2-astra":{"input_cost_per_token":0.000008,"cache_read_input_token_cost":0.0000004,"cache_creation_input_token_cost":0.00001,"output_cost_per_token":0.00004},
"gpt-6-astra":{"input_cost_per_token":0.00001,"cache_read_input_token_cost":0.000001,"cache_creation_input_token_cost":0.0000125,"output_cost_per_token":0.00005},
"gpt-5.6-sol":{"input_cost_per_token":0.000005,"cache_read_input_token_cost":0.0000005,"cache_creation_input_token_cost":0.00000625,"output_cost_per_token":0.00003},
"gpt-5.6-terra":{"input_cost_per_token":0.000002,"cache_read_input_token_cost":0.0000002,"cache_creation_input_token_cost":0.0000025,"output_cost_per_token":0.000012},
"gpt-5.6-luna":{"input_cost_per_token":0.0000002,"cache_read_input_token_cost":0.00000002,"cache_creation_input_token_cost":0.00000025,"output_cost_per_token":0.0000012}}`))
	return p
}
func CodexCredits(model, speed string, c agentusage.TokenCounts) (float64, bool) {
	rates, ok := map[string][3]float64{"gpt-6-astra": {250, 25, 1250}, "gpt-5.6-sol": {100, 10, 500}, "gpt-5.6-terra": {50, 5, 300}, "gpt-5.6-luna": {5, .5, 30}, "gpt-5.5": {125, 12.5, 750}, "gpt-5.4": {62.5, 6.25, 375}}[model]
	if !ok || speed == "unknown" || speed == "" {
		return 0, false
	}
	mult := 1.0
	if speed == "fast" {
		mult = 2.5
		if model == "gpt-5.4" {
			mult = 2
		}
	}
	return (float64(c.InputTokens+c.CacheCreationTokens)*rates[0] + float64(c.CachedInputTokens)*rates[1] + float64(c.OutputTokens)*rates[2]) / 1e6 * mult, true
}
