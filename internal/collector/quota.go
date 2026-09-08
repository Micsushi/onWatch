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

const maxQuotaPollBackoff = time.Hour

type quotaPollState struct {
	Failures int       `json:"failures,omitempty"`
	NextPoll time.Time `json:"next_poll"`
}

// Refreshed credentials stay in device memory; the CLI's shared file is untouched.
type geminiQuotaToken struct {
	sourceAccess, refresh, access string
	expiresAt                     time.Time
}

func (r *Runtime) geminiAccessToken(ctx context.Context, credentials *api.GeminiCredentials) (string, error) {
	if credentials == nil {
		return "", fmt.Errorf("Gemini credential unavailable")
	}
	now := r.quotaNow()
	if credentials.AccessToken != "" && (credentials.ExpiresAt.IsZero() || credentials.ExpiresAt.After(now.Add(time.Minute))) {
		return credentials.AccessToken, nil
	}
	if cached := r.geminiToken; cached != nil && cached.sourceAccess == credentials.AccessToken && cached.refresh == credentials.RefreshToken && cached.expiresAt.After(now.Add(time.Minute)) {
		return cached.access, nil
	}
	if credentials.RefreshToken == "" {
		return "", fmt.Errorf("Gemini credential expired; reauthenticate Gemini CLI")
	}
	client := api.DetectGeminiClientCredentials()
	token, err := api.RefreshGeminiToken(ctx, credentials.RefreshToken, client.ClientID, client.ClientSecret)
	if err != nil {
		return "", err
	}
	r.geminiToken = &geminiQuotaToken{sourceAccess: credentials.AccessToken, refresh: credentials.RefreshToken, access: token.AccessToken, expiresAt: now.Add(time.Duration(token.ExpiresIn) * time.Second)}
	return token.AccessToken, nil
}

func quotaPollDelay(interval time.Duration, failures int, random float64) time.Duration {
	if interval <= 0 {
		interval = time.Minute
	}
	delay := interval
	ceiling := max(maxQuotaPollBackoff, interval)
	for i := 1; i < failures; i++ {
		if delay >= ceiling || delay > ceiling/2 {
			delay = ceiling
			break
		}
		delay *= 2
	}
	if delay > maxQuotaPollBackoff && interval <= maxQuotaPollBackoff {
		delay = maxQuotaPollBackoff
	}
	if random < 0 {
		random = 0
	} else if random > 1 {
		random = 1
	}
	// Spread healthy and retry traffic across a 20% window on either side.
	delay = time.Duration(float64(delay) * (0.8 + 0.4*random))
	if interval <= maxQuotaPollBackoff && delay > maxQuotaPollBackoff {
		return maxQuotaPollBackoff
	}
	return delay
}

func (r *Runtime) quotaNow() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *Runtime) quotaRandom() float64 {
	if r.random != nil {
		return r.random()
	}
	return 0.5
}

func (r *Runtime) collectAssignedQuotas(ctx context.Context) (result error) {
	if r.quotaPolls == nil {
		r.quotaPolls = map[string]quotaPollState{}
	}
	stateChanged := false
	active := make(map[string]bool, len(r.desired.Assignments))
	for _, assignment := range r.desired.Assignments {
		active[strings.ToLower(assignment.Provider)+"\x00"+assignment.ExternalID] = true
	}
	for key := range r.quotaPolls {
		if !active[key] {
			delete(r.quotaPolls, key)
			stateChanged = true
		}
	}
	defer func() {
		if stateChanged {
			if err := r.saveLocalState(); err != nil {
				r.logger.Warn("failed to persist assigned quota poll schedule", "error", err)
				if result == nil {
					result = err
				}
			}
		}
	}()
	for _, assignment := range r.desired.Assignments {
		if err := ctx.Err(); err != nil {
			return err
		}
		interval, err := time.ParseDuration(assignment.PollInterval)
		if err != nil || interval <= 0 {
			interval = time.Minute
		}
		key := strings.ToLower(assignment.Provider) + "\x00" + assignment.ExternalID
		now := r.quotaNow()
		state := r.quotaPolls[key]
		if now.Before(state.NextPoll) {
			continue
		}
		event, err := r.pollQuota(ctx, assignment)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			state.Failures++
			state.NextPoll = now.Add(quotaPollDelay(interval, state.Failures, r.quotaRandom()))
			r.quotaPolls[key] = state
			stateChanged = true
			r.logger.Warn("assigned quota poll failed", "provider", assignment.Provider, "account", assignment.ExternalID, "error", err)
			continue
		}
		if err := r.spool.Append(event); err != nil {
			state.Failures++
			state.NextPoll = now.Add(quotaPollDelay(interval, state.Failures, r.quotaRandom()))
			r.quotaPolls[key] = state
			stateChanged = true
			return err
		}
		state.Failures = 0
		state.NextPoll = now.Add(quotaPollDelay(interval, 0, r.quotaRandom()))
		r.quotaPolls[key] = state
		stateChanged = true
	}
	return nil
}

func (r *Runtime) pollQuota(ctx context.Context, assignment ingest.ProviderAssignment) (ingest.Event, error) {
	now := time.Now().UTC()
	provider := strings.ToLower(strings.TrimSpace(assignment.Provider))
	var metrics []ingest.QuotaMetric
	plan := ""

	switch provider {
	case "codex", "openai":
		credentials := api.DetectCodexCredentials(r.logger)
		if assignment.CredentialAlias != "" {
			credentials = api.ReadCodexCredentialsFromHome(os.Getenv(assignment.CredentialAlias))
		}
		if credentials == nil || credentials.AccessToken == "" {
			return ingest.Event{}, fmt.Errorf("Codex credential unavailable")
		}
		if assignment.ExternalID != credentials.CompositeExternalID() && assignment.ExternalID != credentials.AccountID {
			return ingest.Event{}, fmt.Errorf("Codex credential does not match assigned account")
		}
		client := api.NewCodexClient(credentials.AccessToken, r.logger)
		client.SetAccountID(credentials.AccountID)
		response, err := client.FetchUsage(ctx)
		if err != nil {
			return ingest.Event{}, err
		}
		plan = response.PlanType
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
		token, err := r.geminiAccessToken(ctx, credentials)
		if err != nil {
			return ingest.Event{}, err
		}
		client := api.NewGeminiClient(token, r.logger)
		tier, err := client.FetchTier(ctx)
		if err != nil {
			return ingest.Event{}, err
		}
		client.SetProjectID(tier.CloudAICompanionProject)
		response, err := client.FetchQuotas(ctx)
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
		var snapshot *api.AntigravitySnapshot
		var err error
		source := strings.ToLower(strings.TrimSpace(os.Getenv("ANTIGRAVITY_SOURCE")))
		if source != "ide" {
			if r.antigravityCLI == nil {
				r.antigravityCLI = api.NewAntigravityCLIRunner(r.logger)
			}
			pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			snapshot, err = r.antigravityCLI.Fetch(pollCtx)
			cancel()
		}
		if snapshot == nil && source != "cli" {
			var response *api.AntigravityUserStatusResponse
			response, err = api.NewAntigravityClient(r.logger).FetchQuotas(ctx)
			if err == nil {
				snapshot = response.ToSnapshot(now)
			}
		}
		if err != nil {
			return ingest.Event{}, err
		}
		if snapshot == nil {
			return ingest.Event{}, fmt.Errorf("Antigravity quota unavailable")
		}
		plan = snapshot.PlanName
		for _, model := range snapshot.Models {
			metrics = append(metrics, quotaMetric(model.ModelID, 100-model.RemainingPercent, nil, "percent", model.ResetTime, ""))
		}
		for _, group := range snapshot.SummaryGroups {
			for _, bucket := range group.Buckets {
				metric := quotaMetric(bucket.BucketID, bucket.UsagePercent, nil, "percent", bucket.ResetTime, "")
				metric.Group, metric.Window = group.GroupKey, bucket.Window
				metrics = append(metrics, metric)
			}
		}
	default:
		return ingest.Event{}, fmt.Errorf("unsupported assigned provider %q", provider)
	}

	if len(metrics) == 0 {
		return ingest.Event{}, fmt.Errorf("provider returned no quota metrics")
	}
	payload, err := json.Marshal(ingest.QuotaSnapshot{Version: 1, Plan: plan, Metrics: metrics})
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
