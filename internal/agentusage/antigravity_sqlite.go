package agentusage

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

var antigravityReceiptModel = regexp.MustCompile(`^(gemini|claude|gpt)-[a-z0-9][a-z0-9.-]{0,79}$`)

// Read only accounting fields. Generation and step blobs can also hold private context.
func parseAntigravityConversation(path string, pricing *PricingMap) ([]UsageEvent, error) {
	slashPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	dsn := url.URL{Scheme: "file", Path: slashPath, RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=trusted_schema(0)"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version != 1 {
		return nil, fmt.Errorf("unsupported Antigravity conversation schema version %d", version)
	}
	stepTimes := make(map[string]time.Time)
	steps, err := db.Query("SELECT metadata FROM steps WHERE length(metadata) BETWEEN 1 AND 262144")
	if err != nil {
		return nil, err
	}
	for steps.Next() {
		var metadata []byte
		if err := steps.Scan(&metadata); err != nil {
			steps.Close()
			return nil, err
		}
		id := string(agyBytes(agyBytes(metadata, 9), 11))
		if id != "" && len(id) <= 256 {
			if at := agyTimestamp(agyBytes(metadata, 1)); !at.IsZero() {
				stepTimes[id] = at
			}
		}
	}
	steps.Close()
	if err := steps.Err(); err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT idx, data FROM gen_metadata WHERE length(data) BETWEEN 1 AND 8388608 ORDER BY idx")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	session := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	var events []UsageEvent
	for rows.Next() {
		var idx int64
		var blob []byte
		if err := rows.Scan(&idx, &blob); err != nil {
			return nil, err
		}
		generation := agyBytes(blob, 1)
		usage := agyBytes(generation, 4)
		id := string(agyBytes(usage, 11))
		if len(id) > 256 {
			continue
		}
		at := agyTimestamp(agyBytes(agyBytes(generation, 9), 4))
		if at.IsZero() {
			at = stepTimes[id]
		}
		if at.IsZero() {
			continue
		}
		model, effort := agyModel(string(agyBytes(generation, 21)), string(agyBytes(generation, 19)))
		if model == "" {
			continue
		}
		parts := [6]int{}
		valid := true
		for i, tag := range []protowire.Number{2, 3, 4, 5, 9, 10} {
			value := agyInt(usage, tag)
			if value > 100_000_000 {
				valid = false
				break
			}
			parts[i] = int(value)
		}
		input, output, writes, cached, thinking, visible := parts[0], parts[1], parts[2], parts[3], parts[4], parts[5]
		if !valid || output != thinking+visible || input+output+writes+cached == 0 {
			continue
		}
		counts := TokenCounts{InputTokens: input, CachedInputTokens: cached, CacheCreationTokens: writes, OutputTokens: visible, ReasoningTokens: thinking}
		identity := "antigravity-generation:" + id
		if id == "" {
			identity = "antigravity-row:" + session + ":" + strconv.FormatInt(idx, 10)
		}
		event := UsageEvent{
			Timestamp: at, Source: "antigravity", Provider: "gemini",
			SourcePath: path, SessionID: session, UsageSignature: identity, Model: model, ReasoningEffort: effort,
			InputTokens: input, OutputTokens: visible, ReasoningTokens: thinking,
			CachedInputTokens: cached, CacheCreationTokens: writes, TotalTokens: input + output + writes + cached,
		}
		event.CostUSD = pricing.CalculateCostAt(model, at, counts, GoogleFamilyCostOptions(model, counts))
		events = append(events, event)
	}
	return events, rows.Err()
}

func agyModel(display, served string) (string, string) {
	for _, name := range []string{display, served} {
		model, effort := strings.ToLower(strings.TrimSpace(name)), ""
		model = strings.ReplaceAll(model, " ", "-")
		for _, candidate := range []string{"low", "medium", "high", "xhigh", "max", "thinking"} {
			for _, suffix := range []string{"-(" + candidate + ")", "-" + candidate} {
				if strings.HasSuffix(model, suffix) {
					model, effort = strings.TrimSuffix(model, suffix), candidate
					break
				}
			}
		}
		if strings.HasPrefix(model, "claude-") {
			model = strings.ReplaceAll(model, ".", "-")
		}
		if antigravityReceiptModel.MatchString(model) {
			return model, effort
		}
	}
	return "", ""
}

func agyTimestamp(data []byte) time.Time {
	seconds, nanos := agyInt(data, 1), agyInt(data, 2)
	if seconds < 1700000000 || seconds >= 32503680000 || nanos >= 1e9 {
		return time.Time{}
	}
	return time.Unix(int64(seconds), int64(nanos)).UTC()
}

func agyField(data []byte, wanted protowire.Number, wire protowire.Type) []byte {
	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil
		}
		data = data[n:]
		n = protowire.ConsumeFieldValue(number, typ, data)
		if n < 0 {
			return nil
		}
		if number == wanted && typ == wire {
			return data[:n]
		}
		data = data[n:]
	}
	return nil
}

func agyBytes(data []byte, field protowire.Number) []byte {
	result, _ := protowire.ConsumeBytes(agyField(data, field, protowire.BytesType))
	return result
}

func agyInt(data []byte, field protowire.Number) uint64 {
	result, _ := protowire.ConsumeVarint(agyField(data, field, protowire.VarintType))
	return result
}
