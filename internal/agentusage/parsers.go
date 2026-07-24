package agentusage

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func ParseClaudeUsageLine(line []byte, sourcePath string, pricing *PricingMap) (*UsageEvent, error) {
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		return nil, err
	}
	message := object(obj["message"])
	usage := object(message["usage"])
	if len(usage) == 0 {
		usage = object(obj["usage"])
	}
	cacheCreation1h := intValue(object(usage["cache_creation"]), "ephemeral_1h_input_tokens", "1h_input_tokens")
	counts := TokenCounts{
		InputTokens:           intValue(usage, "input_tokens", "input"),
		CachedInputTokens:     intValue(usage, "cache_read_input_tokens", "cached_input_tokens", "cached"),
		CacheCreationTokens:   intValue(usage, "cache_creation_input_tokens", "cache_creation_tokens"),
		CacheCreation1hTokens: cacheCreation1h,
		OutputTokens:          intValue(usage, "output_tokens", "output"),
		ReasoningTokens:       intValue(usage, "reasoning_output_tokens", "reasoning_tokens", "thinking_tokens"),
		TotalTokens:           intValue(usage, "total_tokens", "total"),
	}
	if counts.TotalTokens <= 0 {
		counts.TotalTokens = counts.InputTokens + counts.CachedInputTokens + counts.CacheCreationTokens + counts.OutputTokens + counts.ReasoningTokens
	}
	model := firstString(message, "model", "model_name")
	if model == "" {
		model = firstString(obj, "model", "model_name")
	}
	event := UsageEvent{
		Timestamp:             timeValue(obj, "timestamp", "ts"),
		Source:                "claude",
		Provider:              "anthropic",
		SessionID:             firstString(obj, "sessionId", "session_id"),
		RequestID:             firstString(obj, "requestId", "request_id"),
		Model:                 model,
		InputTokens:           counts.InputTokens,
		CachedInputTokens:     counts.CachedInputTokens,
		CacheCreationTokens:   counts.CacheCreationTokens,
		CacheCreation1hTokens: counts.CacheCreation1hTokens,
		OutputTokens:          counts.OutputTokens,
		ReasoningTokens:       counts.ReasoningTokens,
		TotalTokens:           counts.TotalTokens,
		CostUSD:               floatValue(obj, "costUSD", "cost_usd"),
		SourcePath:            sourcePath,
	}
	if event.SessionID == "" {
		event.SessionID = strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
	}
	if event.RequestID == "" {
		event.RequestID = firstString(message, "id")
	}
	if event.CostUSD <= 0 {
		costOptions := CostOptions{}
		if claudeUsageIsFast(usage) {
			if multiplier := claudeFastModeCostMultiplier(event.Model); multiplier > 0 {
				costOptions.CostMultiplier = multiplier
				event.SpeedMode = "fast"
				event.SpeedMultiplier = multiplier
				event.SpeedSource = "claude_usage"
			}
		}
		event.CostUSD = pricing.CalculateCostAt(event.Model, event.Timestamp, counts, costOptions)
	}
	return &event, nil
}

// claudeUsageIsFast reports whether a Claude Code usage block was billed at the
// fast/priority tier. Claude Code records this in usage.speed ("fast") and/or
// usage.service_tier ("priority").
func claudeUsageIsFast(usage map[string]any) bool {
	switch strings.ToLower(firstString(usage, "speed")) {
	case "fast", "priority":
		return true
	}
	return strings.ToLower(firstString(usage, "service_tier")) == "priority"
}

// claudeFastModeCostMultiplier returns the fast-mode cost multiplier for a
// Claude model, relative to standard pricing. Per Anthropic's published fast-
// mode rates: Opus 4.8 bills 2x ($10/$50 vs $5/$25); Opus 4.6 and 4.7 bill 6x
// ($30/$150 vs $5/$25). The multiplier applies across all token categories,
// since cache rates are defined as multipliers of the (raised) base input
// price. Returns 0 (no adjustment) for models without a known fast tier.
func claudeFastModeCostMultiplier(model string) float64 {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(m, "claude-opus-4-8"):
		return 2.0
	case strings.HasPrefix(m, "claude-opus-4-7"), strings.HasPrefix(m, "claude-opus-4-6"):
		return 6.0
	default:
		return 0
	}
}

func ParseCodexUsageFile(path string, pricing *PricingMap) ([]UsageEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	sessionID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	currentModel := ""
	currentReasoningEffort := ""
	currentMode := ""
	var currentFastMode *bool
	speed := codexSpeedContext(path)
	seenUsage := make(map[string]struct{})
	var events []UsageEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		payload := object(obj["payload"])
		if typ := firstString(obj, "type"); typ == "turn_context" {
			if model := firstString(payload, "model", "model_name"); model != "" {
				currentModel = model
			}
			if effort := codexReasoningEffort(payload); effort != "" {
				currentReasoningEffort = effort
			}
			if mode := codexMode(payload); mode != "" {
				currentMode = mode
			}
			if fastMode, ok := boolValue(payload, "fast_mode", "fastMode", "use_fast_model", "useFastModel"); ok {
				currentFastMode = &fastMode
			}
			continue
		}
		usage := map[string]any(nil)
		usageSignature := ""
		contentSignature := ""
		if firstString(obj, "type") == "event_msg" && firstString(payload, "type") == "token_count" {
			info := object(payload["info"])
			usage = object(info["last_token_usage"])
			if len(usage) == 0 {
				usage = object(info["total_token_usage"])
			}
			usageSignature = codexTokenCountSignature(info, usage)
			contentSignature = usageSignature
		}
		if len(usage) == 0 {
			usage = object(obj["usage"])
			usageSignature = codexUsageSignature(usage)
		}
		if len(usage) == 0 {
			for _, key := range []string{"data", "result", "response"} {
				nested := object(obj[key])
				usage = object(nested["usage"])
				if len(usage) > 0 {
					if model := firstString(nested, "model", "model_name"); model != "" {
						currentModel = model
					}
					usageSignature = codexUsageSignature(usage)
					break
				}
			}
		}
		if len(usage) == 0 {
			continue
		}
		if model := firstString(obj, "model", "model_name"); model != "" {
			currentModel = model
		}
		if currentModel == "" {
			continue
		}
		if usageSignature != "" {
			key := strings.Join([]string{currentModel, currentReasoningEffort, currentMode, usageSignature}, "\x00")
			if _, ok := seenUsage[key]; ok {
				continue
			}
			seenUsage[key] = struct{}{}
		}
		counts := codexCounts(usage)
		event := UsageEvent{
			Timestamp:           timeValue(obj, "timestamp", "ts"),
			Source:              "codex",
			Provider:            "openai",
			SessionID:           sessionID,
			RequestID:           firstString(obj, "request_id", "id"),
			Model:               currentModel,
			ReasoningEffort:     currentReasoningEffort,
			Mode:                currentMode,
			FastMode:            currentFastMode,
			SpeedMode:           speed.Mode,
			SpeedSource:         speed.Source,
			InputTokens:         counts.InputTokens,
			CachedInputTokens:   counts.CachedInputTokens,
			CacheCreationTokens: counts.CacheCreationTokens,
			OutputTokens:        counts.OutputTokens,
			ReasoningTokens:     counts.ReasoningTokens,
			TotalTokens:         counts.TotalTokens,
			SourcePath:          path,
			UsageSignature:      contentSignature,
		}
		costOptions := CostOptions{}
		if currentFastMode != nil && *currentFastMode {
			costOptions.CostMultiplier = codexFastModeCostMultiplier(event.Model)
		} else if speed.Mode == "fast" {
			costOptions.CostMultiplier = codexFastModeCostMultiplier(event.Model)
		}
		event.SpeedMultiplier = costOptions.CostMultiplier
		event.CostUSD = pricing.CalculateCostAt(event.Model, event.Timestamp, counts, costOptions)
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

type codexSpeedInfo struct {
	Mode       string
	Multiplier float64
	Source     string
}

func codexSpeedContext(sessionPath string) codexSpeedInfo {
	if codexSessionIsArchived(sessionPath) {
		return codexSpeedInfo{}
	}
	if info := codexConfigSpeedContext(sessionPath); info.Mode != "" {
		return info
	}
	statePath := codexGlobalStatePath(sessionPath)
	if statePath == "" {
		return codexSpeedInfo{}
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		return codexSpeedInfo{}
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		return codexSpeedInfo{}
	}
	atoms := object(state["electron-persisted-atom-state"])
	tier := strings.ToLower(strings.TrimSpace(firstString(atoms, "default-service-tier", "service_tier", "serviceTier")))
	switch tier {
	case "fast", "priority":
		return codexSpeedInfo{Mode: "fast", Source: "codex_global_state"}
	case "standard", "default", "auto":
		return codexSpeedInfo{Mode: "standard", Source: "codex_global_state"}
	default:
		return codexSpeedInfo{}
	}
}

func codexSessionIsArchived(sessionPath string) bool {
	for dir := filepath.Clean(filepath.Dir(sessionPath)); ; dir = filepath.Dir(dir) {
		if strings.EqualFold(filepath.Base(dir), "archived_sessions") {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
	}
}

func codexConfigSpeedContext(sessionPath string) codexSpeedInfo {
	dir := filepath.Clean(filepath.Dir(sessionPath))
	for {
		configPath := filepath.Join(dir, "config.toml")
		data, err := os.ReadFile(configPath)
		if err == nil {
			switch codexConfigServiceTier(string(data)) {
			case "fast", "priority":
				return codexSpeedInfo{Mode: "fast", Source: "codex_config"}
			case "standard", "default", "auto":
				return codexSpeedInfo{Mode: "standard", Source: "codex_config"}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return codexSpeedInfo{}
		}
		dir = parent
	}
}

func codexConfigServiceTier(content string) string {
	for _, line := range strings.Split(content, "\n") {
		setting := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		key, value, ok := strings.Cut(setting, "=")
		if !ok || strings.TrimSpace(key) != "service_tier" {
			continue
		}
		return strings.ToLower(strings.Trim(strings.TrimSpace(value), `"'`))
	}
	return ""
}

func codexGlobalStatePath(sessionPath string) string {
	dir := filepath.Clean(filepath.Dir(sessionPath))
	for {
		if strings.EqualFold(filepath.Base(dir), ".codex") {
			return filepath.Join(dir, ".codex-global-state.json")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func codexTokenCountSignature(info, usage map[string]any) string {
	total := object(info["total_token_usage"])
	if len(total) > 0 {
		return codexUsageSignature(usage) + "\x00total:" + codexUsageSignature(total)
	}
	return codexUsageSignature(usage)
}

func codexUsageSignature(usage map[string]any) string {
	if len(usage) == 0 {
		return ""
	}
	counts := codexCounts(usage)
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d",
		counts.InputTokens,
		counts.CachedInputTokens,
		counts.CacheCreationTokens,
		counts.OutputTokens,
		counts.ReasoningTokens,
		counts.TotalTokens,
	)
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

func ParseGeminiUsageFile(path, source, provider string, pricing *PricingMap) ([]UsageEvent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if filepath.Ext(path) == ".jsonl" {
		return parseGeminiJSONL(data, path, source, provider, pricing)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	event := geminiEventFromObject(obj, path, source, provider, pricing)
	if event.Model == "" || event.TotalTokens <= 0 {
		return nil, nil
	}
	return []UsageEvent{event}, nil
}

func ParseAntigravitySettingsFile(path string, pricing *PricingMap) (*UsageEvent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	usage := object(obj["tokenUsage"])
	if len(usage) == 0 {
		return nil, nil
	}
	model := normalizeAntigravityModel(firstString(obj, "model"))
	if model == "" {
		model = defaultAntigravityModel(providerFromAntigravityLock(firstString(obj, "providerLock")))
	}
	provider := providerFromAntigravityLock(firstString(obj, "providerLock"))
	counts := TokenCounts{
		InputTokens:         intValue(usage, "inputTokens", "input_tokens", "input"),
		CachedInputTokens:   intValue(usage, "cacheReadTokens", "cache_read_input_tokens", "cached_input_tokens", "cached"),
		CacheCreationTokens: intValue(usage, "cacheCreationTokens", "cache_creation_input_tokens", "cache_creation_tokens"),
		OutputTokens:        intValue(usage, "outputTokens", "output_tokens", "output"),
		ReasoningTokens:     intValue(usage, "thinkingTokens", "reasoning_output_tokens", "reasoning_tokens", "thoughts"),
		TotalTokens:         intValue(usage, "totalTokens", "total_tokens", "total"),
	}
	if counts.TotalTokens <= 0 {
		counts.TotalTokens = counts.InputTokens + counts.CachedInputTokens + counts.CacheCreationTokens + counts.OutputTokens + counts.ReasoningTokens
	}
	input := counts.InputTokens - counts.CachedInputTokens
	if input < 0 {
		input = 0
	}
	counts.InputTokens = input
	event := UsageEvent{
		Timestamp:           timeValue(obj, "providerLockTimestamp", "timestamp", "ts"),
		Source:              "antigravity",
		Provider:            provider,
		SessionID:           strings.TrimSuffix(filepath.Base(path), ".settings.json"),
		Model:               model,
		InputTokens:         counts.InputTokens,
		CachedInputTokens:   counts.CachedInputTokens,
		CacheCreationTokens: counts.CacheCreationTokens,
		OutputTokens:        counts.OutputTokens,
		ReasoningTokens:     counts.ReasoningTokens,
		TotalTokens:         counts.TotalTokens,
		SourcePath:          path,
	}
	event.CostUSD = pricing.CalculateCostAt(event.Model, event.Timestamp, counts, CostOptions{
		ReasoningBilledAsOutput: true,
		ProviderPrefixes:        []string{"google", "vertex_ai", "openrouter/google", "anthropic", "openai"},
	})
	return &event, nil
}

func ParseCursorUsageCSV(data []byte, sourcePath string) ([]UsageEvent, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) < 2 {
		return nil, nil
	}
	headers := make(map[string]int, len(records[0]))
	for i, header := range records[0] {
		headers[normalizeHeader(header)] = i
	}
	var events []UsageEvent
	for _, record := range records[1:] {
		if len(record) == 0 {
			continue
		}
		inputRaw := csvInt(record, headers, "inputtokens", "input")
		cached := csvInt(record, headers, "cachedinputtokens", "cachedtokens", "cached")
		input := inputRaw - cached
		if input < 0 {
			input = 0
		}
		output := csvInt(record, headers, "outputtokens", "output")
		reasoning := csvInt(record, headers, "reasoningoutputtokens", "reasoningtokens")
		total := csvInt(record, headers, "totaltokens", "total")
		if total <= 0 {
			total = inputRaw + output + reasoning
		}
		event := UsageEvent{
			Timestamp:         csvTime(record, headers, "date", "timestamp", "time"),
			Source:            "cursor",
			Provider:          "cursor",
			Account:           csvString(record, headers, "useremail", "email", "user"),
			Model:             csvString(record, headers, "model", "modelname"),
			InputTokens:       input,
			CachedInputTokens: cached,
			OutputTokens:      output,
			ReasoningTokens:   reasoning,
			TotalTokens:       total,
			CostUSD:           csvFloat(record, headers, "usagebasedprice", "cost", "costusd", "price"),
			SourcePath:        sourcePath,
		}
		if event.Model == "" || event.TotalTokens <= 0 {
			continue
		}
		events = append(events, event)
	}
	return events, nil
}

func ParseCursorStateDB(path string, pricing *PricingMap) ([]UsageEvent, error) {
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	composerModels, err := cursorComposerModels(db)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT key, value FROM cursorDiskKV WHERE key LIKE 'bubbleId:%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []UsageEvent
	for rows.Next() {
		var key string
		var value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if !value.Valid {
			continue
		}
		event, ok := cursorEventFromBubble(key, []byte(value.String), composerModels, path, pricing)
		if ok {
			events = append(events, event)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func cursorComposerModels(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT key, value FROM cursorDiskKV WHERE key LIKE 'composerData:%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	models := make(map[string]string)
	for rows.Next() {
		var key string
		var value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if !value.Valid {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(value.String), &obj); err != nil {
			continue
		}
		modelConfig := object(obj["modelConfig"])
		model := normalizeCursorModel(firstString(modelConfig, "modelName", "model", "name"))
		if model != "" {
			models[strings.TrimPrefix(key, "composerData:")] = model
		}
	}
	return models, rows.Err()
}

func cursorEventFromBubble(key string, data []byte, composerModels map[string]string, sourcePath string, pricing *PricingMap) (UsageEvent, bool) {
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return UsageEvent{}, false
	}
	counts := cursorTokenCounts(object(obj["tokenCount"]))
	if counts.TotalTokens <= 0 {
		return UsageEvent{}, false
	}
	composerID := cursorComposerIDFromBubbleKey(key)
	modelInfo := object(obj["modelInfo"])
	model := normalizeCursorModel(firstString(modelInfo, "modelName", "model", "name"))
	if model == "" {
		model = composerModels[composerID]
	}
	if model == "" {
		model = "cursor-unknown"
	}
	event := UsageEvent{
		Timestamp:           timeValue(obj, "createdAt", "timestamp"),
		Source:              "cursor",
		Provider:            "cursor",
		SessionID:           composerID,
		RequestID:           firstString(obj, "requestId", "request_id", "bubbleId"),
		Model:               model,
		Mode:                cursorMode(obj),
		InputTokens:         counts.InputTokens,
		CachedInputTokens:   counts.CachedInputTokens,
		CacheCreationTokens: counts.CacheCreationTokens,
		OutputTokens:        counts.OutputTokens,
		ReasoningTokens:     counts.ReasoningTokens,
		TotalTokens:         counts.TotalTokens,
		SourcePath:          sourcePath,
	}
	event.CostUSD = pricing.CalculateCostAt(event.Model, event.Timestamp, counts, CostOptions{})
	return event, true
}

func cursorTokenCounts(tokens map[string]any) TokenCounts {
	counts := TokenCounts{
		InputTokens:         intValue(tokens, "inputTokens", "input_tokens", "input"),
		CachedInputTokens:   intValue(tokens, "cachedInputTokens", "cacheReadTokens", "cached_input_tokens", "cached"),
		CacheCreationTokens: intValue(tokens, "cacheCreationTokens", "cache_creation_input_tokens", "cache_creation"),
		OutputTokens:        intValue(tokens, "outputTokens", "output_tokens", "output"),
		ReasoningTokens:     intValue(tokens, "thinkingTokens", "reasoningTokens", "reasoning_output_tokens"),
		TotalTokens:         intValue(tokens, "totalTokens", "total_tokens", "total"),
	}
	if counts.TotalTokens <= 0 {
		counts.TotalTokens = counts.InputTokens + counts.CachedInputTokens + counts.CacheCreationTokens + counts.OutputTokens + counts.ReasoningTokens
	}
	return counts
}

func cursorComposerIDFromBubbleKey(key string) string {
	parts := strings.Split(key, ":")
	if len(parts) >= 3 {
		return parts[1]
	}
	return ""
}

func cursorMode(obj map[string]any) string {
	if mode := firstString(obj, "forceMode", "mode"); mode != "" {
		return mode
	}
	switch intValue(obj, "unifiedMode") {
	case 1:
		return "chat"
	case 2:
		return "agent"
	default:
		return ""
	}
}

func normalizeCursorModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimSuffix(model, "-thinking")
	model = strings.ReplaceAll(model, "claude-4.6", "claude-4-6")
	return model
}

func parseGeminiJSONL(data []byte, path, source, provider string, pricing *PricingMap) ([]UsageEvent, error) {
	sessionID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	currentModel := ""
	var events []UsageEvent
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			return nil, err
		}
		if s := firstString(obj, "session_id", "sessionId"); s != "" {
			sessionID = s
		}
		if model := firstString(obj, "model", "model_name"); model != "" {
			currentModel = model
		}
		tokens := object(obj["tokens"])
		if len(tokens) == 0 {
			stats := object(obj["stats"])
			tokens = object(stats["tokens"])
		}
		if len(tokens) == 0 {
			continue
		}
		event := geminiEventFromTokens(tokens, obj, path, source, provider, pricing)
		event.SessionID = sessionID
		if event.Model == "" {
			event.Model = currentModel
		}
		event.CostUSD = pricing.CalculateCostAt(event.Model, event.Timestamp, geminiCounts(tokens), CostOptions{
			ReasoningBilledAsOutput: true,
			ProviderPrefixes:        []string{"google", "gemini", "vertex_ai", "openrouter/google"},
		})
		if event.TotalTokens > 0 {
			events = append(events, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func geminiEventFromObject(obj map[string]any, path, source, provider string, pricing *PricingMap) UsageEvent {
	stats := object(obj["stats"])
	tokens := object(stats["tokens"])
	if len(tokens) == 0 {
		tokens = object(obj["tokens"])
	}
	event := geminiEventFromTokens(tokens, obj, path, source, provider, pricing)
	event.SessionID = firstString(obj, "sessionId", "session_id")
	if event.SessionID == "" {
		event.SessionID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return event
}

func geminiEventFromTokens(tokens, obj map[string]any, path, source, provider string, pricing *PricingMap) UsageEvent {
	counts := geminiCounts(tokens)
	event := UsageEvent{
		Timestamp:           timeValue(obj, "timestamp", "ts", "startTime", "lastUpdated"),
		Source:              source,
		Provider:            provider,
		RequestID:           firstString(obj, "request_id", "id"),
		Model:               firstString(obj, "model", "model_name"),
		InputTokens:         counts.InputTokens,
		CachedInputTokens:   counts.CachedInputTokens,
		CacheCreationTokens: counts.CacheCreationTokens,
		OutputTokens:        counts.OutputTokens,
		ReasoningTokens:     counts.ReasoningTokens,
		TotalTokens:         counts.TotalTokens,
		SourcePath:          path,
	}
	event.CostUSD = pricing.CalculateCostAt(event.Model, event.Timestamp, counts, CostOptions{
		ReasoningBilledAsOutput: true,
		ProviderPrefixes:        []string{"google", "gemini", "vertex_ai", "openrouter/google"},
	})
	return event
}

func codexCounts(usage map[string]any) TokenCounts {
	inputRaw := intValue(usage, "input_tokens", "input")
	cached := intValue(usage, "cached_input_tokens", "cache_read_input_tokens", "cached")
	input := inputRaw - cached
	if input < 0 {
		input = 0
	}
	counts := TokenCounts{
		InputTokens:         input,
		CachedInputTokens:   cached,
		CacheCreationTokens: intValue(usage, "cache_creation_input_tokens", "cache_creation_tokens"),
		OutputTokens:        intValue(usage, "output_tokens", "output"),
		ReasoningTokens:     intValue(usage, "reasoning_output_tokens", "reasoning_tokens"),
		TotalTokens:         intValue(usage, "total_tokens", "total"),
	}
	if counts.TotalTokens <= 0 {
		counts.TotalTokens = inputRaw + counts.CacheCreationTokens + counts.OutputTokens
	}
	return counts
}

func geminiCounts(tokens map[string]any) TokenCounts {
	inputRaw := intValue(tokens, "input", "input_tokens", "prompt", "prompt_tokens")
	cached := intValue(tokens, "cached", "cached_tokens", "cached_input_tokens")
	input := inputRaw - cached
	if input < 0 {
		input = 0
	}
	counts := TokenCounts{
		InputTokens:         input,
		CachedInputTokens:   cached,
		CacheCreationTokens: intValue(tokens, "cache_creation", "cache_creation_tokens", "cache_creation_input_tokens"),
		OutputTokens:        intValue(tokens, "output", "output_tokens", "candidates", "candidates_tokens"),
		ReasoningTokens:     intValue(tokens, "thoughts", "thoughts_tokens", "reasoning", "reasoning_tokens", "reasoning_output_tokens"),
		TotalTokens:         intValue(tokens, "total", "total_tokens"),
	}
	if counts.TotalTokens <= 0 {
		counts.TotalTokens = inputRaw + counts.CacheCreationTokens + counts.OutputTokens + counts.ReasoningTokens
	}
	return counts
}

func object(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func codexReasoningEffort(payload map[string]any) string {
	if effort := firstString(payload, "effort", "reasoning_effort", "model_reasoning_effort"); effort != "" {
		return effort
	}
	collaborationMode := object(payload["collaboration_mode"])
	if effort := firstString(collaborationMode, "effort", "reasoning_effort"); effort != "" {
		return effort
	}
	settings := object(collaborationMode["settings"])
	if effort := firstString(settings, "effort", "reasoning_effort", "model_reasoning_effort"); effort != "" {
		return effort
	}
	return ""
}

func codexMode(payload map[string]any) string {
	if mode := firstString(payload, "mode", "speed_mode", "model_mode"); mode != "" {
		return mode
	}
	collaborationMode := object(payload["collaboration_mode"])
	if mode := firstString(collaborationMode, "mode"); mode != "" {
		return mode
	}
	return ""
}

func boolValue(m map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		switch v := m[key].(type) {
		case bool:
			return v, true
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true", "1", "yes", "fast", "enabled", "on":
				return true, true
			case "false", "0", "no", "standard", "disabled", "off":
				return false, true
			}
		case float64:
			return v != 0, true
		case int:
			return v != 0, true
		case json.Number:
			i, err := v.Int64()
			if err == nil {
				return i != 0, true
			}
		}
	}
	return false, false
}

func intValue(m map[string]any, keys ...string) int {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case json.Number:
			i, _ := v.Int64()
			return int(i)
		}
	}
	return 0
}

func floatValue(m map[string]any, keys ...string) float64 {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			return v
		case int:
			return float64(v)
		case json.Number:
			f, _ := v.Float64()
			return f
		}
	}
	return 0
}

func timeValue(m map[string]any, keys ...string) time.Time {
	for _, key := range keys {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
				if t, err := time.Parse(layout, s); err == nil {
					return t.UTC()
				}
			}
		}
	}
	return time.Now().UTC()
}

func normalizeHeader(header string) string {
	replacer := strings.NewReplacer(" ", "", "_", "", "-", "", ".", "", "/", "")
	return strings.ToLower(replacer.Replace(strings.TrimSpace(header)))
}

func csvString(record []string, headers map[string]int, keys ...string) string {
	for _, key := range keys {
		if i, ok := headers[normalizeHeader(key)]; ok && i < len(record) {
			return strings.TrimSpace(record[i])
		}
	}
	return ""
}

func csvInt(record []string, headers map[string]int, keys ...string) int {
	s := csvString(record, headers, keys...)
	if s == "" {
		return 0
	}
	s = strings.ReplaceAll(s, ",", "")
	i, _ := strconv.Atoi(s)
	return i
}

func csvFloat(record []string, headers map[string]int, keys ...string) float64 {
	s := csvString(record, headers, keys...)
	if s == "" {
		return 0
	}
	s = strings.TrimPrefix(strings.ReplaceAll(s, ",", ""), "$")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func csvTime(record []string, headers map[string]int, keys ...string) time.Time {
	s := csvString(record, headers, keys...)
	if s == "" {
		return time.Now().UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

func normalizeAntigravityModel(model string) string {
	model = strings.TrimPrefix(strings.TrimSpace(model), "custom:")
	var b strings.Builder
	depth := 0
	previousDash := false
	for _, ch := range strings.ToLower(model) {
		switch ch {
		case '[':
			depth++
			continue
		case ']':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth > 0 {
			continue
		}
		next := ch
		if ch == '.' || ch == '-' || ch == ' ' || ch == '\t' {
			next = '-'
		}
		if next == '-' {
			if !previousDash {
				b.WriteRune(next)
			}
			previousDash = true
			continue
		}
		b.WriteRune(next)
		previousDash = false
	}
	return strings.Trim(b.String(), "-")
}

func providerFromAntigravityLock(provider string) string {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(provider), "-", "_")) {
	case "claude", "anthropic":
		return "anthropic"
	case "openai":
		return "openai"
	case "google", "google_ai", "gemini", "vertex", "vertex_ai":
		return "gemini"
	default:
		return "gemini"
	}
}

func defaultAntigravityModel(provider string) string {
	switch provider {
	case "anthropic":
		return "claude-unknown"
	case "openai":
		return "gpt-unknown"
	case "gemini":
		return "gemini-unknown"
	default:
		return "unknown"
	}
}
