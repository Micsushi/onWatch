package collector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

func (r *Runtime) collectAssignedQuotas(ctx context.Context) error {
	for _, assignment := range r.desired.Assignments {
		interval, err := time.ParseDuration(assignment.PollInterval)
		if err != nil || interval <= 0 {
			interval = time.Minute
		}
		key := strings.ToLower(assignment.Provider) + "\x00" + assignment.ExternalID
		if time.Since(r.lastQuotaPoll[key]) < interval {
			continue
		}
		event, err := r.pollQuota(ctx, assignment)
		r.lastQuotaPoll[key] = time.Now()
		if err != nil {
			r.logger.Warn("assigned quota poll failed", "provider", assignment.Provider, "account", assignment.ExternalID, "error", err)
			continue
		}
		if err := r.spool.Append(event); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) pollQuota(ctx context.Context, assignment ingest.ProviderAssignment) (ingest.Event, error) {
	now := time.Now().UTC()
	provider := strings.ToLower(strings.TrimSpace(assignment.Provider))
	var metrics []ingest.QuotaMetric

	switch provider {
	case "codex", "openai":
		token := api.DetectCodexToken(r.logger)
		if token == "" {
			return ingest.Event{}, fmt.Errorf("Codex credential unavailable")
		}
		response, err := api.NewCodexClient(token, r.logger).FetchUsage(ctx)
		if err != nil {
			return ingest.Event{}, err
		}
		for _, quota := range response.ToSnapshot(now).Quotas {
			metrics = append(metrics, quotaMetric(quota.Name, quota.Utilization, nil, "percent", quota.ResetsAt, quota.Status))
		}
		provider = "openai"
	case "anthropic":
		token := api.DetectAnthropicToken(r.logger)
		if token == "" {
			return ingest.Event{}, fmt.Errorf("Anthropic credential unavailable")
		}
		response, err := api.NewAnthropicClient(token, r.logger).FetchQuotas(ctx)
		if err != nil {
			return ingest.Event{}, err
		}
		for _, quota := range response.ToSnapshot(now).Quotas {
			metrics = append(metrics, quotaMetric(quota.Name, quota.Utilization, nil, "percent", quota.ResetsAt, ""))
		}
	case "copilot":
		token := strings.TrimSpace(os.Getenv(aliasEnv(assignment.CredentialAlias, "COPILOT_TOKEN")))
		if token == "" {
			return ingest.Event{}, fmt.Errorf("Copilot credential unavailable")
		}
		response, err := api.NewCopilotClient(token, r.logger).FetchQuotas(ctx)
		if err != nil {
			return ingest.Event{}, err
		}
		snapshot := response.ToSnapshot(now)
		for _, quota := range snapshot.Quotas {
			limit := float64(quota.Entitlement)
			metrics = append(metrics, quotaMetric(quota.Name, 100-quota.PercentRemaining, &limit, "percent", snapshot.ResetDate, ""))
		}
	case "gemini":
		credentials := api.DetectGeminiCredentials(r.logger)
		if credentials == nil || credentials.AccessToken == "" {
			return ingest.Event{}, fmt.Errorf("Gemini credential unavailable")
		}
		response, err := api.NewGeminiClient(credentials.AccessToken, r.logger).FetchQuotas(ctx)
		if err != nil {
			return ingest.Event{}, err
		}
		for _, quota := range response.ToSnapshot(now).Quotas {
			metrics = append(metrics, quotaMetric(quota.ModelID, quota.UsagePercent, nil, "percent", quota.ResetTime, ""))
		}
	case "cursor":
		token := api.DetectCursorToken(r.logger)
		if token == "" {
			return ingest.Event{}, fmt.Errorf("Cursor credential unavailable")
		}
		snapshot, err := api.NewCursorClient(token, r.logger).FetchQuotas(ctx)
		if err != nil {
			return ingest.Event{}, err
		}
		for _, quota := range snapshot.Quotas {
			limit := quota.Limit
			metrics = append(metrics, quotaMetric(quota.Name, quota.Utilization, &limit, string(quota.Format), quota.ResetsAt, ""))
		}
	case "minimax":
		token := strings.TrimSpace(os.Getenv(aliasEnv(assignment.CredentialAlias, "MINIMAX_API_KEY")))
		if token == "" {
			return ingest.Event{}, fmt.Errorf("MiniMax credential unavailable")
		}
		response, err := api.NewMiniMaxClient(token, r.logger).FetchRemains(ctx)
		if err != nil {
			return ingest.Event{}, err
		}
		for _, model := range response.ToSnapshot(now).Models {
			limit := float64(model.Total)
			metrics = append(metrics, quotaMetric(model.ModelName, model.UsedPercent, &limit, "percent", model.ResetAt, ""))
			if model.HasWeeklyQuota {
				weeklyLimit := float64(model.WeeklyTotal)
				metrics = append(metrics, quotaMetric(model.ModelName+"_weekly", model.WeeklyUsedPercent, &weeklyLimit, "percent", model.WeeklyResetAt, ""))
			}
		}
	case "openrouter":
		token := strings.TrimSpace(os.Getenv(aliasEnv(assignment.CredentialAlias, "OPENROUTER_API_KEY")))
		if token == "" {
			return ingest.Event{}, fmt.Errorf("OpenRouter credential unavailable")
		}
		response, err := api.NewOpenRouterClient(token, r.logger).FetchUsage(ctx)
		if err != nil {
			return ingest.Event{}, err
		}
		snapshot := response.ToSnapshot(now)
		metrics = append(metrics,
			quotaMetric("usage", snapshot.Usage, snapshot.Limit, "usd", nil, ""),
			quotaMetric("daily", snapshot.UsageDaily, snapshot.Limit, "usd", nil, ""),
			quotaMetric("weekly", snapshot.UsageWeekly, snapshot.Limit, "usd", nil, ""),
			quotaMetric("monthly", snapshot.UsageMonthly, snapshot.Limit, "usd", nil, ""),
		)
	case "zai":
		token := strings.TrimSpace(os.Getenv(aliasEnv(assignment.CredentialAlias, "ZAI_API_KEY")))
		if token == "" {
			return ingest.Event{}, fmt.Errorf("Z.AI credential unavailable")
		}
		response, err := api.NewZaiClient(token, r.logger).FetchQuotas(ctx)
		if err != nil {
			return ingest.Event{}, err
		}
		snapshot := response.ToSnapshot(now)
		timeLimit := float64(snapshot.TimeLimit)
		tokenLimit := float64(snapshot.TokensLimit)
		metrics = append(metrics,
			quotaMetric("time", float64(snapshot.TimePercentage), &timeLimit, "percent", nil, ""),
			quotaMetric("tokens", float64(snapshot.TokensPercentage), &tokenLimit, "percent", snapshot.TokensNextResetTime, ""),
		)
	case "antigravity":
		response, err := api.NewAntigravityClient(r.logger).FetchQuotas(ctx)
		if err != nil {
			return ingest.Event{}, err
		}
		for _, model := range response.ToSnapshot(now).Models {
			metrics = append(metrics, quotaMetric(model.ModelID, 100-model.RemainingPercent, nil, "percent", model.ResetTime, ""))
		}
	default:
		return ingest.Event{}, fmt.Errorf("unsupported assigned provider %q", provider)
	}

	if len(metrics) == 0 {
		return ingest.Event{}, fmt.Errorf("provider returned no quota metrics")
	}
	payload, err := json.Marshal(ingest.QuotaSnapshot{Version: 1, Metrics: metrics})
	if err != nil {
		return ingest.Event{}, err
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return ingest.Event{}, err
	}
	event := ingest.Event{
		EventID:    "evt_" + hex.EncodeToString(idBytes),
		Kind:       "quota_snapshot",
		CapturedAt: now,
		Provider:   provider,
		Account:    ingest.Account{ExternalID: assignment.ExternalID},
		Payload:    payload,
	}
	return event, event.Validate(now)
}

func quotaMetric(name string, value float64, limit *float64, unit string, reset *time.Time, status string) ingest.QuotaMetric {
	return ingest.QuotaMetric{Name: name, Value: value, Limit: limit, Unit: unit, ResetsAt: reset, Status: status}
}

func aliasEnv(alias, fallback string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fallback
	}
	return alias
}
