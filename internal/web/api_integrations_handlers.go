package web

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/store"
)

const apiIntegrationResponseCacheTTL = 15 * time.Second

type apiIntegrationResponseCache struct {
	mu         sync.Mutex
	entries    map[string]apiIntegrationResponseCacheEntry
	refreshing map[string]bool
}

type apiIntegrationResponseCacheEntry struct {
	version   uint64
	expiresAt time.Time
	payload   map[string]interface{}
}

func (c *apiIntegrationResponseCache) get(key string, version uint64, now time.Time) (map[string]interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		return nil, false
	}
	entry, ok := c.entries[key]
	if !ok || entry.version != version || now.After(entry.expiresAt) {
		return nil, false
	}
	return entry.payload, true
}

func (c *apiIntegrationResponseCache) getStale(key string) (map[string]interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		return nil, false
	}
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return entry.payload, true
}

func (c *apiIntegrationResponseCache) set(key string, version uint64, now time.Time, payload map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]apiIntegrationResponseCacheEntry)
	}
	c.entries[key] = apiIntegrationResponseCacheEntry{
		version:   version,
		expiresAt: now.Add(apiIntegrationResponseCacheTTL),
		payload:   payload,
	}
	delete(c.refreshing, key)
}

func (c *apiIntegrationResponseCache) startRefresh(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshing == nil {
		c.refreshing = make(map[string]bool)
	}
	if c.refreshing[key] {
		return false
	}
	c.refreshing[key] = true
	return true
}

func (c *apiIntegrationResponseCache) cancelRefresh(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.refreshing, key)
}

type apiIntegrationCurrentModelBreakdown struct {
	Model             string                                 `json:"model"`
	RequestCount      int                                    `json:"requestCount"`
	PromptTokens      int                                    `json:"promptTokens"`
	CompletionTokens  int                                    `json:"completionTokens"`
	TotalTokens       int                                    `json:"totalTokens"`
	InputTokens       int                                    `json:"inputTokens"`
	CachedTokens      int                                    `json:"cachedInputTokens"`
	CacheCreateTokens int                                    `json:"cacheCreationInputTokens"`
	OutputTokens      int                                    `json:"outputTokens"`
	ReasoningTokens   int                                    `json:"reasoningTokens"`
	TotalCostUSD      *float64                               `json:"totalCostUsd,omitempty"`
	LastCapturedAt    string                                 `json:"lastCapturedAt"`
	Efforts           []apiIntegrationCurrentEffortBreakdown `json:"efforts,omitempty"`
}

type apiIntegrationCurrentEffortBreakdown struct {
	ReasoningEffort   string   `json:"reasoningEffort"`
	Mode              string   `json:"mode"`
	SpeedMode         string   `json:"speedMode"`
	RequestCount      int      `json:"requestCount"`
	PromptTokens      int      `json:"promptTokens"`
	CompletionTokens  int      `json:"completionTokens"`
	TotalTokens       int      `json:"totalTokens"`
	InputTokens       int      `json:"inputTokens"`
	CachedTokens      int      `json:"cachedInputTokens"`
	CacheCreateTokens int      `json:"cacheCreationInputTokens"`
	OutputTokens      int      `json:"outputTokens"`
	ReasoningTokens   int      `json:"reasoningTokens"`
	TotalCostUSD      *float64 `json:"totalCostUsd,omitempty"`
	LastCapturedAt    string   `json:"lastCapturedAt"`
}

type apiIntegrationCurrentAccountBreakdown struct {
	Account           string                                `json:"account"`
	RequestCount      int                                   `json:"requestCount"`
	PromptTokens      int                                   `json:"promptTokens"`
	CompletionTokens  int                                   `json:"completionTokens"`
	TotalTokens       int                                   `json:"totalTokens"`
	InputTokens       int                                   `json:"inputTokens"`
	CachedTokens      int                                   `json:"cachedInputTokens"`
	CacheCreateTokens int                                   `json:"cacheCreationInputTokens"`
	OutputTokens      int                                   `json:"outputTokens"`
	ReasoningTokens   int                                   `json:"reasoningTokens"`
	TotalCostUSD      *float64                              `json:"totalCostUsd,omitempty"`
	LastCapturedAt    string                                `json:"lastCapturedAt"`
	Models            []apiIntegrationCurrentModelBreakdown `json:"models"`
}

type apiIntegrationCurrentProviderBreakdown struct {
	Provider          string                                  `json:"provider"`
	RequestCount      int                                     `json:"requestCount"`
	PromptTokens      int                                     `json:"promptTokens"`
	CompletionTokens  int                                     `json:"completionTokens"`
	TotalTokens       int                                     `json:"totalTokens"`
	InputTokens       int                                     `json:"inputTokens"`
	CachedTokens      int                                     `json:"cachedInputTokens"`
	CacheCreateTokens int                                     `json:"cacheCreationInputTokens"`
	OutputTokens      int                                     `json:"outputTokens"`
	ReasoningTokens   int                                     `json:"reasoningTokens"`
	TotalCostUSD      *float64                                `json:"totalCostUsd,omitempty"`
	LastCapturedAt    string                                  `json:"lastCapturedAt"`
	Accounts          []apiIntegrationCurrentAccountBreakdown `json:"accounts"`
}

// APIIntegrationsCurrent returns grouped current API integration usage totals.
func (h *Handler) APIIntegrationsCurrent(w http.ResponseWriter, r *http.Request) {
	version := h.apiIntegrationUsageVersion()
	now := time.Now()
	const key = "current"
	if cached, ok := h.apiIntegrationsCache.get(key, version, now); ok {
		w.Header().Set("X-OnWatch-Data-State", "fresh")
		respondJSON(w, http.StatusOK, cached)
		return
	}
	if cached, ok := h.apiIntegrationsCache.getStale(key); ok {
		w.Header().Set("X-OnWatch-Data-State", "stale")
		w.Header().Set("Retry-After", "1")
		if h.apiIntegrationsCache.startRefresh(key) {
			go h.refreshAPIIntegrationsCurrent(key, version)
		}
		respondJSON(w, http.StatusOK, cached)
		return
	}
	payload := h.buildAPIIntegrationsCurrent()
	h.apiIntegrationsCache.set(key, version, now, payload)
	w.Header().Set("X-OnWatch-Data-State", "fresh")
	respondJSON(w, http.StatusOK, payload)
}

func (h *Handler) refreshAPIIntegrationsCurrent(key string, version uint64) {
	defer func() {
		if recovered := recover(); recovered != nil {
			h.apiIntegrationsCache.cancelRefresh(key)
			h.logger.Error("API integrations current refresh panicked", "error", recovered)
		}
	}()
	payload := h.buildAPIIntegrationsCurrent()
	h.apiIntegrationsCache.set(key, version, time.Now(), payload)
}

func (h *Handler) buildAPIIntegrationsCurrent() map[string]interface{} {
	response := map[string]interface{}{}
	if h.store == nil {
		return response
	}

	effortRows, err := h.store.QueryAPIIntegrationUsageEffortSummary()
	if err != nil {
		h.logger.Error("failed to query API integrations effort summary", "error", err)
		return response
	}
	rows := summarizeAPIIntegrationUsage(effortRows)

	type modelNode struct {
		row apiIntegrationCurrentModelBreakdown
	}
	type accountNode struct {
		row    apiIntegrationCurrentAccountBreakdown
		models map[string]*modelNode
	}
	type providerNode struct {
		row      apiIntegrationCurrentProviderBreakdown
		accounts map[string]*accountNode
	}
	type integrationNode struct {
		RequestCount      int
		PromptTokens      int
		CompletionTokens  int
		TotalTokens       int
		InputTokens       int
		CachedTokens      int
		CacheCreateTokens int
		OutputTokens      int
		ReasoningTokens   int
		TotalCostUSD      float64
		HasCost           bool
		LastCapturedAt    time.Time
		Providers         map[string]*providerNode
	}

	integrationsMap := make(map[string]*integrationNode)
	for _, entry := range rows {
		integrationState, ok := integrationsMap[entry.IntegrationName]
		if !ok {
			integrationState = &integrationNode{Providers: make(map[string]*providerNode)}
			integrationsMap[entry.IntegrationName] = integrationState
		}
		providerState, ok := integrationState.Providers[entry.Provider]
		if !ok {
			providerState = &providerNode{
				row:      apiIntegrationCurrentProviderBreakdown{Provider: entry.Provider},
				accounts: make(map[string]*accountNode),
			}
			integrationState.Providers[entry.Provider] = providerState
		}
		accountState, ok := providerState.accounts[entry.AccountName]
		if !ok {
			accountState = &accountNode{
				row:    apiIntegrationCurrentAccountBreakdown{Account: entry.AccountName},
				models: make(map[string]*modelNode),
			}
			providerState.accounts[entry.AccountName] = accountState
		}

		model := apiIntegrationCurrentModelBreakdown{
			Model:             entry.Model,
			RequestCount:      entry.RequestCount,
			PromptTokens:      entry.PromptTokens,
			CompletionTokens:  entry.CompletionTokens,
			TotalTokens:       entry.TotalTokens,
			InputTokens:       entry.InputTokens,
			CachedTokens:      entry.CachedTokens,
			CacheCreateTokens: entry.CacheCreateTokens,
			OutputTokens:      entry.OutputTokens,
			ReasoningTokens:   entry.ReasoningTokens,
			LastCapturedAt:    entry.LastCapturedAt.UTC().Format(time.RFC3339),
		}
		if entry.TotalCostUSD > 0 {
			cost := entry.TotalCostUSD
			model.TotalCostUSD = &cost
		}
		accountState.models[entry.Model] = &modelNode{row: model}

		acc := &accountState.row
		acc.RequestCount += entry.RequestCount
		acc.PromptTokens += entry.PromptTokens
		acc.CompletionTokens += entry.CompletionTokens
		acc.TotalTokens += entry.TotalTokens
		acc.InputTokens += entry.InputTokens
		acc.CachedTokens += entry.CachedTokens
		acc.CacheCreateTokens += entry.CacheCreateTokens
		acc.OutputTokens += entry.OutputTokens
		acc.ReasoningTokens += entry.ReasoningTokens
		acc.LastCapturedAt = laterTimeString(acc.LastCapturedAt, entry.LastCapturedAt)
		if entry.TotalCostUSD > 0 {
			var current float64
			if acc.TotalCostUSD != nil {
				current = *acc.TotalCostUSD
			}
			current += entry.TotalCostUSD
			acc.TotalCostUSD = &current
		}

		prov := &providerState.row
		prov.RequestCount += entry.RequestCount
		prov.PromptTokens += entry.PromptTokens
		prov.CompletionTokens += entry.CompletionTokens
		prov.TotalTokens += entry.TotalTokens
		prov.InputTokens += entry.InputTokens
		prov.CachedTokens += entry.CachedTokens
		prov.CacheCreateTokens += entry.CacheCreateTokens
		prov.OutputTokens += entry.OutputTokens
		prov.ReasoningTokens += entry.ReasoningTokens
		prov.LastCapturedAt = laterTimeString(prov.LastCapturedAt, entry.LastCapturedAt)
		if entry.TotalCostUSD > 0 {
			var current float64
			if prov.TotalCostUSD != nil {
				current = *prov.TotalCostUSD
			}
			current += entry.TotalCostUSD
			prov.TotalCostUSD = &current
		}

		integrationState.RequestCount += entry.RequestCount
		integrationState.PromptTokens += entry.PromptTokens
		integrationState.CompletionTokens += entry.CompletionTokens
		integrationState.TotalTokens += entry.TotalTokens
		integrationState.InputTokens += entry.InputTokens
		integrationState.CachedTokens += entry.CachedTokens
		integrationState.CacheCreateTokens += entry.CacheCreateTokens
		integrationState.OutputTokens += entry.OutputTokens
		integrationState.ReasoningTokens += entry.ReasoningTokens
		integrationState.TotalCostUSD += entry.TotalCostUSD
		integrationState.HasCost = integrationState.HasCost || entry.TotalCostUSD > 0
		if entry.LastCapturedAt.After(integrationState.LastCapturedAt) {
			integrationState.LastCapturedAt = entry.LastCapturedAt
		}
	}

	for _, entry := range effortRows {
		integrationState, ok := integrationsMap[entry.IntegrationName]
		if !ok {
			continue
		}
		providerState, ok := integrationState.Providers[entry.Provider]
		if !ok {
			continue
		}
		accountState, ok := providerState.accounts[entry.AccountName]
		if !ok {
			continue
		}
		modelState, ok := accountState.models[entry.Model]
		if !ok {
			continue
		}
		effort := apiIntegrationCurrentEffortBreakdown{
			ReasoningEffort:   entry.ReasoningEffort,
			Mode:              entry.Mode,
			SpeedMode:         entry.SpeedMode,
			RequestCount:      entry.RequestCount,
			PromptTokens:      entry.PromptTokens,
			CompletionTokens:  entry.CompletionTokens,
			TotalTokens:       entry.TotalTokens,
			InputTokens:       entry.InputTokens,
			CachedTokens:      entry.CachedTokens,
			CacheCreateTokens: entry.CacheCreateTokens,
			OutputTokens:      entry.OutputTokens,
			ReasoningTokens:   entry.ReasoningTokens,
			LastCapturedAt:    entry.LastCapturedAt.UTC().Format(time.RFC3339),
		}
		if entry.TotalCostUSD > 0 {
			cost := entry.TotalCostUSD
			effort.TotalCostUSD = &cost
		}
		modelState.row.Efforts = append(modelState.row.Efforts, effort)
	}

	for integrationName, integrationState := range integrationsMap {
		providers := make([]apiIntegrationCurrentProviderBreakdown, 0, len(integrationState.Providers))
		for _, providerState := range integrationState.Providers {
			accounts := make([]apiIntegrationCurrentAccountBreakdown, 0, len(providerState.accounts))
			for _, accountState := range providerState.accounts {
				models := make([]apiIntegrationCurrentModelBreakdown, 0, len(accountState.models))
				for _, modelState := range accountState.models {
					sortAPIIntegrationEfforts(modelState.row.Efforts)
					models = append(models, modelState.row)
				}
				sortAPIIntegrationModels(models)
				accountState.row.Models = models
				accounts = append(accounts, accountState.row)
			}
			sortAPIIntegrationAccounts(accounts)
			providerState.row.Accounts = accounts
			providers = append(providers, providerState.row)
		}
		sortAPIIntegrationProviders(providers)

		item := map[string]interface{}{
			"integration":              integrationName,
			"requestCount":             integrationState.RequestCount,
			"promptTokens":             integrationState.PromptTokens,
			"completionTokens":         integrationState.CompletionTokens,
			"totalTokens":              integrationState.TotalTokens,
			"inputTokens":              integrationState.InputTokens,
			"cachedInputTokens":        integrationState.CachedTokens,
			"cacheCreationInputTokens": integrationState.CacheCreateTokens,
			"outputTokens":             integrationState.OutputTokens,
			"reasoningTokens":          integrationState.ReasoningTokens,
			"lastCapturedAt":           integrationState.LastCapturedAt.UTC().Format(time.RFC3339),
			"providers":                providers,
		}
		if integrationState.HasCost {
			item["totalCostUsd"] = integrationState.TotalCostUSD
		}
		response[integrationName] = item
	}

	return response
}

func summarizeAPIIntegrationUsage(effortRows []store.APIIntegrationUsageEffortSummaryRow) []store.APIIntegrationUsageSummaryRow {
	type summaryKey struct {
		integration string
		provider    string
		account     string
		model       string
	}

	summaries := make(map[summaryKey]*store.APIIntegrationUsageSummaryRow)
	for _, effort := range effortRows {
		key := summaryKey{
			integration: effort.IntegrationName,
			provider:    effort.Provider,
			account:     effort.AccountName,
			model:       effort.Model,
		}
		summary := summaries[key]
		if summary == nil {
			summary = &store.APIIntegrationUsageSummaryRow{
				IntegrationName: effort.IntegrationName,
				Provider:        effort.Provider,
				AccountName:     effort.AccountName,
				Model:           effort.Model,
			}
			summaries[key] = summary
		}
		summary.RequestCount += effort.RequestCount
		summary.PromptTokens += effort.PromptTokens
		summary.CompletionTokens += effort.CompletionTokens
		summary.TotalTokens += effort.TotalTokens
		summary.InputTokens += effort.InputTokens
		summary.CachedTokens += effort.CachedTokens
		summary.CacheCreateTokens += effort.CacheCreateTokens
		summary.OutputTokens += effort.OutputTokens
		summary.ReasoningTokens += effort.ReasoningTokens
		summary.TotalCostUSD += effort.TotalCostUSD
		if effort.LastCapturedAt.After(summary.LastCapturedAt) {
			summary.LastCapturedAt = effort.LastCapturedAt
		}
	}

	rows := make([]store.APIIntegrationUsageSummaryRow, 0, len(summaries))
	for _, summary := range summaries {
		rows = append(rows, *summary)
	}
	sort.Slice(rows, func(i, j int) bool {
		left := rows[i]
		right := rows[j]
		if left.IntegrationName != right.IntegrationName {
			return left.IntegrationName < right.IntegrationName
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		if left.AccountName != right.AccountName {
			return left.AccountName < right.AccountName
		}
		return left.Model < right.Model
	})
	return rows
}

// APIIntegrationsHistory returns chart-ready aggregated history grouped by integration.
func (h *Handler) APIIntegrationsHistory(w http.ResponseWriter, r *http.Request) {
	window, err := historyWindowForRequest(r, time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	version := h.apiIntegrationUsageVersion()
	now := time.Now()
	h.setAPIIntegrationArchiveHeaders(w, window.Start, window.End)
	key := "history:" + window.CacheKey
	if cached, ok := h.apiIntegrationsCache.get(key, version, now); ok {
		respondJSON(w, http.StatusOK, cached)
		return
	}
	payload := h.buildAPIIntegrationsHistory(window.Start, window.End)
	h.apiIntegrationsCache.set(key, version, now, payload)
	respondJSON(w, http.StatusOK, payload)
}

// APIIntegrationsSessions returns chat/session-level usage for cost graphs.
func (h *Handler) APIIntegrationsSessions(w http.ResponseWriter, r *http.Request) {
	window, err := historyWindowForRequest(r, time.Now().UTC())
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	integration := r.URL.Query().Get("integration")
	version := h.apiIntegrationUsageVersion()
	now := time.Now()
	h.setAPIIntegrationArchiveHeaders(w, window.Start, window.End)
	includeSessions := !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("includeSessions")), "false")
	key := "sessions:" + window.CacheKey + ":" + integration + fmt.Sprintf(":%t", includeSessions)
	if cached, ok := h.apiIntegrationsCache.get(key, version, now); ok {
		respondJSON(w, http.StatusOK, cached)
		return
	}
	payload := h.buildAPIIntegrationsSessions(window.Start, window.End, integration, includeSessions)
	h.apiIntegrationsCache.set(key, version, now, payload)
	respondJSON(w, http.StatusOK, payload)
}

func (h *Handler) buildAPIIntegrationsSessions(start, end time.Time, integration string, includeSessions bool) map[string]interface{} {
	response := map[string]interface{}{}
	if h.store == nil {
		return response
	}

	totals, err := h.store.QueryAPIIntegrationUsageTotals(start, end, integration)
	if err != nil {
		h.logger.Error("failed to query API integrations session totals", "error", err)
		return response
	}
	modelTotals, err := h.store.QueryAPIIntegrationUsageEffortTotals(start, end, integration)
	if err != nil {
		h.logger.Error("failed to query API integrations model totals", "error", err)
		return response
	}
	var rows []store.APIIntegrationUsageSessionRow
	if includeSessions {
		rows, err = h.store.QueryAPIIntegrationUsageSessions(start, end, integration, maxChartPoints*10)
		if err != nil {
			h.logger.Error("failed to query API integrations sessions", "error", err)
			return response
		}
	}

	totalsByIntegration := make(map[string]interface{})
	for _, row := range totals {
		entry := map[string]interface{}{
			"requestCount":             row.RequestCount,
			"promptTokens":             row.PromptTokens,
			"completionTokens":         row.CompletionTokens,
			"totalTokens":              row.TotalTokens,
			"inputTokens":              row.InputTokens,
			"cachedInputTokens":        row.CachedTokens,
			"cacheCreationInputTokens": row.CacheCreateTokens,
			"outputTokens":             row.OutputTokens,
			"reasoningTokens":          row.ReasoningTokens,
		}
		if row.TotalCostUSD > 0 {
			entry["totalCostUsd"] = row.TotalCostUSD
		}
		if !row.LastCapturedAt.IsZero() {
			entry["lastCapturedAt"] = row.LastCapturedAt.UTC().Format(time.RFC3339)
		}
		totalsByIntegration[row.IntegrationName] = entry
	}
	response["_totals"] = totalsByIntegration

	modelsByIntegration := make(map[string][]map[string]interface{})
	for _, row := range modelTotals {
		entry := map[string]interface{}{
			"model":                    row.Model,
			"reasoningEffort":          row.ReasoningEffort,
			"mode":                     row.Mode,
			"speedMode":                row.SpeedMode,
			"requestCount":             row.RequestCount,
			"promptTokens":             row.PromptTokens,
			"completionTokens":         row.CompletionTokens,
			"totalTokens":              row.TotalTokens,
			"inputTokens":              row.InputTokens,
			"cachedInputTokens":        row.CachedTokens,
			"cacheCreationInputTokens": row.CacheCreateTokens,
			"outputTokens":             row.OutputTokens,
			"reasoningTokens":          row.ReasoningTokens,
			"lastCapturedAt":           row.LastCapturedAt.UTC().Format(time.RFC3339),
		}
		if row.TotalCostUSD > 0 {
			entry["totalCostUsd"] = row.TotalCostUSD
		}
		modelsByIntegration[row.IntegrationName] = append(modelsByIntegration[row.IntegrationName], entry)
	}
	response["_models"] = modelsByIntegration

	byIntegration := make(map[string][]map[string]interface{})
	for _, row := range rows {
		entry := map[string]interface{}{
			"capturedAt":               row.StartedAt.UTC().Format(time.RFC3339),
			"lastCapturedAt":           row.LastCapturedAt.UTC().Format(time.RFC3339),
			"sessionId":                row.SessionID,
			"chatDate":                 row.ChatDate,
			"requestCount":             row.RequestCount,
			"promptTokens":             row.PromptTokens,
			"completionTokens":         row.CompletionTokens,
			"totalTokens":              row.TotalTokens,
			"inputTokens":              row.InputTokens,
			"cachedInputTokens":        row.CachedTokens,
			"cacheCreationInputTokens": row.CacheCreateTokens,
			"outputTokens":             row.OutputTokens,
			"reasoningTokens":          row.ReasoningTokens,
		}
		if row.TotalCostUSD > 0 {
			entry["totalCostUsd"] = row.TotalCostUSD
		}
		byIntegration[row.IntegrationName] = append(byIntegration[row.IntegrationName], entry)
	}

	for integrationName, entries := range byIntegration {
		var cumulativeCost float64
		var cumulativeTokens int
		for _, entry := range entries {
			cumulativeCost += numberFromInterface(entry["totalCostUsd"])
			cumulativeTokens += int(numberFromInterface(entry["totalTokens"]))
			entry["cumulativeCostUsd"] = cumulativeCost
			entry["cumulativeTotalTokens"] = cumulativeTokens
		}
		if total, ok := totalsByIntegration[integrationName].(map[string]interface{}); ok && len(entries) > 0 {
			last := entries[len(entries)-1]
			if totalCost, ok := total["totalCostUsd"]; ok {
				last["cumulativeCostUsd"] = numberFromInterface(totalCost)
			}
			if totalTokens, ok := total["totalTokens"]; ok {
				last["cumulativeTotalTokens"] = int(numberFromInterface(totalTokens))
			}
		}
		step := downsampleStep(len(entries), maxChartPoints)
		if step <= 1 {
			response[integrationName] = entries
			continue
		}
		downsampled := make([]map[string]interface{}, 0, min(len(entries), maxChartPoints))
		last := len(entries) - 1
		for index, entry := range entries {
			if index != 0 && index != last && index%step != 0 {
				continue
			}
			downsampled = append(downsampled, entry)
		}
		response[integrationName] = downsampled
	}

	return response
}

func (h *Handler) buildAPIIntegrationsHistory(start, end time.Time) map[string]interface{} {
	response := map[string]interface{}{}
	if h.store == nil {
		return response
	}

	duration := end.Sub(start)
	bucketSize := apiIntegrationHistoryBucketSize(duration)
	rows, err := h.store.QueryAPIIntegrationUsageBuckets(start, end, bucketSize)
	if err != nil {
		h.logger.Error("failed to query API integrations history", "error", err)
		return response
	}

	byIntegration := make(map[string][]map[string]interface{})
	for _, row := range rows {
		entry := map[string]interface{}{
			"capturedAt":               row.BucketStart.UTC().Format(time.RFC3339),
			"requestCount":             row.RequestCount,
			"promptTokens":             row.PromptTokens,
			"completionTokens":         row.CompletionTokens,
			"totalTokens":              row.TotalTokens,
			"inputTokens":              row.InputTokens,
			"cachedInputTokens":        row.CachedTokens,
			"cacheCreationInputTokens": row.CacheCreateTokens,
			"outputTokens":             row.OutputTokens,
			"reasoningTokens":          row.ReasoningTokens,
		}
		if row.TotalCostUSD > 0 {
			entry["totalCostUsd"] = row.TotalCostUSD
		}
		byIntegration[row.IntegrationName] = append(byIntegration[row.IntegrationName], entry)
	}

	for integrationName, entries := range byIntegration {
		step := downsampleStep(len(entries), maxChartPoints)
		if step <= 1 {
			response[integrationName] = entries
			continue
		}
		downsampled := make([]map[string]interface{}, 0, min(len(entries), maxChartPoints))
		last := len(entries) - 1
		for index, entry := range entries {
			if index != 0 && index != last && index%step != 0 {
				continue
			}
			downsampled = append(downsampled, entry)
		}
		response[integrationName] = downsampled
	}

	return response
}

func (h *Handler) setAPIIntegrationArchiveHeaders(w http.ResponseWriter, start, end time.Time) {
	retentionDays := 30
	if h.config != nil && h.config.APIIntegrationsRetention > 0 {
		retentionDays = max(1, int(h.config.APIIntegrationsRetention/(24*time.Hour)))
	}
	usesArchive := false
	if h.store != nil {
		if archived, err := h.store.APIIntegrationUsageUsesArchive(start, end); err == nil {
			usesArchive = archived
		} else {
			h.logger.Error("failed to check API integrations archive range", "error", err)
		}
	}
	w.Header().Set("X-OnWatch-Archived-Data", strconv.FormatBool(usesArchive))
	w.Header().Set("X-OnWatch-Archive-Resolution", "1h")
	w.Header().Set("X-OnWatch-Raw-Retention-Days", strconv.Itoa(retentionDays))
}

func (h *Handler) apiIntegrationUsageVersion() uint64 {
	if h == nil || h.store == nil {
		return 0
	}
	return h.store.APIIntegrationUsageVersion()
}

func numberFromInterface(value interface{}) float64 {
	switch v := value.(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	case float32:
		return float64(v)
	default:
		return 0
	}
}

// APIIntegrationsHealth returns ingest subsystem status for API integrations telemetry.
func (h *Handler) APIIntegrationsHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildAPIIntegrationsHealth())
}

func (h *Handler) buildAPIIntegrationsHealth() map[string]interface{} {
	response := map[string]interface{}{
		"enabled": false,
		"dir":     "",
		"running": false,
		"files":   []map[string]interface{}{},
		"alerts":  []map[string]interface{}{},
	}
	if h.config != nil {
		response["enabled"] = h.config.APIIntegrationsEnabled
		response["dir"] = h.config.APIIntegrationsDir
	}
	if enabled, _ := response["enabled"].(bool); !enabled {
		return response
	}
	if h.agentManager != nil {
		response["running"] = h.agentManager.IsRunning("api_integrations")
	}
	if h.store == nil {
		return response
	}

	files, err := h.store.QueryAPIIntegrationIngestHealth()
	if err == nil {
		payload := make([]map[string]interface{}, 0, len(files))
		for _, file := range files {
			item := map[string]interface{}{
				"sourcePath":  file.SourcePath,
				"offsetBytes": file.OffsetBytes,
				"fileSize":    file.FileSize,
				"partialLine": file.PartialLine,
				"updatedAt":   file.UpdatedAt.UTC().Format(time.RFC3339),
			}
			if file.FileModTime != nil {
				item["fileModTime"] = file.FileModTime.UTC().Format(time.RFC3339)
			}
			if file.LastCapturedAt != nil {
				item["lastCapturedAt"] = file.LastCapturedAt.UTC().Format(time.RFC3339)
			}
			payload = append(payload, item)
		}
		response["files"] = payload
	}

	alerts, err := h.store.GetActiveSystemAlertsByProvider("api_integrations", 20)
	if err == nil {
		payload := make([]map[string]interface{}, 0, len(alerts))
		for _, alert := range alerts {
			item := map[string]interface{}{
				"id":        alert.ID,
				"type":      alert.AlertType,
				"title":     alert.Title,
				"message":   alert.Message,
				"severity":  alert.Severity,
				"createdAt": alert.CreatedAt.UTC().Format(time.RFC3339),
			}
			if alert.Metadata != "" {
				item["metadata"] = alert.Metadata
			}
			payload = append(payload, item)
		}
		response["alerts"] = payload
	}

	return response
}

func apiIntegrationHistoryBucketSize(duration time.Duration) time.Duration {
	switch {
	case duration <= time.Hour:
		return time.Minute
	case duration <= 6*time.Hour:
		return 5 * time.Minute
	case duration <= 24*time.Hour:
		return 15 * time.Minute
	case duration <= 7*24*time.Hour:
		return time.Hour
	default:
		return 6 * time.Hour
	}
}

func laterTimeString(current string, candidate time.Time) string {
	if current == "" {
		return candidate.UTC().Format(time.RFC3339)
	}
	parsed, err := time.Parse(time.RFC3339, current)
	if err != nil || candidate.After(parsed) {
		return candidate.UTC().Format(time.RFC3339)
	}
	return current
}

func sortAPIIntegrationProviders(items []apiIntegrationCurrentProviderBreakdown) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Provider < items[j].Provider
	})
}

func sortAPIIntegrationAccounts(items []apiIntegrationCurrentAccountBreakdown) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Account < items[j].Account
	})
}

func sortAPIIntegrationModels(items []apiIntegrationCurrentModelBreakdown) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Model < items[j].Model
	})
}

func sortAPIIntegrationEfforts(items []apiIntegrationCurrentEffortBreakdown) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].ModelSortKey() != items[j].ModelSortKey() {
			return items[i].ModelSortKey() < items[j].ModelSortKey()
		}
		return items[i].LastCapturedAt < items[j].LastCapturedAt
	})
}

func (item apiIntegrationCurrentEffortBreakdown) ModelSortKey() string {
	return item.ReasoningEffort + "\x00" + item.SpeedMode + "\x00" + item.Mode
}
