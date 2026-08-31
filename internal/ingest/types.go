package ingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

const (
	SchemaVersion         = 1
	MaxBatchEvents        = 500
	MaxRequestBytes       = 2 << 20
	MaxEventBytes         = 64 << 10
	MaxFutureClockSkew    = 10 * time.Minute
	MaxLiveObservationAge = 90 * 24 * time.Hour
)

type Account struct {
	ExternalID string `json:"external_id"`
	Label      string `json:"label,omitempty"`
}

type Event struct {
	EventID    string          `json:"event_id"`
	Kind       string          `json:"kind"`
	CapturedAt time.Time       `json:"captured_at"`
	Provider   string          `json:"provider"`
	Account    Account         `json:"account"`
	Payload    json.RawMessage `json:"payload"`
}

type Batch struct {
	SchemaVersion    int       `json:"schema_version"`
	DeviceID         string    `json:"device_id"`
	CollectorVersion string    `json:"collector_version,omitempty"`
	ConfigRevision   int64     `json:"config_revision,omitempty"`
	SentAt           time.Time `json:"sent_at"`
	Events           []Event   `json:"events"`
}

type EventResult struct {
	EventID string `json:"event_id"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
}

type DesiredConfig struct {
	Revision           int64                `json:"revision"`
	Assignments        []ProviderAssignment `json:"assignments,omitempty"`
	Sources            []string             `json:"sources,omitempty"`
	CollectionInterval string               `json:"collection_interval,omitempty"`
	UploadInterval     string               `json:"upload_interval,omitempty"`
}

type ProviderAssignment struct {
	Provider        string `json:"provider"`
	ExternalID      string `json:"external_id"`
	CredentialAlias string `json:"credential_alias,omitempty"`
	PollInterval    string `json:"poll_interval"`
}

type BatchResponse struct {
	ServerTime     time.Time     `json:"server_time"`
	ConfigRevision int64         `json:"config_revision"`
	DesiredConfig  DesiredConfig `json:"desired_config"`
	Results        []EventResult `json:"results"`
}

type Heartbeat struct {
	SchemaVersion    int        `json:"schema_version"`
	DeviceID         string     `json:"device_id"`
	CollectorVersion string     `json:"collector_version"`
	Platform         string     `json:"platform"`
	ConfigRevision   int64      `json:"config_revision"`
	QueueBytes       int64      `json:"queue_bytes"`
	PendingEvents    int        `json:"pending_events"`
	OldestQueuedAt   *time.Time `json:"oldest_queued_at,omitempty"`
	LastUploadAt     *time.Time `json:"last_upload_at,omitempty"`
	Capabilities     []string   `json:"capabilities,omitempty"`
}

type HeartbeatResponse struct {
	ServerTime     time.Time     `json:"server_time"`
	ConfigRevision int64         `json:"config_revision"`
	DesiredConfig  DesiredConfig `json:"desired_config,omitempty"`
}

type QuotaMetric struct {
	Name     string     `json:"name"`
	Value    float64    `json:"value"`
	Limit    *float64   `json:"limit,omitempty"`
	Unit     string     `json:"unit"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Status   string     `json:"status,omitempty"`
}

type QuotaSnapshot struct {
	Version int           `json:"version"`
	Metrics []QuotaMetric `json:"metrics"`
}

func DecodeBatch(data []byte, now time.Time) (*Batch, error) {
	if len(data) == 0 || len(data) > MaxRequestBytes {
		return nil, fmt.Errorf("request_size")
	}
	var batch Batch
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&batch); err != nil {
		return nil, fmt.Errorf("invalid_json: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid_json")
	}
	if batch.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported_schema")
	}
	if batch.SentAt.IsZero() || batch.SentAt.After(now.Add(MaxFutureClockSkew)) || len(batch.CollectorVersion) > 128 {
		return nil, fmt.Errorf("invalid_batch_metadata")
	}
	if err := ValidateDeviceID(batch.DeviceID); err != nil {
		return nil, err
	}
	if len(batch.Events) == 0 || len(batch.Events) > MaxBatchEvents {
		return nil, fmt.Errorf("batch_size")
	}
	seen := make(map[string]struct{}, len(batch.Events))
	for i := range batch.Events {
		if _, ok := seen[batch.Events[i].EventID]; ok {
			return nil, fmt.Errorf("duplicate_event_id")
		}
		seen[batch.Events[i].EventID] = struct{}{}
	}
	return &batch, nil
}

func (event Event) Validate(now time.Time) error {
	encoded, err := json.Marshal(event)
	if err != nil || len(encoded) > MaxEventBytes {
		return fmt.Errorf("event_size")
	}
	if len(event.EventID) < 5 || len(event.EventID) > 160 || !strings.HasPrefix(event.EventID, "evt_") {
		return fmt.Errorf("invalid_event_id")
	}
	if event.Kind != "usage_event" && event.Kind != "quota_snapshot" {
		return fmt.Errorf("unknown_event_kind")
	}
	if event.CapturedAt.IsZero() || event.CapturedAt.After(now.Add(MaxFutureClockSkew)) {
		return fmt.Errorf("future_timestamp")
	}
	if event.CapturedAt.Before(now.Add(-MaxLiveObservationAge)) {
		return fmt.Errorf("observation_too_old")
	}
	if strings.TrimSpace(event.Provider) == "" || len(event.Provider) > 64 {
		return fmt.Errorf("invalid_provider")
	}
	if strings.TrimSpace(event.Account.ExternalID) == "" || len(event.Account.ExternalID) > 256 || len(event.Account.Label) > 256 {
		return fmt.Errorf("invalid_account")
	}
	if len(event.Payload) == 0 || len(event.Payload) > MaxEventBytes {
		return fmt.Errorf("event_size")
	}
	if containsForbiddenKey(event.Payload) {
		return fmt.Errorf("forbidden_payload_field")
	}
	if event.Kind == "quota_snapshot" {
		var snapshot QuotaSnapshot
		decoder := json.NewDecoder(bytes.NewReader(event.Payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&snapshot); err != nil || snapshot.Version != 1 || len(snapshot.Metrics) == 0 || len(snapshot.Metrics) > 128 {
			return fmt.Errorf("invalid_quota_payload")
		}
		for _, metric := range snapshot.Metrics {
			if strings.TrimSpace(metric.Name) == "" || len(metric.Name) > 128 || strings.TrimSpace(metric.Unit) == "" || len(metric.Unit) > 32 ||
				math.IsNaN(metric.Value) || math.IsInf(metric.Value, 0) || (metric.Limit != nil && (math.IsNaN(*metric.Limit) || math.IsInf(*metric.Limit, 0))) || len(metric.Status) > 64 {
				return fmt.Errorf("invalid_quota_payload")
			}
		}
	}
	return nil
}

func ValidateDesiredConfig(config DesiredConfig) error {
	if config.Revision < 0 || len(config.Assignments) > 64 || len(config.Sources) > 32 {
		return fmt.Errorf("invalid_desired_config")
	}
	if config.CollectionInterval != "" {
		interval, err := time.ParseDuration(config.CollectionInterval)
		if err != nil || interval < time.Second {
			return fmt.Errorf("invalid_collection_interval")
		}
	}
	if config.UploadInterval != "" {
		interval, err := time.ParseDuration(config.UploadInterval)
		if err != nil || interval < time.Second {
			return fmt.Errorf("invalid_upload_interval")
		}
	}
	seen := make(map[string]struct{}, len(config.Assignments))
	for _, assignment := range config.Assignments {
		provider := strings.ToLower(strings.TrimSpace(assignment.Provider))
		account := strings.TrimSpace(assignment.ExternalID)
		if provider == "" || len(provider) > 64 || account == "" || len(account) > 256 || len(assignment.CredentialAlias) > 128 {
			return fmt.Errorf("invalid_assignment")
		}
		for _, value := range []string{provider, account, assignment.CredentialAlias} {
			if strings.ContainsAny(value, "\x00\r\n/\\") {
				return fmt.Errorf("invalid_assignment")
			}
		}
		interval, err := time.ParseDuration(assignment.PollInterval)
		if err != nil || interval < 10*time.Second {
			return fmt.Errorf("invalid_poll_interval")
		}
		key := provider + "\x00" + account
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate_assignment")
		}
		seen[key] = struct{}{}
	}
	for _, source := range config.Sources {
		source = strings.TrimSpace(source)
		if source == "" || len(source) > 64 || strings.ContainsAny(source, "\x00\r\n/\\") {
			return fmt.Errorf("invalid_source")
		}
	}
	return nil
}

func ValidateDeviceID(deviceID string) error {
	if len(deviceID) != 36 || !strings.HasPrefix(deviceID, "dev_") {
		return fmt.Errorf("invalid_device_id")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(deviceID, "dev_")); err != nil {
		return fmt.Errorf("invalid_device_id")
	}
	return nil
}

func HashPayload(payload json.RawMessage) string {
	payload = CanonicalPayload(payload)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func CanonicalPayload(payload json.RawMessage) json.RawMessage {
	var value any
	if json.Unmarshal(payload, &value) == nil {
		if canonical, err := json.Marshal(value); err == nil {
			return canonical
		}
	}
	return payload
}

func ImmutableDigest(event Event) string {
	identity := struct {
		Kind       string `json:"kind"`
		CapturedAt string `json:"captured_at"`
		Provider   string `json:"provider"`
		ExternalID string `json:"external_id"`
	}{event.Kind, event.CapturedAt.UTC().Format(time.RFC3339Nano), strings.ToLower(event.Provider), event.Account.ExternalID}
	encoded, _ := json.Marshal(identity)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func containsForbiddenKey(payload []byte) bool {
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return false
	}
	forbidden := map[string]struct{}{
		"authorization": {}, "access_token": {}, "refresh_token": {}, "token": {},
		"cookie": {}, "cookies": {}, "prompt": {}, "completion": {}, "source_path": {}, "raw_line": {},
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch typed := v.(type) {
		case map[string]any:
			for key, child := range typed {
				if _, blocked := forbidden[strings.ToLower(strings.TrimSpace(key))]; blocked {
					return true
				}
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}
