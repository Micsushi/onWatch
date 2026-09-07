//go:build ignore

// Fills missing generated agent usage costs without changing stored historical
// costs. Uses captured_at and normalized token columns, including cached input.
// Compact metadata intentionally omits those token counts.

package main

import (
	"database/sql"
	"encoding/json"
	"flag"
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
	integration, provider, model, metadata, speedMode                    string
	capturedAt                                                           time.Time
}

func main() {
	home := os.Getenv("USERPROFILE")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	dbPath := filepath.Join(home, ".onwatch", "data", "onwatch.db")
	flag.StringVar(&dbPath, "db", dbPath, "database to backfill")
	model := flag.String("model", "", "only backfill this model")
	dryRun := flag.Bool("dry-run", false, "calculate without changing rows")
	flag.Parse()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA busy_timeout=10000`); err != nil {
		panic(err)
	}

	pricing, err := agentusage.DefaultPricingMap()
	if err != nil {
		panic(err)
	}

	rows, err := db.Query(`
		SELECT id, captured_at, integration_name, provider, model, COALESCE(metadata_json, ''), speed_mode,
		       input_tokens, cached_input_tokens, cache_creation_input_tokens,
		       CASE WHEN json_valid(metadata_json) THEN CAST(COALESCE(json_extract(metadata_json,'$.cache_creation_1h_input_tokens'),0) AS INTEGER) ELSE 0 END,
		       output_tokens, reasoning_output_tokens
		FROM api_integration_usage_events
		WHERE cost_usd IS NULL
		  AND (? = '' OR model = ?)
		  AND integration_name IN ('Claude Code','Codex CLI','Gemini CLI','Antigravity')
	`, *model, *model)
	if err != nil {
		panic(err)
	}

	var toUpdate []row
	for rows.Next() {
		var r row
		var capturedAt string
		if err := rows.Scan(&r.id, &capturedAt, &r.integration, &r.provider, &r.model, &r.metadata, &r.speedMode, &r.input, &r.cached, &r.cacheCreation, &r.cacheCreation1h, &r.output, &r.reasoning); err != nil {
			panic(err)
		}
		r.capturedAt, err = time.Parse(time.RFC3339Nano, capturedAt)
		if err != nil {
			fmt.Printf("skipping row %d: invalid captured_at %q\n", r.id, capturedAt)
			continue
		}
		toUpdate = append(toUpdate, r)
	}
	if err := rows.Err(); err != nil {
		panic(err)
	}
	rows.Close()

	fmt.Printf("rows to backfill: %d\n", len(toUpdate))

	updated, skipped := 0, 0
	totalCost := 0.0
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
		if !*dryRun {
			if _, err := db.Exec(`UPDATE api_integration_usage_events SET cost_usd=? WHERE id=? AND cost_usd IS NULL`, cost, r.id); err != nil {
				panic(err)
			}
		}
		totalCost += cost
		updated++
	}
	fmt.Printf("updated: %d  skipped (unknown model/zero): %d\n", updated, skipped)
	fmt.Printf("dry-run: %v, added estimated cost: $%.4f\n", *dryRun, totalCost)

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
		fast := r.speedMode == "fast" || r.speedMode == "priority"
		opts = agentusage.CodexCostOptions(r.model, agentusage.TokenCounts{
			InputTokens: r.input, CachedInputTokens: r.cached, CacheCreationTokens: r.cacheCreation,
		}, fast)
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
