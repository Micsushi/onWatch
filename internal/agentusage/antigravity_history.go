package agentusage

import (
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
	_ "modernc.org/sqlite"
)

const (
	SourceAntigravityHistory = "antigravity_history"

	antigravityHistoryMaxRows       = 10_000
	antigravityHistoryMaxBlobBytes  = 4 * 1024 * 1024
	antigravityHistoryMaxModelBytes = 256
)

// ParseAntigravityHistoryDB reads only the metadata table from an Antigravity
// conversation database. The connection is read-only and both row count and
// protobuf size are capped so a damaged database cannot cause an unbounded
// import.
func ParseAntigravityHistoryDB(path string, pricing *PricingMap) ([]UsageEvent, error) {
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	rows, err := db.Query(`
		SELECT idx, CASE WHEN length(data) <= ? THEN data END, size, length(data)
		FROM gen_metadata
		ORDER BY idx
		LIMIT ?`, antigravityHistoryMaxBlobBytes, antigravityHistoryMaxRows+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessionID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	var events []UsageEvent
	rowCount := 0
	for rows.Next() {
		rowCount++
		if rowCount > antigravityHistoryMaxRows {
			return nil, fmt.Errorf("Antigravity history exceeds %d rows; import withheld", antigravityHistoryMaxRows)
		}
		var (
			idx        sql.NullInt64
			data       []byte
			size       sql.NullInt64
			blobLength sql.NullInt64
		)
		if err := rows.Scan(&idx, &data, &size, &blobLength); err != nil {
			return nil, err
		}
		if blobLength.Valid && blobLength.Int64 > antigravityHistoryMaxBlobBytes {
			return nil, fmt.Errorf("Antigravity history row exceeds blob limit; import withheld")
		}
		if !idx.Valid || len(data) == 0 || len(data) > antigravityHistoryMaxBlobBytes {
			continue
		}
		if size.Valid && (size.Int64 < 0 || size.Int64 > antigravityHistoryMaxBlobBytes) {
			continue
		}
		event, ok := antigravityHistoryEvent(data, path, sessionID, idx.Int64, pricing)
		if ok {
			events = append(events, event)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

type antigravityHistoryUsage struct {
	input, output, cacheWrite, cacheRead, thinking, response             int
	inputOK, outputOK, cacheWriteOK, cacheReadOK, thinkingOK, responseOK bool
}

func antigravityHistoryEvent(data []byte, sourcePath, sessionID string, idx int64, pricing *PricingMap) (UsageEvent, bool) {
	var chat []byte
	if !historyProtoFields(data, func(number protowire.Number, typ protowire.Type, bytes []byte, value uint64) bool {
		if number == 1 && typ == protowire.BytesType {
			chat = bytes
		}
		return true
	}) {
		return UsageEvent{}, false
	}
	if len(chat) == 0 {
		return UsageEvent{}, false
	}

	var usage, start []byte
	var modelFull, model, display string
	if !historyProtoFields(chat, func(number protowire.Number, typ protowire.Type, bytes []byte, value uint64) bool {
		if typ != protowire.BytesType {
			return true
		}
		switch number {
		case 4:
			usage = bytes
		case 9:
			start = bytes
		case 19:
			model = safeHistoryString(bytes)
		case 21:
			display = safeHistoryString(bytes)
		case 22:
			modelFull = safeHistoryString(bytes)
		}
		return true
	}) {
		return UsageEvent{}, false
	}
	if len(usage) == 0 {
		return UsageEvent{}, false
	}

	counts, outputTotal, ok := antigravityHistoryCounts(usage)
	if !ok || outputTotal <= 0 {
		return UsageEvent{}, false
	}
	provider, ok := antigravityHistoryProvider(usage)
	if !ok {
		return UsageEvent{}, false
	}
	when, ok := antigravityHistoryStartTime(start)
	if !ok {
		return UsageEvent{}, false
	}
	model = normalizeAntigravityModel(firstNonEmptyHistory(modelFull, model, display))
	if model == "" || len(model) > antigravityHistoryMaxModelBytes {
		return UsageEvent{}, false
	}

	event := UsageEvent{
		Timestamp:           when,
		Source:              "antigravity",
		Provider:            provider,
		SessionID:           sessionID,
		RequestID:           "gen_metadata:" + strconv.FormatInt(idx, 10),
		Model:               model,
		InputTokens:         counts.InputTokens,
		CachedInputTokens:   counts.CachedInputTokens,
		CacheCreationTokens: counts.CacheCreationTokens,
		OutputTokens:        counts.OutputTokens,
		ReasoningTokens:     counts.ReasoningTokens,
		TotalTokens:         counts.TotalTokens,
		SourcePath:          sourcePath,
	}
	event.CostUSD = pricing.CalculateCostAt(event.Model, event.Timestamp, counts, CostOptions{
		ReasoningBilledAsOutput: true,
		ProviderPrefixes:        []string{"google", "vertex_ai", "openrouter/google", "anthropic", "openai"},
	})
	return event, true
}

func antigravityHistoryProvider(data []byte) (string, bool) {
	var provider uint64
	var found bool
	if !historyProtoFields(data, func(number protowire.Number, typ protowire.Type, bytes []byte, value uint64) bool {
		if number == 6 && typ == protowire.VarintType {
			provider, found = value, true
		}
		return true
	}) || !found {
		return "", false
	}
	switch provider {
	case 3, 24, 30:
		return "gemini", true
	case 26:
		return "anthropic", true
	case 31:
		return "openai", true
	default:
		return "", false
	}
}

func antigravityHistoryCounts(data []byte) (TokenCounts, int, bool) {
	var raw antigravityHistoryUsage
	if !historyProtoFields(data, func(number protowire.Number, typ protowire.Type, bytes []byte, value uint64) bool {
		if typ != protowire.VarintType {
			return true
		}
		if value > uint64(maxInt()) {
			return false
		}
		v := int(value)
		switch number {
		case 2:
			raw.input, raw.inputOK = v, true
		case 3:
			raw.output, raw.outputOK = v, true
		case 4:
			raw.cacheWrite, raw.cacheWriteOK = v, true
		case 5:
			raw.cacheRead, raw.cacheReadOK = v, true
		case 9:
			raw.thinking, raw.thinkingOK = v, true
		case 10:
			raw.response, raw.responseOK = v, true
		}
		return true
	}) {
		return TokenCounts{}, 0, false
	}
	if !raw.inputOK && !raw.outputOK && !raw.cacheWriteOK && !raw.cacheReadOK && !raw.thinkingOK && !raw.responseOK {
		return TokenCounts{}, 0, false
	}

	// Installed Antigravity metadata reports output as response + thinking.
	// Keep the two existing buckets separate so ToAPIIntegrationLine does not
	// count thinking twice.
	response := raw.response
	combinedOutput, combinedOK := historyAdd(raw.response, raw.thinking)
	if raw.outputOK && raw.responseOK && raw.thinkingOK && (!combinedOK || raw.output != combinedOutput) {
		return TokenCounts{}, 0, false
	}
	if !raw.responseOK {
		if raw.outputOK {
			response = raw.output - raw.thinking
			if response < 0 {
				return TokenCounts{}, 0, false
			}
		} else {
			response = 0
		}
	}
	outputTotal := raw.output
	if !raw.outputOK {
		if !combinedOK {
			return TokenCounts{}, 0, false
		}
		outputTotal = combinedOutput
	}
	if outputTotal < 0 {
		return TokenCounts{}, 0, false
	}

	input := raw.input
	if raw.cacheReadOK {
		input -= raw.cacheRead
		if input < 0 {
			input = 0
		}
	}
	total, totalOK := historyAdd(raw.input, raw.cacheWrite, outputTotal)
	if !totalOK {
		return TokenCounts{}, 0, false
	}
	counts := TokenCounts{
		InputTokens:         input,
		CachedInputTokens:   raw.cacheRead,
		CacheCreationTokens: raw.cacheWrite,
		OutputTokens:        response,
		ReasoningTokens:     raw.thinking,
		TotalTokens:         total,
	}
	if counts.TotalTokens <= 0 {
		return TokenCounts{}, 0, false
	}
	return counts, outputTotal, true
}

func historyAdd(values ...int) (int, bool) {
	total := 0
	for _, value := range values {
		if value < 0 || total > maxInt()-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func antigravityHistoryStartTime(data []byte) (time.Time, bool) {
	if len(data) == 0 {
		return time.Time{}, false
	}
	var timestamp []byte
	if !historyProtoFields(data, func(number protowire.Number, typ protowire.Type, bytes []byte, value uint64) bool {
		if number == 4 && typ == protowire.BytesType {
			timestamp = bytes
		}
		return true
	}) || len(timestamp) == 0 {
		return time.Time{}, false
	}
	var seconds uint64
	var nanos uint64
	var secondsOK, nanosOK bool
	if !historyProtoFields(timestamp, func(number protowire.Number, typ protowire.Type, bytes []byte, value uint64) bool {
		if typ != protowire.VarintType {
			return true
		}
		switch number {
		case 1:
			seconds, secondsOK = value, true
		case 2:
			nanos, nanosOK = value, true
		}
		return true
	}) || !secondsOK || seconds > math.MaxInt64 || (nanosOK && nanos >= 1_000_000_000) {
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), int64(nanos)).UTC(), true
}

func historyProtoFields(data []byte, visit func(protowire.Number, protowire.Type, []byte, uint64) bool) bool {
	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return false
		}
		data = data[n:]
		switch typ {
		case protowire.VarintType:
			value, consumed := protowire.ConsumeVarint(data)
			if consumed < 0 || !visit(number, typ, nil, value) {
				return false
			}
			n = consumed
		case protowire.BytesType:
			bytes, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 || !visit(number, typ, bytes, 0) {
				return false
			}
			n = consumed
		default:
			consumed := protowire.ConsumeFieldValue(number, typ, data)
			if consumed < 0 || !visit(number, typ, nil, 0) {
				return false
			}
			n = consumed
		}
		if n > len(data) {
			return false
		}
		data = data[n:]
	}
	return true
}

func safeHistoryString(data []byte) string {
	if len(data) == 0 || !utf8.Valid(data) {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func firstNonEmptyHistory(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
