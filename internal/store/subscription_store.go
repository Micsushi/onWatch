package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/onllm-dev/onwatch/v2/internal/agentusage"
	ai "github.com/onllm-dev/onwatch/v2/internal/api_integrations"
	"github.com/onllm-dev/onwatch/v2/internal/subscription"
	"math"
	"strings"
	"time"
)

type subscriptionExecer interface {
	Exec(string, ...any) (sql.Result, error)
}

func (s *Store) recordSubscriptionTelemetry(e *ai.UsageEvent) error {
	return recordSubscriptionTelemetry(s.db, e)
}
func recordSubscriptionTelemetry(db subscriptionExecer, e *ai.UsageEvent) error {
	if e.Integration != "Codex CLI" {
		return nil
	}
	var meta struct {
		Quota map[string]json.RawMessage `json:"quota_telemetry"`
	}
	if json.Unmarshal([]byte(e.MetadataJSON), &meta) != nil || len(meta.Quota) == 0 {
		return nil
	}
	var plan string
	_ = json.Unmarshal(meta.Quota["plan"], &plan)
	if plan != "pro" && plan != "plus" && plan != "prolite" && plan != "free" {
		return nil
	}
	for _, name := range []string{"seven_day", "five_hour"} {
		var q struct {
			Used  *float64 `json:"used"`
			Reset int64    `json:"reset"`
		}
		if json.Unmarshal(meta.Quota[name], &q) != nil || q.Used == nil || *q.Used < 0 || *q.Used > 100 || math.IsNaN(*q.Used) || q.Reset <= 0 {
			continue
		}
		if err := insertSubscriptionMeter(db, "codex", e.Account, subscription.Meter{At: e.Timestamp, Reset: time.Unix(q.Reset, 0).UTC(), Used: *q.Used, Quota: name, Plan: plan}); err != nil {
			return err
		}
	}
	return nil
}

// InsertSubscriptionMeter supports replay of sanitized local quota counters.
func (s *Store) InsertSubscriptionMeter(provider, account string, m subscription.Meter) error {
	return insertSubscriptionMeter(s.db, provider, account, m)
}
func insertSubscriptionMeter(db subscriptionExecer, provider, account string, m subscription.Meter) error {
	if account == "" {
		account = "default"
	}
	if provider != "codex" || m.At.IsZero() || m.Used < 0 || m.Used > 100 || math.IsNaN(m.Used) {
		return fmt.Errorf("invalid subscription meter")
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO subscription_meter_observations(provider,account_name,plan,quota,captured_at,resets_at,utilization) VALUES(?,?,?,?,?,?,?)`, provider, account, m.Plan, m.Quota, m.At.UTC().Format(time.RFC3339Nano), m.Reset.UTC().Format(time.RFC3339Nano), m.Used)
	return err
}

// SubscriptionInputs streams normalized token vectors. The bound is explicit: no silent LIMIT truncation.
func (s *Store) SubscriptionInputs(ctx context.Context, provider, account string, accountID int64, start, end time.Time) ([]subscription.Event, []subscription.Meter, error) {
	integration := map[string]string{"codex": "Codex CLI", "anthropic": "Claude Code", "antigravity": "Antigravity"}[provider]
	if integration == "" {
		return nil, nil, fmt.Errorf("unsupported provider")
	}
	if account == "" {
		account = "default"
	}
	var events []subscription.Event
	q := `SELECT captured_at,captured_at,model,reasoning_effort,speed_mode,source_path,1,input_tokens,cached_input_tokens,cache_creation_input_tokens,output_tokens,total_tokens,metadata_json,0 FROM api_integration_usage_events WHERE integration_name=? AND account_name=? AND captured_at COLLATE ONWATCH_RFC3339>=? AND captured_at COLLATE ONWATCH_RFC3339<?
 UNION ALL SELECT first_captured_at,last_captured_at,model,reasoning_effort,speed_mode,origin_scope,request_count,input_tokens,cached_input_tokens,cache_creation_input_tokens,output_tokens,total_tokens,'{}',1 FROM api_integration_usage_hourly WHERE integration_name=? AND account_name=? AND first_captured_at COLLATE ONWATCH_RFC3339>=? AND last_captured_at COLLATE ONWATCH_RFC3339<?`
	bounds := []any{integration, account, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)}
	args := append(append([]any{}, bounds...), bounds...)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	current := subscription.Prices()
	fallback, _ := agentusage.DefaultPricingMap()
	for rows.Next() {
		var e subscription.Event
		var at, last, path, metadata string
		var counts agentusage.TokenCounts
		var archived int
		if err = rows.Scan(&at, &last, &e.Model, &e.Effort, &e.Speed, &path, &e.Requests, &counts.InputTokens, &counts.CachedInputTokens, &counts.CacheCreationTokens, &counts.OutputTokens, &counts.TotalTokens, &metadata, &archived); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if counts.TotalTokens <= 0 {
			continue
		}
		e.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		e.End, err = time.Parse(time.RFC3339Nano, last)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		e.Archived = archived == 1
		e.Tokens = counts.TotalTokens
		e.Input = counts.InputTokens + counts.CachedInputTokens + counts.CacheCreationTokens
		e.Cached = counts.CachedInputTokens
		e.Output = counts.OutputTokens
		var meta struct {
			SpeedSource string `json:"speed_source"`
			Cache1h     int    `json:"cache_creation_1h_input_tokens"`
			Quota       struct {
				Plan string `json:"plan"`
			} `json:"quota_telemetry"`
		}
		_ = json.Unmarshal([]byte(metadata), &meta)
		e.Plan = meta.Quota.Plan
		counts.CacheCreation1hTokens = meta.Cache1h
		price := fallback
		if current.Known(e.Model, nil) {
			price = current
		}
		e.Priced = price.Known(e.Model, nil)
		e.Cost = price.CalculateCost(e.Model, counts, agentusage.CostOptions{})
		speed := e.Speed
		if meta.SpeedSource == "codex_config" {
			speed = "unknown"
		}
		if provider == "codex" {
			e.Credits, e.CreditsKnown = subscription.CodexCredits(e.Model, speed, counts)
		}
		switch {
		case strings.Contains(path, "/Users/"):
			e.Device = "macOS source"
		case strings.Contains(path, ":\\") || strings.Contains(path, ":/"):
			e.Device = "Windows source"
		case strings.HasPrefix(path, "device:") || strings.HasPrefix(path, "import:"):
			e.Device = "Imported source"
		default:
			e.Device = "Other / unknown source"
		}
		events = append(events, e)
		if len(events) > 250000 {
			rows.Close()
			return nil, nil, fmt.Errorf("too many usage records; choose a shorter range")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	meters, err := s.subscriptionMeters(ctx, provider, account, accountID, start, end)
	return events, meters, err
}
func (s *Store) subscriptionMeters(ctx context.Context, provider, account string, accountID int64, start, end time.Time) ([]subscription.Meter, error) {
	var query string
	var args []any
	startRaw := start.UTC().Format(time.RFC3339Nano)
	endRaw := end.UTC().Format(time.RFC3339Nano)
	switch provider {
	case "codex":
		query = `SELECT s.captured_at,COALESCE(q.resets_at,''),q.utilization,q.quota_name,COALESCE(s.plan_type,''),'poll' FROM codex_snapshots s JOIN codex_quota_values q ON q.snapshot_id=s.id WHERE s.account_id=? AND s.captured_at COLLATE ONWATCH_RFC3339>=? AND s.captured_at COLLATE ONWATCH_RFC3339<?`
		args = []any{accountID, startRaw, endRaw}
	case "anthropic":
		query = `SELECT s.captured_at,COALESCE(q.resets_at,''),q.utilization,q.quota_name,'','poll' FROM anthropic_snapshots s JOIN anthropic_quota_values q ON q.snapshot_id=s.id WHERE s.captured_at COLLATE ONWATCH_RFC3339>=? AND s.captured_at COLLATE ONWATCH_RFC3339<?`
		args = []any{startRaw, endRaw}
	case "antigravity":
		query = `SELECT s.captured_at,COALESCE(q.reset_time,''),100-q.remaining_fraction*100,q.group_key||':'||q.window_kind,s.plan_name,'poll' FROM antigravity_snapshots s JOIN antigravity_quota_summary_buckets q ON q.snapshot_id=s.id WHERE s.captured_at COLLATE ONWATCH_RFC3339>=? AND s.captured_at COLLATE ONWATCH_RFC3339<?`
		args = []any{startRaw, endRaw}
	}
	if provider == "codex" {
		query += ` UNION ALL SELECT captured_at,resets_at,utilization,quota,plan,'rollout' FROM subscription_meter_observations WHERE provider='codex' AND account_name=? AND captured_at COLLATE ONWATCH_RFC3339>=? AND captured_at COLLATE ONWATCH_RFC3339<? AND plan=(SELECT plan_type FROM codex_snapshots WHERE account_id=? ORDER BY captured_at COLLATE ONWATCH_RFC3339 DESC LIMIT 1)`
		args = append(args, account, startRaw, endRaw, accountID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []subscription.Meter{}
	for rows.Next() {
		var m subscription.Meter
		var at, reset string
		if err = rows.Scan(&at, &reset, &m.Used, &m.Quota, &m.Plan, &m.Source); err != nil {
			return nil, err
		}
		m.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		if reset != "" {
			m.Reset, err = time.Parse(time.RFC3339Nano, reset)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, m)
		if len(result) > 200000 {
			return nil, fmt.Errorf("too many quota samples; choose a shorter range")
		}
	}
	return result, rows.Err()
}
