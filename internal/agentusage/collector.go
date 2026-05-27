package agentusage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	SourceClaude       = "claude"
	SourceCodex        = "codex"
	SourceCursorCSV    = "cursor_csv"
	SourceCursorSQLite = "cursor_sqlite"
	SourceGemini       = "gemini"
	SourceAntigravity  = "antigravity"

	initialScanWindow = 6 * time.Hour
)

type Source struct {
	Kind            string
	Path            string
	Source          string
	Provider        string
	InitialBackfill bool
}

type Collector struct {
	outDir      string
	pricing     *PricingMap
	sources     []Source
	seen        map[string]struct{}
	fileStates  map[string]fileState
	seenLoaded  bool
	initialized bool
	logger      *slog.Logger
}

type fileState struct {
	Size    int64
	ModTime int64
}

func NewCollector(outDir string, pricing *PricingMap, sources []Source, logger *slog.Logger) *Collector {
	if logger == nil {
		logger = slog.Default()
	}
	return &Collector{
		outDir:     outDir,
		pricing:    pricing,
		sources:    sources,
		seen:       make(map[string]struct{}),
		fileStates: make(map[string]fileState),
		logger:     logger,
	}
}

func (c *Collector) CollectOnce() error {
	if err := os.MkdirAll(c.outDir, 0o700); err != nil {
		return err
	}
	outPath := c.outputPath(time.Now())
	if !c.seenLoaded {
		seenPath := c.seenPath()
		_, seenStatErr := os.Stat(seenPath)
		if err := c.loadSeenKeys(seenPath); err != nil {
			c.logger.Warn("agent usage collector could not load persisted fingerprints", "path", seenPath, "error", err)
		}
		if err := c.loadSeenFromOutput(outPath); err != nil {
			c.logger.Warn("agent usage collector could not load existing output fingerprints", "path", outPath, "error", err)
		}
		if os.IsNotExist(seenStatErr) && len(c.seen) > 0 {
			if err := appendSeenKeys(seenPath, mapKeys(c.seen)); err != nil {
				c.logger.Warn("agent usage collector could not seed persisted fingerprints", "path", seenPath, "error", err)
			}
		}
		c.seenLoaded = true
	}
	var wrote bool
	for _, source := range c.sources {
		events, err := c.collectSource(source)
		if err != nil {
			c.logger.Warn("agent usage collector skipped source", "path", source.Path, "kind", source.Kind, "error", err)
			continue
		}
		var lines [][]byte
		var newKeys []string
		for _, event := range events {
			line, err := event.ToAPIIntegrationLine()
			if err != nil {
				c.logger.Warn("agent usage collector skipped event", "path", source.Path, "error", err)
				continue
			}
			key := eventKey(event, line)
			if _, ok := c.seen[key]; ok {
				continue
			}
			c.seen[key] = struct{}{}
			lines = append(lines, line)
			newKeys = append(newKeys, key)
		}
		if len(lines) > 0 {
			if err := appendLines(outPath, lines); err != nil {
				return err
			}
			if err := appendSeenKeys(c.seenPath(), newKeys); err != nil {
				return err
			}
			wrote = true
		}
	}
	if !wrote {
		c.initialized = true
		return nil
	}
	c.initialized = true
	return nil
}

func (c *Collector) outputPath(now time.Time) string {
	return filepath.Join(c.outDir, fmt.Sprintf("agent-usage-%s.jsonl", now.UTC().Format("2006-01-02")))
}

func (c *Collector) seenPath() string {
	return filepath.Join(c.outDir, ".agent-usage-seen")
}

func (c *Collector) loadSeenKeys(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, raw := range strings.Split(string(data), "\n") {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		c.seen[key] = struct{}{}
	}
	return nil
}

func (c *Collector) loadSeenFromOutput(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lineBytes := append([]byte(line), '\n')
		event, ok := usageEventFromOutputLine(lineBytes)
		if !ok {
			continue
		}
		c.seen[eventKey(event, lineBytes)] = struct{}{}
	}
	return nil
}

func usageEventFromOutputLine(line []byte) (UsageEvent, bool) {
	var wire struct {
		TS               string         `json:"ts"`
		Integration      string         `json:"integration"`
		Provider         string         `json:"provider"`
		Account          string         `json:"account"`
		Model            string         `json:"model"`
		RequestID        string         `json:"request_id"`
		PromptTokens     int            `json:"prompt_tokens"`
		CompletionTokens int            `json:"completion_tokens"`
		TotalTokens      int            `json:"total_tokens"`
		CostUSD          float64        `json:"cost_usd"`
		Metadata         map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(line, &wire); err != nil {
		return UsageEvent{}, false
	}
	event := UsageEvent{
		Timestamp:         parseTimeRFC3339(wire.TS),
		Provider:          wire.Provider,
		Account:           wire.Account,
		RequestID:         wire.RequestID,
		Model:             wire.Model,
		TotalTokens:       wire.TotalTokens,
		CostUSD:           wire.CostUSD,
		InputTokens:       wire.PromptTokens,
		OutputTokens:      wire.CompletionTokens,
		ReasoningEffort:   stringFromMap(wire.Metadata, "reasoning_effort"),
		Mode:              stringFromMap(wire.Metadata, "mode"),
		Source:            stringFromMap(wire.Metadata, "source"),
		SourcePath:        stringFromMap(wire.Metadata, "source_path"),
		CachedInputTokens: intFromMap(wire.Metadata, "cached_input_tokens"),
	}
	event.InputTokens = intFromMapDefault(wire.Metadata, "input_tokens", event.InputTokens)
	event.CacheCreationTokens = intFromMap(wire.Metadata, "cache_creation_input_tokens")
	event.OutputTokens = intFromMapDefault(wire.Metadata, "output_tokens", event.OutputTokens)
	event.ReasoningTokens = intFromMap(wire.Metadata, "reasoning_output_tokens")
	if event.Source == "" {
		event.Source = wire.Integration
	}
	return event, !event.Timestamp.IsZero() && event.Model != ""
}

func parseTimeRFC3339(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err == nil {
		return parsed
	}
	return time.Time{}
}

func stringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if value, ok := m[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func intFromMap(m map[string]any, key string) int {
	return intFromMapDefault(m, key, 0)
}

func intFromMapDefault(m map[string]any, key string, fallback int) int {
	if m == nil {
		return fallback
	}
	switch value := m[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return fallback
}

func appendLines(path string, lines [][]byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, line := range lines {
		if _, err := file.Write(line); err != nil {
			return err
		}
	}
	return nil
}

func appendSeenKeys(path string, keys []string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, err := file.WriteString(key + "\n"); err != nil {
			return err
		}
	}
	return nil
}

func mapKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (c *Collector) collectSource(source Source) ([]UsageEvent, error) {
	paths, err := expandSourcePaths(source.Path, source.Kind)
	if err != nil {
		return nil, err
	}
	var events []UsageEvent
	for _, path := range paths {
		changed := c.sourceFileChanged(path)
		if !c.initialized && !source.InitialBackfill && !sourceFileInInitialWindow(path) {
			continue
		}
		if c.initialized && !changed {
			continue
		}
		switch source.Kind {
		case SourceClaude:
			fileEvents, err := parseClaudeFile(path, c.pricing)
			if err != nil {
				return nil, err
			}
			events = append(events, fileEvents...)
		case SourceCodex:
			fileEvents, err := ParseCodexUsageFile(path, c.pricing)
			if err != nil {
				return nil, err
			}
			events = append(events, fileEvents...)
		case SourceCursorCSV:
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			fileEvents, err := ParseCursorUsageCSV(data, path)
			if err != nil {
				return nil, err
			}
			events = append(events, fileEvents...)
		case SourceCursorSQLite:
			fileEvents, err := ParseCursorStateDB(path, c.pricing)
			if err != nil {
				return nil, err
			}
			events = append(events, fileEvents...)
		case SourceGemini:
			displaySource := source.Source
			if displaySource == "" {
				displaySource = strings.TrimSuffix(source.Kind, "_csv")
			}
			provider := source.Provider
			if provider == "" {
				provider = "gemini"
			}
			fileEvents, err := ParseGeminiUsageFile(path, displaySource, provider, c.pricing)
			if err != nil {
				return nil, err
			}
			events = append(events, fileEvents...)
		case SourceAntigravity:
			event, err := ParseAntigravitySettingsFile(path, c.pricing)
			if err != nil {
				return nil, err
			}
			if event != nil {
				events = append(events, *event)
			}
		default:
			return nil, fmt.Errorf("unsupported source kind %q", source.Kind)
		}
	}
	return events, nil
}

func sourceFileInInitialWindow(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return info.ModTime().After(time.Now().Add(-initialScanWindow))
}

func (c *Collector) sourceFileChanged(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	state := fileState{Size: info.Size(), ModTime: info.ModTime().UnixNano()}
	previous, ok := c.fileStates[path]
	c.fileStates[path] = state
	if !ok {
		return true
	}
	return previous.Size != state.Size || previous.ModTime != state.ModTime
}

func parseClaudeFile(path string, pricing *PricingMap) ([]UsageEvent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var events []UsageEvent
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		event, err := ParseClaudeUsageLine([]byte(line), path, pricing)
		if err != nil {
			return nil, err
		}
		events = append(events, *event)
	}
	return events, nil
}

func expandSourcePaths(path, kind string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	var paths []string
	err = filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipSourceDir(candidate) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(candidate))
		switch kind {
		case SourceCursorCSV:
			if ext == ".csv" {
				paths = append(paths, candidate)
			}
		case SourceCursorSQLite:
			if strings.EqualFold(filepath.Base(candidate), "state.vscdb") {
				paths = append(paths, candidate)
			}
		case SourceAntigravity:
			if ext == ".json" {
				paths = append(paths, candidate)
			}
		default:
			if ext == ".jsonl" || ext == ".json" {
				paths = append(paths, candidate)
			}
		}
		return nil
	})
	sortSourcePathsByRecent(paths)
	return paths, err
}

func sortSourcePathsByRecent(paths []string) {
	sort.SliceStable(paths, func(i, j int) bool {
		left, leftErr := os.Stat(paths[i])
		right, rightErr := os.Stat(paths[j])
		if leftErr == nil && rightErr == nil && !left.ModTime().Equal(right.ModTime()) {
			return left.ModTime().After(right.ModTime())
		}
		return paths[i] < paths[j]
	})
}

func shouldSkipSourceDir(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	switch name {
	case ".git", "node_modules", "vendor", "cache", ".cache":
		return true
	default:
		return false
	}
}

func eventKey(event UsageEvent, line []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(event.SourcePath))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(event.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00")))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(event.Source))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(event.Model))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(event.RequestID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(line)
	return hex.EncodeToString(h.Sum(nil))
}

func DefaultSources(home string) []Source {
	if home == "" {
		if detected, err := os.UserHomeDir(); err == nil {
			home = detected
		}
	}
	if home == "" {
		return nil
	}
	var sources []Source
	addDir := func(kind, path, displaySource, provider string, initialBackfill bool) {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			sources = append(sources, Source{Kind: kind, Path: path, Source: displaySource, Provider: provider, InitialBackfill: initialBackfill})
		}
	}
	addDir(SourceCodex, filepath.Join(home, ".codex", "sessions"), "codex", "openai", false)
	addDir(SourceCodex, filepath.Join(home, ".codex", "archived_sessions"), "codex", "openai", true)
	addDir(SourceClaude, filepath.Join(home, ".claude", "projects"), "claude", "anthropic", false)
	if geminiDataDir := strings.TrimSpace(os.Getenv("GEMINI_DATA_DIR")); geminiDataDir != "" {
		for _, rawPath := range strings.Split(geminiDataDir, ",") {
			addDir(SourceGemini, strings.TrimSpace(rawPath), "gemini", "gemini", false)
		}
	} else {
		addDir(SourceGemini, filepath.Join(home, ".gemini", "tmp"), "gemini", "gemini", false)
	}
	if droidSessionsDir := strings.TrimSpace(os.Getenv("DROID_SESSIONS_DIR")); droidSessionsDir != "" {
		for _, rawPath := range strings.Split(droidSessionsDir, ",") {
			addDir(SourceAntigravity, strings.TrimSpace(rawPath), "antigravity", "gemini", false)
		}
	} else {
		addDir(SourceAntigravity, filepath.Join(home, ".gemini", "antigravity"), "antigravity", "gemini", true)
		addDir(SourceAntigravity, filepath.Join(home, ".factory", "sessions"), "antigravity", "gemini", true)
	}
	if csvPath := strings.TrimSpace(os.Getenv("ONWATCH_CURSOR_USAGE_CSV")); csvPath != "" {
		if _, err := os.Stat(csvPath); err == nil {
			sources = append(sources, Source{Kind: SourceCursorCSV, Path: csvPath, Source: "cursor", Provider: "cursor"})
		}
	}
	if stateDBPath := strings.TrimSpace(os.Getenv("ONWATCH_CURSOR_STATE_DB")); stateDBPath != "" {
		if _, err := os.Stat(stateDBPath); err == nil {
			sources = append(sources, Source{Kind: SourceCursorSQLite, Path: stateDBPath, Source: "cursor", Provider: "cursor", InitialBackfill: true})
		}
	} else if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
		stateDBPath := filepath.Join(appData, "Cursor", "User", "globalStorage", "state.vscdb")
		if _, err := os.Stat(stateDBPath); err == nil {
			sources = append(sources, Source{Kind: SourceCursorSQLite, Path: stateDBPath, Source: "cursor", Provider: "cursor", InitialBackfill: true})
		}
	}
	return sources
}
