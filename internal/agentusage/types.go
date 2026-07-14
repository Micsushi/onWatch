package agentusage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TokenCounts stores provider token categories before they are collapsed into
// the existing API integrations prompt/completion buckets.
type TokenCounts struct {
	InputTokens           int
	CachedInputTokens     int
	CacheCreationTokens   int
	CacheCreation1hTokens int
	OutputTokens          int
	ReasoningTokens       int
	TotalTokens           int
}

type CostOptions struct {
	ReasoningBilledAsOutput bool
	ProviderPrefixes        []string
	CostMultiplier          float64
}

type UsageEvent struct {
	Timestamp             time.Time
	Source                string
	Provider              string
	Account               string
	SessionID             string
	RequestID             string
	Model                 string
	ReasoningEffort       string
	Mode                  string
	FastMode              *bool
	SpeedMode             string
	SpeedMultiplier       float64
	SpeedSource           string
	InputTokens           int
	CachedInputTokens     int
	CacheCreationTokens   int
	CacheCreation1hTokens int
	OutputTokens          int
	ReasoningTokens       int
	TotalTokens           int
	CostUSD               float64
	SourcePath            string
	// UsageSignature is a content-only identity for codex token_count events
	// (last + cumulative usage). Forked/resumed sessions replay their parent's
	// history into a new rollout file with fresh timestamps, so dedupe keys for
	// these events must not include path, session, or timestamp.
	UsageSignature string
}

func (e UsageEvent) ToAPIIntegrationLine() ([]byte, error) {
	ts := e.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	total := e.TotalTokens
	if total <= 0 {
		total = e.InputTokens + e.CachedInputTokens + e.CacheCreationTokens + e.OutputTokens + e.ReasoningTokens
	}
	account := strings.TrimSpace(e.Account)
	if account == "" {
		account = "default"
	}
	cost := (*float64)(nil)
	if e.CostUSD > 0 {
		cost = &e.CostUSD
	}

	metadata := map[string]any{
		"source":                      e.Source,
		"session_id":                  e.SessionID,
		"input_tokens":                e.InputTokens,
		"cached_input_tokens":         e.CachedInputTokens,
		"cache_creation_input_tokens": e.CacheCreationTokens,
		"output_tokens":               e.OutputTokens,
		"reasoning_output_tokens":     e.ReasoningTokens,
		"source_path":                 e.SourcePath,
		"event_key":                   e.dedupeKey(ts, total),
	}
	if e.CacheCreation1hTokens > 0 {
		metadata["cache_creation_1h_input_tokens"] = e.CacheCreation1hTokens
	}
	if strings.TrimSpace(e.ReasoningEffort) != "" {
		metadata["reasoning_effort"] = strings.TrimSpace(e.ReasoningEffort)
	}
	if strings.TrimSpace(e.Mode) != "" {
		metadata["mode"] = strings.TrimSpace(e.Mode)
	}
	if e.FastMode != nil {
		metadata["fast_mode"] = *e.FastMode
		if *e.FastMode {
			metadata["speed_mode"] = "fast"
		} else {
			metadata["speed_mode"] = "standard"
		}
	}
	if strings.TrimSpace(e.SpeedMode) != "" {
		metadata["speed_mode"] = strings.TrimSpace(e.SpeedMode)
	}
	if e.SpeedMultiplier > 0 {
		metadata["speed_multiplier"] = e.SpeedMultiplier
	}
	if strings.TrimSpace(e.SpeedSource) != "" {
		metadata["speed_source"] = strings.TrimSpace(e.SpeedSource)
	}

	wire := struct {
		TS               string         `json:"ts"`
		Integration      string         `json:"integration"`
		Provider         string         `json:"provider"`
		Account          string         `json:"account"`
		Model            string         `json:"model"`
		RequestID        string         `json:"request_id,omitempty"`
		PromptTokens     int            `json:"prompt_tokens"`
		CompletionTokens int            `json:"completion_tokens"`
		TotalTokens      int            `json:"total_tokens"`
		CostUSD          *float64       `json:"cost_usd,omitempty"`
		Metadata         map[string]any `json:"metadata"`
	}{
		TS:               ts.UTC().Format(time.RFC3339Nano),
		Integration:      integrationName(e.Source),
		Provider:         e.Provider,
		Account:          account,
		Model:            e.Model,
		RequestID:        e.RequestID,
		PromptTokens:     e.InputTokens + e.CachedInputTokens + e.CacheCreationTokens,
		CompletionTokens: e.OutputTokens,
		TotalTokens:      total,
		CostUSD:          cost,
		Metadata:         metadata,
	}

	if strings.TrimSpace(wire.Provider) == "" {
		return nil, fmt.Errorf("provider is required")
	}
	if strings.TrimSpace(wire.Model) == "" {
		return nil, fmt.Errorf("model is required")
	}

	line, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	line = append(line, '\n')
	return line, nil
}

func (e UsageEvent) stableEventKey(ts time.Time, total int) string {
	h := sha256.New()
	writeStableEventPart(h, e.SourcePath)
	writeStableEventPart(h, ts.UTC().Format(time.RFC3339Nano))
	writeStableEventPart(h, e.Source)
	writeStableEventPart(h, e.Provider)
	writeStableEventPart(h, e.Account)
	writeStableEventPart(h, e.SessionID)
	writeStableEventPart(h, e.RequestID)
	writeStableEventPart(h, e.Model)
	writeStableEventPart(h, e.ReasoningEffort)
	writeStableEventPart(h, e.Mode)
	writeStableEventPart(h, fmt.Sprintf("%d", e.InputTokens))
	writeStableEventPart(h, fmt.Sprintf("%d", e.CachedInputTokens))
	writeStableEventPart(h, fmt.Sprintf("%d", e.CacheCreationTokens))
	writeStableEventPart(h, fmt.Sprintf("%d", e.OutputTokens))
	writeStableEventPart(h, fmt.Sprintf("%d", e.ReasoningTokens))
	writeStableEventPart(h, fmt.Sprintf("%d", total))
	return hex.EncodeToString(h.Sum(nil))
}

func (e UsageEvent) dedupeKey(ts time.Time, total int) string {
	if strings.TrimSpace(e.UsageSignature) != "" {
		h := sha256.New()
		writeStableEventPart(h, e.Source)
		writeStableEventPart(h, e.Provider)
		writeStableEventPart(h, e.Account)
		writeStableEventPart(h, e.Model)
		writeStableEventPart(h, e.ReasoningEffort)
		writeStableEventPart(h, e.Mode)
		writeStableEventPart(h, e.UsageSignature)
		return hex.EncodeToString(h.Sum(nil))
	}
	if strings.EqualFold(strings.TrimSpace(e.Source), "claude") && strings.TrimSpace(e.RequestID) != "" {
		account := strings.TrimSpace(e.Account)
		if account == "" {
			account = "default"
		}
		h := sha256.New()
		writeStableEventPart(h, e.Source)
		writeStableEventPart(h, e.Provider)
		writeStableEventPart(h, account)
		writeStableEventPart(h, e.RequestID)
		writeStableEventPart(h, e.Model)
		writeStableEventPart(h, fmt.Sprintf("%d", e.InputTokens))
		writeStableEventPart(h, fmt.Sprintf("%d", e.CachedInputTokens))
		writeStableEventPart(h, fmt.Sprintf("%d", e.CacheCreationTokens))
		writeStableEventPart(h, fmt.Sprintf("%d", e.CacheCreation1hTokens))
		writeStableEventPart(h, fmt.Sprintf("%d", e.OutputTokens))
		writeStableEventPart(h, fmt.Sprintf("%d", e.ReasoningTokens))
		writeStableEventPart(h, fmt.Sprintf("%d", total))
		return hex.EncodeToString(h.Sum(nil))
	}
	return e.stableEventKey(ts, total)
}

func writeStableEventPart(h interface{ Write([]byte) (int, error) }, part string) {
	_, _ = h.Write([]byte(strings.TrimSpace(part)))
	_, _ = h.Write([]byte{0})
}

func integrationName(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex CLI"
	case "cursor":
		return "Cursor"
	case "antigravity":
		return "Antigravity"
	case "gemini":
		return "Gemini CLI"
	default:
		if strings.TrimSpace(source) == "" {
			return "Agent Usage"
		}
		return source
	}
}
