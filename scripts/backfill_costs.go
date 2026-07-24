//go:build ignore

// Fills missing generated agent usage costs without changing stored historical
// costs. Uses each row's captured_at timestamp and metadata_json token breakdown
// to avoid double-counting prompt_tokens, which is stored as
// input+cached+cacheCreation combined.

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/agentusage"
	_ "modernc.org/sqlite"
)

type row struct {
	id, input, cached, cacheCreation, cacheCreation1h, output, reasoning int
	integration, provider, model, metadata                               string
	capturedAt                                                           time.Time
}

func main() {
	home := os.Getenv("USERPROFILE")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	dbPath := filepath.Join(home, ".onwatch", "data", "onwatch.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA busy_timeout=10000`); err != nil {
		panic(err)
	}
	if _, err := db.Exec(`
		UPDATE api_integration_usage_events
		SET metadata_json = json_remove(metadata_json, '$.fast_mode', '$.speed_mode', '$.speed_multiplier', '$.speed_source')
		WHERE integration_name = 'Codex CLI'
		  AND json_valid(metadata_json)
		  AND json_extract(metadata_json, '$.source_path') LIKE '%archived_sessions%'
	`); err != nil {
		panic(err)
	}

	pricing, err := agentusage.DefaultPricingMap()
	if err != nil {
		panic(err)
	}

	rows, err := db.Query(`
		SELECT id, captured_at, integration_name, provider, model, COALESCE(metadata_json, ''),
		       CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json,'$.input_tokens'), prompt_tokens) AS INTEGER) ELSE prompt_tokens END,
		       CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json,'$.cached_input_tokens'),0) AS INTEGER) ELSE 0 END,
		       CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json,'$.cache_creation_input_tokens'),0) AS INTEGER) ELSE 0 END,
		       CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json,'$.cache_creation_1h_input_tokens'),0) AS INTEGER) ELSE 0 END,
		       CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json,'$.output_tokens'), completion_tokens) AS INTEGER) ELSE completion_tokens END,
		       CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json,'$.reasoning_output_tokens'),0) AS INTEGER) ELSE 0 END
		FROM api_integration_usage_events
		WHERE cost_usd IS NULL
		  AND integration_name IN ('Claude Code','Codex CLI','Gemini CLI','Antigravity')
	`)
	if err != nil {
		panic(err)
	}

	var toUpdate []row
	for rows.Next() {
		var r row
		var capturedAt string
		if err := rows.Scan(&r.id, &capturedAt, &r.integration, &r.provider, &r.model, &r.metadata, &r.input, &r.cached, &r.cacheCreation, &r.cacheCreation1h, &r.output, &r.reasoning); err != nil {
			panic(err)
		}
		r.capturedAt, err = time.Parse(time.RFC3339Nano, capturedAt)
		if err != nil {
			fmt.Printf("skipping row %d: invalid captured_at %q\n", r.id, capturedAt)
			continue
		}
		toUpdate = append(toUpdate, r)
	}
	rows.Close()

	fmt.Printf("rows to backfill: %d\n", len(toUpdate))

	updated, skipped := 0, 0
	for _, r := range toUpdate {
		opts := costOptionsForRow(r)
		cost := pricing.CalculateCostAt(r.model, r.capturedAt, agentusage.TokenCounts{
			InputTokens:           r.input,
			OutputTokens:          r.output,
			CachedInputTokens:     r.cached,
			CacheCreationTokens:   r.cacheCreation,
			CacheCreation1hTokens: r.cacheCreation1h,
			ReasoningTokens:       r.reasoning,
		}, opts)
		if cost == 0 {
			skipped++
			continue
		}
		metadata := normalizeCostMetadata(r, opts)
		if _, err := db.Exec(`UPDATE api_integration_usage_events SET cost_usd=?, metadata_json=? WHERE id=? AND cost_usd IS NULL`, cost, metadata, r.id); err != nil {
			panic(err)
		}
		updated++
	}
	fmt.Printf("updated: %d  skipped (unknown model/zero): %d\n", updated, skipped)

	// Spot-check: show per-model totals
	fmt.Println("\nper-model 24h totals after backfill:")
	chk, _ := db.Query(`
		SELECT model, COUNT(*) reqs, SUM(total_tokens) toks,
		       ROUND(SUM(COALESCE(cost_usd,0)),4) cost,
		       SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END) null_cost
		FROM api_integration_usage_events
		WHERE captured_at >= datetime('now','-24 hours')
		GROUP BY model ORDER BY toks DESC
	`)
	defer chk.Close()
	fmt.Printf("%-40s %8s %15s %12s %10s\n", "model", "reqs", "tokens", "cost", "null_cost")
	for chk.Next() {
		var model sql.NullString
		var reqs, toks, nullCost int64
		var cost float64
		chk.Scan(&model, &reqs, &toks, &cost, &nullCost)
		fmt.Printf("%-40s %8d %15d %12.4f %10d\n", model.String, reqs, toks, cost, nullCost)
	}
}

func costOptionsForRow(r row) agentusage.CostOptions {
	opts := agentusage.CostOptions{}
	var metadata map[string]any
	_ = json.Unmarshal([]byte(r.metadata), &metadata)
	switch strings.ToLower(strings.TrimSpace(r.integration)) {
	case "codex cli":
		if !codexSourceIsArchived(metadataString(metadata, "source_path")) && (metadataBool(metadata, "fast_mode") || codexMetadataRequestsFast(metadata)) {
			opts.CostMultiplier = codexFastModeCostMultiplier(r.model)
		}
	case "gemini cli", "antigravity":
		opts.ReasoningBilledAsOutput = true
		opts.ProviderPrefixes = agentusage.GoogleFamilyProviderPrefixes
	}
	if opts.CostMultiplier <= 0 && metadata != nil && strings.ToLower(strings.TrimSpace(r.integration)) != "codex cli" {
		if multiplier, ok := metadata["speed_multiplier"].(float64); ok && multiplier > 0 {
			opts.CostMultiplier = multiplier
		}
	}
	return opts
}

func normalizeCostMetadata(r row, opts agentusage.CostOptions) string {
	if strings.TrimSpace(r.metadata) == "" {
		return r.metadata
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(r.metadata), &metadata); err != nil || metadata == nil {
		return r.metadata
	}
	if strings.ToLower(strings.TrimSpace(r.integration)) == "codex cli" {
		if codexSourceIsArchived(metadataString(metadata, "source_path")) {
			delete(metadata, "fast_mode")
			delete(metadata, "speed_mode")
			delete(metadata, "speed_multiplier")
			delete(metadata, "speed_source")
		} else if opts.CostMultiplier > 0 {
			metadata["speed_multiplier"] = opts.CostMultiplier
		} else {
			delete(metadata, "speed_multiplier")
		}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return r.metadata
	}
	return string(encoded)
}

func codexMetadataRequestsFast(metadata map[string]any) bool {
	switch metadataString(metadata, "speed_mode") {
	case "fast", "priority":
		return true
	default:
		return false
	}
}

func codexSourceIsArchived(sourcePath string) bool {
	return strings.Contains(strings.ToLower(sourcePath), "archived_sessions")
}

func codexFastModeCostMultiplier(model string) float64 {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(normalized, "gpt-5.5"):
		return 2.5
	case strings.HasPrefix(normalized, "gpt-5.4") && !strings.HasPrefix(normalized, "gpt-5.4-mini"):
		return 2
	default:
		return 0
	}
}

func metadataBool(metadata map[string]any, key string) bool {
	value, ok := metadata[key]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "fast", "enabled", "on":
			return true
		}
	}
	return false
}

func metadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key].(string)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(value))
}
