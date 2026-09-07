package subscription

import "github.com/onllm-dev/onwatch/v2/internal/agentusage"

// Rates verified 2026-09-07. Credits are not converted to dollars.
// API basis is current Standard short-context, intentionally independent of Codex speed.
func Prices() *agentusage.PricingMap {
	p, _ := agentusage.NewPricingMapFromJSON([]byte(`{
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
