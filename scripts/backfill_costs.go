//go:build ignore

// Rebuild missing log estimates from normalized request categories and the
// model price in effect at capture time. Dry run is the default.

package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/agentusage"
	_ "modernc.org/sqlite"
)

type estimateRow struct {
	id                 int64
	integration, model string
	speed, capturedAt  string
	counts             agentusage.TokenCounts
	previous           sql.NullFloat64
	generated          bool
}

type costChange struct {
	id       int64
	previous sql.NullFloat64
	next     float64
}

type summary struct {
	requests, tokens int
	delta            float64
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	profile, _ := os.UserHomeDir()
	dbPath := flag.String("db", filepath.Join(profile, ".onwatch", "data", "onwatch.db"), "database to audit")
	model := flag.String("model", "", "only audit this exact model ID")
	apply := flag.Bool("apply", false, "apply verified corrections in one transaction")
	dryRun := flag.Bool("dry-run", false, "calculate without changing rows (default)")
	legacy := flag.Bool("correct-legacy", false, "also compare historically incorrect generated Codex rates")
	flag.Parse()
	if *apply && *dryRun {
		return fmt.Errorf("choose either --apply or --dry-run")
	}
	absPath, err := filepath.Abs(*dbPath)
	if err != nil {
		return err
	}
	mode := "ro"
	if *apply {
		mode = "rw"
	}
	path := filepath.ToSlash(absPath)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "mode=" + mode + "&_pragma=busy_timeout(10000)&_txlock=immediate"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return err
	}
	defer db.Close()
	pricing, err := agentusage.DefaultPricingMap()
	if err != nil {
		return err
	}
	var queryer interface {
		Query(string, ...any) (*sql.Rows, error)
	} = db
	var tx *sql.Tx
	if *apply {
		tx, err = db.BeginTx(context.Background(), nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		queryer = tx
	}
	generated := "0"
	where := "WHERE cost_usd IS NULL"
	if *legacy {
		generated = `(source_path LIKE 'device:%' OR source_path GLOB '*agent-usage-[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9].jsonl')`
		where = `WHERE (cost_usd IS NULL OR model IN ('gpt-5.6-sol','gpt-5.6-terra','gpt-5.6-luna','claude-sonnet-5'))`
	}
	rows, err := queryer.Query(fmt.Sprintf(`
		SELECT id,captured_at,integration_name,model,speed_mode,cost_usd,%s,
		       input_tokens,cached_input_tokens,cache_creation_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,
		       CASE WHEN json_valid(metadata_json)
		            THEN CAST(COALESCE(json_extract(metadata_json,'$.cache_creation_1h_input_tokens'),0) AS INTEGER)
		            ELSE 0 END
		FROM api_integration_usage_events
		%s AND total_tokens>0 AND (?='' OR model=?)
		  AND ((integration_name='Codex CLI' AND provider='openai')
		    OR (integration_name='Claude Code' AND provider='anthropic')
		    OR (integration_name='Gemini CLI' AND provider='gemini')
		    OR integration_name='Antigravity')
	`, generated, where), *model, *model)
	if err != nil {
		return err
	}
	defer rows.Close()

	var changes []costChange
	summaries := make(map[string]summary)
	for rows.Next() {
		var r estimateRow
		c := &r.counts
		if err := rows.Scan(&r.id, &r.capturedAt, &r.integration, &r.model, &r.speed, &r.previous, &r.generated,
			&c.InputTokens, &c.CachedInputTokens, &c.CacheCreationTokens, &c.OutputTokens, &c.ReasoningTokens,
			&c.TotalTokens, &c.CacheCreation1hTokens); err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339Nano, r.capturedAt)
		if err != nil {
			return fmt.Errorf("invalid timestamp in usage row %d: %w", r.id, err)
		}
		estimated := pricing.EstimateLoggedCost(r.integration, r.model, r.speed, at, r.counts)
		action := "filled"
		if r.previous.Valid {
			action = legacyRateDisposition(pricing, r, at, estimated)
			if action == "" {
				continue
			}
		} else if estimated <= 0 {
			action = "unpriced"
		}
		if action == "filled" || action == "corrected" {
			changes = append(changes, costChange{r.id, r.previous, estimated})
		}
		key := r.integration + " " + r.model + " " + action
		stats := summaries[key]
		stats.requests++
		stats.tokens += c.TotalTokens
		stats.delta += estimated - r.previous.Float64
		summaries[key] = stats
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if tx != nil {
		update, err := tx.Prepare("UPDATE api_integration_usage_events SET cost_usd=? WHERE id=? AND cost_usd IS ?")
		if err != nil {
			return err
		}
		defer update.Close()
		for _, change := range changes {
			result, err := update.Exec(change.next, change.id, change.previous)
			if err != nil {
				return err
			}
			if count, err := result.RowsAffected(); err != nil || count != 1 {
				return fmt.Errorf("usage row %d changed during repair", change.id)
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	keys := make([]string, 0, len(summaries))
	for key := range summaries {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		s := summaries[key]
		fmt.Printf("%s: %d requests, %d tokens, estimate change %+.4f USD\n", key, s.requests, s.tokens, s.delta)
	}
	fmt.Printf("apply=%t, verified changes=%d\n", *apply, len(changes))
	return nil
}

func legacyRateDisposition(pricing *agentusage.PricingMap, r estimateRow, at time.Time, estimated float64) string {
	if estimated <= 0 || math.Abs(estimated-r.previous.Float64) < 1e-9 {
		return ""
	}
	launch := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	switch r.model {
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		original := pricing.EstimateLoggedCost(r.integration, r.model, r.speed, launch, r.counts)
		if math.Abs(original-r.previous.Float64) < 1e-9 {
			// The onWatch Codex collector always computes cost from token logs.
			if r.generated && r.integration == "Codex CLI" {
				return "corrected"
			}
			return "preserved legacy-rate candidate"
		}
	case "claude-sonnet-5":
		// Claude logs may supply a cost directly. The stored row cannot prove its origin.
		if math.Abs(1.5*estimated-r.previous.Float64) < 1e-9 {
			return "preserved legacy-rate candidate"
		}
	}
	return ""
}
