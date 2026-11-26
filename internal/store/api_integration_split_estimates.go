package store

import (
	"fmt"
	"math"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/agentusage"
)

type APIIntegrationUsageGroup struct {
	IntegrationName, Provider, AccountName, Model, ReasoningEffort, Mode, SpeedMode string
}

type APIIntegrationSplitEstimate struct {
	Tokens                   int     `json:"tokens"`
	CostUSD                  float64 `json:"costUsd"`
	ArchivedTokens           int     `json:"archivedTokens"`
	LikelyContextTokens      int     `json:"likelyContextTokens"`
	ProviderMixTokens        int     `json:"providerMixTokens"`
	InputTokens              int     `json:"inputTokens"`
	CachedInputTokens        int     `json:"cachedInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	ReasoningTokens          int     `json:"reasoningTokens"`
}

func (e *APIIntegrationSplitEstimate) Add(other APIIntegrationSplitEstimate) {
	e.Tokens += other.Tokens
	e.CostUSD += other.CostUSD
	e.ArchivedTokens += other.ArchivedTokens
	e.LikelyContextTokens += other.LikelyContextTokens
	e.ProviderMixTokens += other.ProviderMixTokens
	e.InputTokens += other.InputTokens
	e.CachedInputTokens += other.CachedInputTokens
	e.CacheCreationInputTokens += other.CacheCreationInputTokens
	e.OutputTokens += other.OutputTokens
	e.ReasoningTokens += other.ReasoningTokens
}

// EstimateAPIIntegrationMissingSplits prices total-only records without changing the stored usage.
func (s *Store) EstimateAPIIntegrationMissingSplits(start, end time.Time, integration string) (map[APIIntegrationUsageGroup]APIIntegrationSplitEstimate, error) {
	const supported = "integration_name IN ('Codex CLI', 'Claude Code', 'Gemini CLI', 'Antigravity')"
	const group = "integration_name, provider, account_name, model, reasoning_effort, mode, speed_mode"
	const split = "input_tokens + cached_input_tokens + cache_creation_input_tokens + output_tokens + reasoning_output_tokens"
	startRaw, endRaw := start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)
	// Older Codex records lost the compaction event and cumulative counters. Their quota
	// metadata still identifies the total-only records as Codex token-count messages.
	const codexContext = `CASE WHEN integration_name = 'Codex CLI' AND json_valid(metadata_json)
		THEN json_type(metadata_json, '$.quota_telemetry') IS NOT NULL
			AND json_type(metadata_json, '$.codex_usage_counter_advanced') IS NOT 'true' ELSE 0 END`
	query := `SELECT ` + group + `, captured_at, total_tokens, 1, 0, ` + codexContext + `
		FROM api_integration_usage_events
		WHERE captured_at COLLATE ONWATCH_RFC3339 >= ? AND captured_at COLLATE ONWATCH_RFC3339 < ?
		AND (? = '' OR integration_name = ?) AND ` + supported + `
		AND cost_usd IS NULL AND total_tokens > 0 AND prompt_tokens + completion_tokens + ` + split + ` = 0
		UNION ALL
		SELECT ` + group + `, last_captured_at, total_tokens, request_count, 1, 0
		FROM api_integration_usage_hourly
		WHERE hour_start < ? AND last_captured_at COLLATE ONWATCH_RFC3339 >= ?
		AND (? = '' OR integration_name = ?) AND ` + supported + `
		AND total_cost_usd = 0 AND request_count > 0 AND total_tokens > 0 AND prompt_tokens + completion_tokens + ` + split + ` = 0`
	rows, err := s.db.Query(query, startRaw, endRaw, integration, integration, endRaw, startRaw, integration, integration)
	if err != nil {
		return nil, fmt.Errorf("query total-only API usage: %w", err)
	}
	defer rows.Close()
	type record struct {
		group            APIIntegrationUsageGroup
		at               time.Time
		tokens, requests int
		archived         bool
	}
	var records []record
	estimates := make(map[APIIntegrationUsageGroup]APIIntegrationSplitEstimate)
	var first, last time.Time
	for rows.Next() {
		var usage record
		var stamp string
		var likelyContext bool
		g := &usage.group
		if err := rows.Scan(&g.IntegrationName, &g.Provider, &g.AccountName, &g.Model, &g.ReasoningEffort, &g.Mode, &g.SpeedMode,
			&stamp, &usage.tokens, &usage.requests, &usage.archived, &likelyContext); err != nil {
			return nil, err
		}
		if likelyContext {
			estimate := estimates[usage.group]
			estimate.LikelyContextTokens += usage.tokens
			estimates[usage.group] = estimate
			continue
		}
		usage.at, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return nil, fmt.Errorf("parse total-only usage timestamp: %w", err)
		}
		if first.IsZero() || usage.at.Before(first) {
			first = usage.at
		}
		if usage.at.After(last) {
			last = usage.at
		}
		records = append(records, usage)
	}
	if err := rows.Err(); err != nil || len(records) == 0 {
		return estimates, err
	}
	pricing, err := centralAgentPricing()
	if err != nil {
		return nil, err
	}

	// Whole UTC months keep the reference mix independent of the selected chart window.
	firstMonth := time.Date(first.UTC().Year(), first.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	lastMonth := time.Date(last.UTC().Year(), last.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	from := firstMonth.AddDate(0, -1, 0).Format(time.RFC3339)
	to := lastMonth.AddDate(0, 2, 0).Format(time.RFC3339)
	const columns = "integration_name, model, input_tokens, cached_input_tokens, cache_creation_input_tokens, output_tokens, reasoning_output_tokens"
	reference, err := s.db.Query(`WITH usage AS (
		SELECT strftime('%Y-%m',captured_at) AS month, `+columns+`
		FROM api_integration_usage_events
		WHERE captured_at COLLATE ONWATCH_RFC3339 >= ? AND captured_at COLLATE ONWATCH_RFC3339 < ?
		AND (? = '' OR integration_name = ?) AND `+supported+` AND `+split+` > 0
		UNION ALL
		SELECT strftime('%Y-%m',hour_start) AS month, `+columns+`
		FROM api_integration_usage_hourly
		WHERE hour_start >= ? AND hour_start < ?
		AND (? = '' OR integration_name = ?) AND `+supported+` AND `+split+` > 0
	)
	SELECT month, integration_name, model, sum(input_tokens), sum(cached_input_tokens),
		sum(cache_creation_input_tokens), sum(output_tokens), sum(reasoning_output_tokens)
	FROM usage GROUP BY month, integration_name, model`,
		from, to, integration, integration, from, to, integration, integration)
	if err != nil {
		return nil, fmt.Errorf("query API token split samples: %w", err)
	}
	defer reference.Close()
	type cohort struct{ month, integration, model string }
	modelMix := make(map[cohort][5]int)
	providerMix := make(map[cohort][5]int)
	for reference.Next() {
		var key cohort
		var mix [5]int
		if err := reference.Scan(&key.month, &key.integration, &key.model, &mix[0], &mix[1], &mix[2], &mix[3], &mix[4]); err != nil {
			return nil, err
		}
		// Codex and Claude output already includes reasoning.
		if key.integration == "Codex CLI" || key.integration == "Claude Code" {
			mix[4] = 0
		}
		modelMix[key] = mix
		key.model = ""
		total := providerMix[key]
		for i, count := range mix {
			total[i] += count
		}
		providerMix[key] = total
	}
	if err := reference.Err(); err != nil {
		return nil, err
	}

	for _, usage := range records {
		g := usage.group
		at := usage.at.UTC()
		month := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
		months := []string{month.Format("2006-01"), month.AddDate(0, -1, 0).Format("2006-01"), month.AddDate(0, 1, 0).Format("2006-01")}
		var mix [5]int
		for _, date := range months {
			mix = modelMix[cohort{date, g.IntegrationName, g.Model}]
			if mix != [5]int{} {
				break
			}
		}
		usingProvider := mix == [5]int{}
		if usingProvider {
			for _, date := range months {
				mix = providerMix[cohort{date, g.IntegrationName, ""}]
				if mix != [5]int{} {
					break
				}
			}
		}
		if mix == [5]int{} {
			continue
		}
		// Hourly records retain the number of requests, so price their mean request size.
		perRequest := usage.tokens / usage.requests
		remainder := usage.tokens % usage.requests
		var projected APIIntegrationSplitEstimate
		for offset, requests := range []int{usage.requests - remainder, remainder} {
			if requests == 0 {
				continue
			}
			counts := splitEstimateCounts(perRequest+offset, mix)
			projected.CostUSD += float64(requests) * pricing.EstimateLoggedCost(g.IntegrationName, g.Model, g.SpeedMode, usage.at, counts)
			projected.InputTokens += requests * counts.InputTokens
			projected.CachedInputTokens += requests * counts.CachedInputTokens
			projected.CacheCreationInputTokens += requests * counts.CacheCreationTokens
			projected.OutputTokens += requests * counts.OutputTokens
			projected.ReasoningTokens += requests * counts.ReasoningTokens
		}
		if projected.CostUSD <= 0 {
			continue
		}
		projected.Tokens = usage.tokens
		if usingProvider {
			projected.ProviderMixTokens = usage.tokens
		}
		if usage.archived {
			projected.ArchivedTokens = usage.tokens
		}
		estimate := estimates[g]
		estimate.Add(projected)
		estimates[g] = estimate
	}
	return estimates, nil
}

func splitEstimateCounts(tokens int, mix [5]int) agentusage.TokenCounts {
	total := mix[0] + mix[1] + mix[2] + mix[3] + mix[4]
	var counts [5]int
	var weight, allocated int
	for i, count := range mix {
		weight += count
		next := int(math.Round(float64(tokens) * (float64(weight) / float64(total))))
		counts[i] = next - allocated
		allocated = next
	}
	return agentusage.TokenCounts{
		InputTokens: counts[0], CachedInputTokens: counts[1], CacheCreationTokens: counts[2],
		OutputTokens: counts[3], ReasoningTokens: counts[4], TotalTokens: tokens,
	}
}
