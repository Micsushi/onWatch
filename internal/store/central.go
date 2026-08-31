package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	apiintegrations "github.com/onllm-dev/onwatch/v2/internal/api_integrations"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

type Device struct {
	ID                    string
	Name                  string
	Platform              string
	CollectorVersion      string
	CreatedAt             time.Time
	RotatedAt             *time.Time
	RevokedAt             *time.Time
	LastHeartbeatAt       *time.Time
	LastAcceptedEventAt   *time.Time
	QueueBytes            int64
	PendingEvents         int
	OldestQueuedAt        *time.Time
	LastUploadAt          *time.Time
	DesiredConfigRevision int64
	DesiredConfig         ingest.DesiredConfig
}

type PollOwner struct {
	Provider    string
	ExternalID  string
	OwnerKind   string
	DeviceID    string
	EffectiveAt time.Time
}

func (s *Store) ensureCentralSchema() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS devices (
			device_id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			token_digest BLOB NOT NULL,
			platform TEXT NOT NULL DEFAULT '',
			collector_version TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			rotated_at TEXT,
			revoked_at TEXT,
			last_heartbeat_at TEXT,
			last_accepted_event_at TEXT,
			queue_bytes INTEGER NOT NULL DEFAULT 0,
			pending_events INTEGER NOT NULL DEFAULT 0,
			oldest_queued_at TEXT,
			last_upload_at TEXT,
			desired_config_revision INTEGER NOT NULL DEFAULT 0,
			desired_config_json TEXT NOT NULL DEFAULT '{}'
		);
		CREATE INDEX IF NOT EXISTS idx_devices_heartbeat ON devices(last_heartbeat_at);

		CREATE TABLE IF NOT EXISTS ingest_receipts (
			device_id TEXT NOT NULL REFERENCES devices(device_id) ON DELETE RESTRICT,
			event_id TEXT NOT NULL,
			immutable_digest TEXT NOT NULL,
			payload_digest TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			event_kind TEXT NOT NULL,
			status TEXT NOT NULL,
			target_table TEXT NOT NULL,
			target_record_id TEXT NOT NULL,
			accepted_at TEXT NOT NULL,
			enriched_at TEXT,
			PRIMARY KEY (device_id, event_id)
		);

		CREATE TABLE IF NOT EXISTS observation_provenance (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			target_table TEXT NOT NULL,
			target_record_id TEXT NOT NULL,
			device_id TEXT NOT NULL REFERENCES devices(device_id) ON DELETE RESTRICT,
			event_id TEXT NOT NULL,
			observed_at TEXT NOT NULL,
			UNIQUE(device_id, event_id)
		);
		CREATE INDEX IF NOT EXISTS idx_observation_provenance_target ON observation_provenance(target_table, target_record_id);
		CREATE INDEX IF NOT EXISTS idx_observation_provenance_device ON observation_provenance(device_id, observed_at);

		CREATE TABLE IF NOT EXISTS provider_poll_owners (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL,
			external_account_id TEXT NOT NULL,
			owner_kind TEXT NOT NULL CHECK(owner_kind IN ('server', 'device')),
			device_id TEXT REFERENCES devices(device_id) ON DELETE RESTRICT,
			effective_at TEXT NOT NULL,
			ended_at TEXT,
			CHECK((owner_kind = 'server' AND device_id IS NULL) OR (owner_kind = 'device' AND device_id IS NOT NULL))
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_poll_owner_active
			ON provider_poll_owners(provider, external_account_id) WHERE ended_at IS NULL;

		CREATE TABLE IF NOT EXISTS central_quota_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL,
			external_account_id TEXT NOT NULL,
			account_label TEXT NOT NULL DEFAULT '',
			captured_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_central_quota_range
			ON central_quota_snapshots(provider, external_account_id, captured_at COLLATE ONWATCH_RFC3339);

		CREATE TABLE IF NOT EXISTS central_quota_values (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			snapshot_id INTEGER NOT NULL REFERENCES central_quota_snapshots(id) ON DELETE CASCADE,
			metric_name TEXT NOT NULL,
			value REAL NOT NULL,
			limit_value REAL,
			unit TEXT NOT NULL,
			resets_at TEXT,
			status TEXT NOT NULL DEFAULT '',
			UNIQUE(snapshot_id, metric_name)
		);
	`)
	return err
}

func GenerateDeviceCredential() (deviceID, token string, err error) {
	idBytes := make([]byte, 16)
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(idBytes); err != nil {
		return "", "", fmt.Errorf("generate device id: %w", err)
	}
	if _, err = rand.Read(tokenBytes); err != nil {
		return "", "", fmt.Errorf("generate device token: %w", err)
	}
	return "dev_" + hex.EncodeToString(idBytes), base64.RawURLEncoding.EncodeToString(tokenBytes), nil
}

func tokenDigest(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}

func (s *Store) CreateDevice(name, platform string) (*Device, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return nil, "", fmt.Errorf("device name is required and must be at most 128 characters")
	}
	deviceID, token, err := GenerateDeviceCredential()
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	_, err = s.db.Exec(`INSERT INTO devices(device_id, name, token_digest, platform, created_at, desired_config_json) VALUES(?, ?, ?, ?, ?, ?)`,
		deviceID, name, tokenDigest(token), strings.TrimSpace(platform), now.Format(time.RFC3339Nano), `{"revision":0}`)
	if err != nil {
		return nil, "", fmt.Errorf("create device: %w", err)
	}
	device, err := s.GetDevice(deviceID)
	return device, token, err
}

func (s *Store) AuthenticateDevice(deviceID, token string) (*Device, error) {
	if err := ingest.ValidateDeviceID(deviceID); err != nil || strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("invalid device credential")
	}
	var stored []byte
	var revoked sql.NullString
	if err := s.db.QueryRow(`SELECT token_digest, revoked_at FROM devices WHERE device_id = ?`, deviceID).Scan(&stored, &revoked); err != nil {
		return nil, fmt.Errorf("invalid device credential")
	}
	presented := tokenDigest(token)
	if len(stored) != len(presented) || subtle.ConstantTimeCompare(stored, presented) != 1 || revoked.Valid {
		return nil, fmt.Errorf("invalid device credential")
	}
	return s.GetDevice(deviceID)
}

func (s *Store) RotateDeviceToken(deviceID string) (string, error) {
	if err := ingest.ValidateDeviceID(deviceID); err != nil {
		return "", err
	}
	_, token, err := GenerateDeviceCredential()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(`UPDATE devices SET token_digest = ?, rotated_at = ? WHERE device_id = ? AND revoked_at IS NULL`, tokenDigest(token), now, deviceID)
	if err != nil {
		return "", fmt.Errorf("rotate device token: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return "", fmt.Errorf("device not found")
	}
	return token, nil
}

func (s *Store) RevokeDevice(deviceID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE devices SET revoked_at = ?, desired_config_revision = desired_config_revision + 1, desired_config_json = json_object('revision', desired_config_revision + 1) WHERE device_id = ? AND revoked_at IS NULL`, now, deviceID)
	if err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		var exists int
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM devices WHERE device_id = ?)`, deviceID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("device not found")
		}
		return tx.Commit()
	}
	if _, err := tx.Exec(`UPDATE provider_poll_owners SET ended_at = ? WHERE device_id = ? AND ended_at IS NULL`, now, deviceID); err != nil {
		return fmt.Errorf("end revoked device ownership: %w", err)
	}
	return tx.Commit()
}

func (s *Store) GetDevice(deviceID string) (*Device, error) {
	row := s.db.QueryRow(`SELECT device_id, name, platform, collector_version, created_at, rotated_at, revoked_at,
		last_heartbeat_at, last_accepted_event_at, queue_bytes, pending_events, oldest_queued_at, last_upload_at,
		desired_config_revision, desired_config_json FROM devices WHERE device_id = ?`, deviceID)
	return scanDevice(row)
}

func (s *Store) ListDevices() ([]Device, error) {
	rows, err := s.db.Query(`SELECT device_id, name, platform, collector_version, created_at, rotated_at, revoked_at,
		last_heartbeat_at, last_accepted_event_at, queue_bytes, pending_events, oldest_queued_at, last_upload_at,
		desired_config_revision, desired_config_json FROM devices ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var devices []Device
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, *device)
	}
	return devices, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanDevice(row rowScanner) (*Device, error) {
	var device Device
	var created, configJSON string
	var rotated, revoked, heartbeat, accepted, oldest, upload sql.NullString
	if err := row.Scan(&device.ID, &device.Name, &device.Platform, &device.CollectorVersion, &created, &rotated, &revoked,
		&heartbeat, &accepted, &device.QueueBytes, &device.PendingEvents, &oldest, &upload,
		&device.DesiredConfigRevision, &configJSON); err != nil {
		return nil, err
	}
	device.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	device.RotatedAt = parseNullableTime(rotated)
	device.RevokedAt = parseNullableTime(revoked)
	device.LastHeartbeatAt = parseNullableTime(heartbeat)
	device.LastAcceptedEventAt = parseNullableTime(accepted)
	device.OldestQueuedAt = parseNullableTime(oldest)
	device.LastUploadAt = parseNullableTime(upload)
	_ = json.Unmarshal([]byte(configJSON), &device.DesiredConfig)
	return &device, nil
}

func parseNullableTime(value sql.NullString) *time.Time {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func (s *Store) RecordHeartbeat(deviceID string, heartbeat ingest.Heartbeat) (*Device, error) {
	var oldest, upload any
	if heartbeat.OldestQueuedAt != nil {
		oldest = heartbeat.OldestQueuedAt.UTC().Format(time.RFC3339Nano)
	}
	if heartbeat.LastUploadAt != nil {
		upload = heartbeat.LastUploadAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := s.db.Exec(`UPDATE devices SET platform = ?, collector_version = ?, last_heartbeat_at = ?, queue_bytes = ?, pending_events = ?, oldest_queued_at = ?, last_upload_at = ? WHERE device_id = ? AND revoked_at IS NULL`,
		heartbeat.Platform, heartbeat.CollectorVersion, time.Now().UTC().Format(time.RFC3339Nano), heartbeat.QueueBytes, heartbeat.PendingEvents, oldest, upload, deviceID)
	if err != nil {
		return nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, fmt.Errorf("device unavailable")
	}
	return s.GetDevice(deviceID)
}

func (s *Store) SetPollOwner(provider, externalID, ownerKind, deviceID string) error {
	provider = normalizeCentralProvider(provider)
	externalID = strings.TrimSpace(externalID)
	if provider == "" || externalID == "" || (ownerKind != "server" && ownerKind != "device") {
		return fmt.Errorf("invalid poll owner")
	}
	if ownerKind == "server" {
		deviceID = ""
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var activeKind string
	var activeDevice sql.NullString
	err = tx.QueryRow(`SELECT owner_kind, device_id FROM provider_poll_owners WHERE provider = ? AND external_account_id = ? AND ended_at IS NULL`, provider, externalID).Scan(&activeKind, &activeDevice)
	if err == nil {
		if activeKind == ownerKind && ((!activeDevice.Valid && deviceID == "") || (activeDevice.Valid && activeDevice.String == deviceID)) {
			return tx.Commit()
		}
		return fmt.Errorf("poll owner already active; unassign the old owner before assigning a new one")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if ownerKind == "device" {
		var revoked sql.NullString
		if err := tx.QueryRow(`SELECT revoked_at FROM devices WHERE device_id = ?`, deviceID).Scan(&revoked); err != nil || revoked.Valid {
			return fmt.Errorf("active device is required for device ownership")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var device any
	if deviceID != "" {
		device = deviceID
	}
	if _, err := tx.Exec(`INSERT INTO provider_poll_owners(provider, external_account_id, owner_kind, device_id, effective_at) VALUES(?, ?, ?, ?, ?)`, provider, externalID, ownerKind, device, now); err != nil {
		return err
	}
	return tx.Commit()
}

// HasServerPollOwner reports whether the central process owns any active account
// for a provider. It is used only as the outer guard for single-account agents.
func (s *Store) HasServerPollOwner(provider string) bool {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM provider_poll_owners WHERE provider = ? AND owner_kind = 'server' AND ended_at IS NULL`, normalizeCentralProvider(provider)).Scan(&count)
	return err == nil && count > 0
}

func (s *Store) IsServerPollOwner(provider, externalID string) bool {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM provider_poll_owners WHERE provider = ? AND external_account_id = ? AND owner_kind = 'server' AND ended_at IS NULL`, normalizeCentralProvider(provider), strings.TrimSpace(externalID)).Scan(&count)
	return err == nil && count == 1
}

func (s *Store) ClearPollOwner(provider, externalID string) error {
	provider = normalizeCentralProvider(provider)
	externalID = strings.TrimSpace(externalID)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ownerKind string
	var ownerDevice sql.NullString
	if err := tx.QueryRow(`SELECT owner_kind, device_id FROM provider_poll_owners WHERE provider = ? AND external_account_id = ? AND ended_at IS NULL`, provider, externalID).Scan(&ownerKind, &ownerDevice); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("active poll owner not found")
		}
		return err
	}
	result, err := tx.Exec(`UPDATE provider_poll_owners SET ended_at = ? WHERE provider = ? AND external_account_id = ? AND ended_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), provider, externalID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("active poll owner changed")
	}
	if ownerKind == "device" && ownerDevice.Valid {
		var revision int64
		var configJSON string
		if err := tx.QueryRow(`SELECT desired_config_revision, desired_config_json FROM devices WHERE device_id = ?`, ownerDevice.String).Scan(&revision, &configJSON); err != nil {
			return err
		}
		var config ingest.DesiredConfig
		if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
			return err
		}
		filtered := config.Assignments[:0]
		for _, assignment := range config.Assignments {
			if normalizeCentralProvider(assignment.Provider) != provider || assignment.ExternalID != externalID {
				filtered = append(filtered, assignment)
			}
		}
		config.Assignments = filtered
		config.Revision = revision + 1
		encoded, err := json.Marshal(config)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE devices SET desired_config_revision = ?, desired_config_json = ? WHERE device_id = ?`, config.Revision, string(encoded), ownerDevice.String); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func normalizeCentralProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "codex" {
		return "openai"
	}
	return provider
}

func (s *Store) ListPollOwners() ([]PollOwner, error) {
	rows, err := s.db.Query(`SELECT provider, external_account_id, owner_kind, COALESCE(device_id, ''), effective_at FROM provider_poll_owners WHERE ended_at IS NULL ORDER BY provider, external_account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owners []PollOwner
	for rows.Next() {
		var owner PollOwner
		var effective string
		if err := rows.Scan(&owner.Provider, &owner.ExternalID, &owner.OwnerKind, &owner.DeviceID, &effective); err != nil {
			return nil, err
		}
		owner.EffectiveAt, _ = time.Parse(time.RFC3339Nano, effective)
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}

func (s *Store) SetDeviceDesiredConfig(deviceID string, config ingest.DesiredConfig) error {
	if err := ingest.ValidateDesiredConfig(config); err != nil {
		return err
	}
	current, err := s.GetDevice(deviceID)
	if err != nil {
		return err
	}
	config.Revision = current.DesiredConfigRevision + 1
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`UPDATE devices SET desired_config_revision = ?, desired_config_json = ? WHERE device_id = ?`, config.Revision, string(encoded), deviceID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("device not found")
	}
	return nil
}

func (s *Store) StoreIngestBatch(device *Device, events []ingest.Event, now time.Time) ([]ingest.EventResult, error) {
	if device == nil {
		return nil, fmt.Errorf("device is required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	results := make([]ingest.EventResult, 0, len(events))
	for _, event := range events {
		result, err := storeIngestEvent(tx, device.ID, event, now)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	accepted := false
	for _, result := range results {
		if result.Status == "accepted" || result.Status == "enriched" {
			accepted = true
			break
		}
	}
	if accepted {
		if _, err := tx.Exec(`UPDATE devices SET last_accepted_event_at = ? WHERE device_id = ?`, now.UTC().Format(time.RFC3339Nano), device.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if accepted {
		s.bumpAPIIntegrationUsageVersion()
	}
	return results, nil
}

func storeIngestEvent(tx *sql.Tx, deviceID string, event ingest.Event, now time.Time) (ingest.EventResult, error) {
	result := ingest.EventResult{EventID: event.EventID}
	payloadDigest := ingest.HashPayload(event.Payload)
	immutableDigest := ingest.ImmutableDigest(event)
	var priorImmutable, priorPayloadDigest, priorPayloadJSON, priorStatus, targetTable, targetID string
	err := tx.QueryRow(`SELECT immutable_digest, payload_digest, payload_json, status, target_table, target_record_id FROM ingest_receipts WHERE device_id = ? AND event_id = ?`, deviceID, event.EventID).Scan(&priorImmutable, &priorPayloadDigest, &priorPayloadJSON, &priorStatus, &targetTable, &targetID)
	if err == nil {
		if subtle.ConstantTimeCompare([]byte(priorImmutable), []byte(immutableDigest)) != 1 {
			result.Status, result.Code = "rejected", "event_identity_conflict"
			return result, nil
		}
		if subtle.ConstantTimeCompare([]byte(priorPayloadDigest), []byte(payloadDigest)) == 1 {
			result.Status = "duplicate"
			return result, nil
		}
		if event.Kind == "usage_event" && jsonIsEnrichment([]byte(priorPayloadJSON), event.Payload) {
			if err := enrichUsageEvent(tx, targetID, deviceID, event); err != nil {
				result.Status, result.Code = "rejected", "event_payload_conflict"
				return result, nil
			}
			if _, err := tx.Exec(`UPDATE ingest_receipts SET payload_digest = ?, payload_json = ?, status = 'enriched', enriched_at = ? WHERE device_id = ? AND event_id = ?`, payloadDigest, string(ingest.CanonicalPayload(event.Payload)), now.UTC().Format(time.RFC3339Nano), deviceID, event.EventID); err != nil {
				return result, err
			}
			result.Status = "enriched"
			return result, nil
		}
		result.Status, result.Code = "rejected", "event_payload_conflict"
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}

	targetTable, targetID = "", ""
	if event.Kind == "usage_event" {
		eventRecord, err := apiintegrations.ParseUsageEventLine(event.Payload, "device:"+deviceID)
		if err != nil {
			result.Status, result.Code = "rejected", "invalid_usage_payload"
			return result, nil
		}
		if eventRecord.Provider != strings.ToLower(event.Provider) || eventRecord.Account != event.Account.ExternalID || !eventRecord.Timestamp.Equal(event.CapturedAt) {
			result.Status, result.Code = "rejected", "payload_envelope_mismatch"
			return result, nil
		}
		metadata, storedMetadata := normalizedAPIIntegrationMetadata(eventRecord)
		insert, err := tx.Exec(`INSERT OR IGNORE INTO api_integration_usage_events (
			captured_at, integration_name, provider, account_name, model, request_id, prompt_tokens, completion_tokens,
			total_tokens, cost_usd, latency_ms, metadata_json, session_id, reasoning_effort, mode, speed_mode,
			input_tokens, cached_input_tokens, cache_creation_input_tokens, output_tokens, reasoning_output_tokens,
			source_path, fingerprint, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			eventRecord.Timestamp.Format(time.RFC3339Nano), eventRecord.Integration, eventRecord.Provider, eventRecord.Account,
			eventRecord.Model, eventRecord.RequestID, eventRecord.PromptTokens, eventRecord.CompletionTokens, eventRecord.TotalTokens,
			eventRecord.CostUSD, eventRecord.LatencyMS, storedMetadata, metadata.SessionID, metadata.ReasoningEffort, metadata.Mode,
			metadata.SpeedMode, normalizedTokenValue(metadata.InputTokens, eventRecord.PromptTokens), normalizedTokenValue(metadata.CachedInputTokens, 0),
			normalizedTokenValue(metadata.CacheCreationInputTokens, 0), normalizedTokenValue(metadata.OutputTokens, eventRecord.CompletionTokens),
			normalizedTokenValue(metadata.ReasoningOutputTokens, 0), eventRecord.SourcePath, eventRecord.Fingerprint, now.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return result, err
		}
		rows, _ := insert.RowsAffected()
		var id int64
		if rows == 1 {
			id, _ = insert.LastInsertId()
			result.Status = "accepted"
		} else {
			if err := tx.QueryRow(`SELECT id FROM api_integration_usage_events WHERE fingerprint = ?`, eventRecord.Fingerprint).Scan(&id); err != nil {
				return result, err
			}
			result.Status = "duplicate"
		}
		targetTable, targetID = "api_integration_usage_events", fmt.Sprintf("%d", id)
	} else {
		var ownerKind string
		var ownerDevice sql.NullString
		err := tx.QueryRow(`SELECT owner_kind, device_id FROM provider_poll_owners WHERE provider = ? AND external_account_id = ? AND ended_at IS NULL`, normalizeCentralProvider(event.Provider), event.Account.ExternalID).Scan(&ownerKind, &ownerDevice)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (ownerKind != "device" || !ownerDevice.Valid || ownerDevice.String != deviceID)) {
			result.Status, result.Code = "rejected", "ownership_conflict"
			return result, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result, err
		}
		var snapshot ingest.QuotaSnapshot
		if err := json.Unmarshal(event.Payload, &snapshot); err != nil {
			result.Status, result.Code = "rejected", "invalid_quota_payload"
			return result, nil
		}
		insert, err := tx.Exec(`INSERT INTO central_quota_snapshots(provider, external_account_id, account_label, captured_at, created_at) VALUES(?, ?, ?, ?, ?)`,
			normalizeCentralProvider(event.Provider), event.Account.ExternalID, event.Account.Label, event.CapturedAt.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return result, err
		}
		id, _ := insert.LastInsertId()
		for _, metric := range snapshot.Metrics {
			var reset any
			if metric.ResetsAt != nil {
				reset = metric.ResetsAt.UTC().Format(time.RFC3339Nano)
			}
			if _, err := tx.Exec(`INSERT INTO central_quota_values(snapshot_id, metric_name, value, limit_value, unit, resets_at, status) VALUES(?, ?, ?, ?, ?, ?, ?)`, id, metric.Name, metric.Value, metric.Limit, metric.Unit, reset, metric.Status); err != nil {
				return result, err
			}
		}
		if err := mirrorCentralQuotaSnapshot(tx, normalizeCentralProvider(event.Provider), event.Account.ExternalID, event.CapturedAt, snapshot); err != nil {
			return result, err
		}
		targetTable, targetID, result.Status = "central_quota_snapshots", fmt.Sprintf("%d", id), "accepted"
	}

	if result.Status == "accepted" || result.Status == "duplicate" {
		if _, err := tx.Exec(`INSERT INTO ingest_receipts(device_id, event_id, immutable_digest, payload_digest, payload_json, event_kind, status, target_table, target_record_id, accepted_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			deviceID, event.EventID, immutableDigest, payloadDigest, string(ingest.CanonicalPayload(event.Payload)), event.Kind, result.Status, targetTable, targetID, now.UTC().Format(time.RFC3339Nano)); err != nil {
			return result, err
		}
		if _, err := tx.Exec(`INSERT INTO observation_provenance(target_table, target_record_id, device_id, event_id, observed_at) VALUES(?, ?, ?, ?, ?)`, targetTable, targetID, deviceID, event.EventID, event.CapturedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return result, err
		}
	}
	return result, nil
}

func mirrorCentralQuotaSnapshot(tx *sql.Tx, provider, externalID string, capturedAt time.Time, snapshot ingest.QuotaSnapshot) error {
	captured := capturedAt.UTC().Format(time.RFC3339Nano)
	insertValues := func(query string, id int64, includeStatus bool) error {
		for _, metric := range snapshot.Metrics {
			var reset any
			if metric.ResetsAt != nil {
				reset = metric.ResetsAt.UTC().Format(time.RFC3339Nano)
			}
			args := []any{id, metric.Name, metric.Value, reset}
			if includeStatus {
				args = append(args, metric.Status)
			}
			if _, err := tx.Exec(query, args...); err != nil {
				return err
			}
		}
		return nil
	}
	switch provider {
	case "anthropic":
		result, err := tx.Exec(`INSERT INTO anthropic_snapshots(captured_at, raw_json, quota_count) VALUES(?, '', ?)`, captured, len(snapshot.Metrics))
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		return insertValues(`INSERT INTO anthropic_quota_values(snapshot_id, quota_name, utilization, resets_at) VALUES(?, ?, ?, ?)`, id, false)
	case "openai":
		accountID := int64(1)
		if parsed, err := strconv.ParseInt(externalID, 10, 64); err == nil && parsed > 0 {
			accountID = parsed
		}
		result, err := tx.Exec(`INSERT INTO codex_snapshots(captured_at, account_id, raw_json, quota_count) VALUES(?, ?, '', ?)`, captured, accountID, len(snapshot.Metrics))
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		return insertValues(`INSERT INTO codex_quota_values(snapshot_id, quota_name, utilization, resets_at, status) VALUES(?, ?, ?, ?, ?)`, id, true)
	case "copilot":
		result, err := tx.Exec(`INSERT INTO copilot_snapshots(captured_at, raw_json, quota_count) VALUES(?, '', ?)`, captured, len(snapshot.Metrics))
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		for _, metric := range snapshot.Metrics {
			limit := int64(100)
			if metric.Limit != nil && *metric.Limit > 0 {
				limit = int64(math.Round(*metric.Limit))
			}
			remaining := int64(math.Round(float64(limit) * (1 - metric.Value/100)))
			if remaining < 0 {
				remaining = 0
			}
			if _, err := tx.Exec(`INSERT INTO copilot_quota_values(snapshot_id, quota_name, entitlement, remaining, percent_remaining) VALUES(?, ?, ?, ?, ?)`, id, metric.Name, limit, remaining, 100-metric.Value); err != nil {
				return err
			}
		}
		return nil
	case "gemini":
		result, err := tx.Exec(`INSERT INTO gemini_snapshots(captured_at, raw_json, quota_count) VALUES(?, '', ?)`, captured, len(snapshot.Metrics))
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		for _, metric := range snapshot.Metrics {
			var reset any
			if metric.ResetsAt != nil {
				reset = metric.ResetsAt.UTC().Format(time.RFC3339Nano)
			}
			remaining := math.Max(0, 1-metric.Value/100)
			if _, err := tx.Exec(`INSERT INTO gemini_quota_values(snapshot_id, model_id, remaining_fraction, usage_percent, reset_time) VALUES(?, ?, ?, ?, ?)`, id, metric.Name, remaining, metric.Value, reset); err != nil {
				return err
			}
		}
		return nil
	case "cursor":
		result, err := tx.Exec(`INSERT INTO cursor_snapshots(captured_at, raw_json, quota_count) VALUES(?, '', ?)`, captured, len(snapshot.Metrics))
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		for _, metric := range snapshot.Metrics {
			limit := 100.0
			if metric.Limit != nil && *metric.Limit > 0 {
				limit = *metric.Limit
			}
			used := limit * metric.Value / 100
			var reset any
			if metric.ResetsAt != nil {
				reset = metric.ResetsAt.UTC().Format(time.RFC3339Nano)
			}
			if _, err := tx.Exec(`INSERT INTO cursor_quota_values(snapshot_id, quota_name, used, limit_value, utilization, format, resets_at) VALUES(?, ?, ?, ?, ?, ?, ?)`, id, metric.Name, used, limit, metric.Value, metric.Unit, reset); err != nil {
				return err
			}
		}
		return nil
	case "openrouter":
		metrics := quotaMetricMap(snapshot.Metrics)
		usage, daily, weekly, monthly := metricValue(metrics["usage"]), metricValue(metrics["daily"]), metricValue(metrics["weekly"]), metricValue(metrics["monthly"])
		var limit, remaining any
		if metrics["usage"] != nil && metrics["usage"].Limit != nil {
			limit = *metrics["usage"].Limit
			remaining = math.Max(0, *metrics["usage"].Limit-usage)
		}
		_, err := tx.Exec(`INSERT INTO openrouter_snapshots(captured_at, label, usage, usage_daily, usage_weekly, usage_monthly, credit_limit, limit_remaining) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, captured, externalID, usage, daily, weekly, monthly, limit, remaining)
		return err
	case "antigravity":
		result, err := tx.Exec(`INSERT INTO antigravity_snapshots(captured_at, raw_json, model_count) VALUES(?, '', ?)`, captured, len(snapshot.Metrics))
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		for _, metric := range snapshot.Metrics {
			remaining := math.Max(0, 100-metric.Value)
			var reset any
			if metric.ResetsAt != nil {
				reset = metric.ResetsAt.UTC().Format(time.RFC3339Nano)
			}
			if _, err := tx.Exec(`INSERT INTO antigravity_model_values(snapshot_id, model_id, label, remaining_fraction, remaining_percent, is_exhausted, reset_time) VALUES(?, ?, ?, ?, ?, ?, ?)`, id, metric.Name, metric.Name, remaining/100, remaining, boolInt(remaining <= 0), reset); err != nil {
				return err
			}
		}
		return nil
	case "minimax":
		result, err := tx.Exec(`INSERT INTO minimax_snapshots(captured_at, raw_json, model_count) VALUES(?, '', ?)`, captured, len(snapshot.Metrics))
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		for _, metric := range snapshot.Metrics {
			total := int64(100)
			if metric.Limit != nil && *metric.Limit > 0 {
				total = int64(math.Round(*metric.Limit))
			}
			used := int64(math.Round(float64(total) * metric.Value / 100))
			remain := total - used
			if remain < 0 {
				remain = 0
			}
			var reset any
			if metric.ResetsAt != nil {
				reset = metric.ResetsAt.UTC().Format(time.RFC3339Nano)
			}
			if _, err := tx.Exec(`INSERT INTO minimax_model_values(snapshot_id, model_name, total, remain, used, used_percent, reset_at) VALUES(?, ?, ?, ?, ?, ?, ?)`, id, metric.Name, total, remain, used, metric.Value, reset); err != nil {
				return err
			}
		}
		return nil
	case "zai":
		metrics := quotaMetricMap(snapshot.Metrics)
		timeMetric, tokenMetric := metrics["time"], metrics["tokens"]
		if timeMetric == nil || tokenMetric == nil {
			return fmt.Errorf("zai quota snapshot requires time and tokens metrics")
		}
		var tokenReset any
		if tokenMetric.ResetsAt != nil {
			tokenReset = tokenMetric.ResetsAt.UTC().Format(time.RFC3339Nano)
		}
		_, err := tx.Exec(`INSERT INTO zai_snapshots(captured_at, time_limit, time_unit, time_number, time_usage, time_current_value, time_remaining, time_percentage, tokens_limit, tokens_unit, tokens_number, tokens_usage, tokens_current_value, tokens_remaining, tokens_percentage, tokens_next_reset) VALUES(?, 100, 1, 1, 100, ?, ?, ?, 100, 1, 1, 100, ?, ?, ?, ?)`, captured, timeMetric.Value, math.Max(0, 100-timeMetric.Value), int(math.Round(timeMetric.Value)), tokenMetric.Value, math.Max(0, 100-tokenMetric.Value), int(math.Round(tokenMetric.Value)), tokenReset)
		return err
	}
	return nil
}

func quotaMetricMap(metrics []ingest.QuotaMetric) map[string]*ingest.QuotaMetric {
	result := make(map[string]*ingest.QuotaMetric, len(metrics))
	for i := range metrics {
		metric := &metrics[i]
		result[strings.ToLower(metric.Name)] = metric
	}
	return result
}

func metricValue(metric *ingest.QuotaMetric) float64 {
	if metric == nil {
		return 0
	}
	return metric.Value
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func jsonIsEnrichment(previous, current []byte) bool {
	var oldValue, newValue any
	if json.Unmarshal(previous, &oldValue) != nil || json.Unmarshal(current, &newValue) != nil {
		return false
	}
	var contains func(any, any) bool
	contains = func(oldPart, newPart any) bool {
		oldMap, oldIsMap := oldPart.(map[string]any)
		newMap, newIsMap := newPart.(map[string]any)
		if oldIsMap || newIsMap {
			if !oldIsMap || !newIsMap {
				return false
			}
			for key, oldChild := range oldMap {
				newChild, ok := newMap[key]
				if !ok || !contains(oldChild, newChild) {
					return false
				}
			}
			return true
		}
		return jsonEqual(oldPart, newPart)
	}
	return contains(oldValue, newValue) && !jsonEqual(oldValue, newValue)
}

func jsonEqual(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return subtle.ConstantTimeCompare(leftJSON, rightJSON) == 1
}

func enrichUsageEvent(tx *sql.Tx, targetID, deviceID string, event ingest.Event) error {
	record, err := apiintegrations.ParseUsageEventLine(event.Payload, "device:"+deviceID)
	if err != nil || record.Provider != strings.ToLower(event.Provider) || record.Account != event.Account.ExternalID || !record.Timestamp.Equal(event.CapturedAt) {
		return fmt.Errorf("invalid usage enrichment")
	}
	metadata, storedMetadata := normalizedAPIIntegrationMetadata(record)
	result, err := tx.Exec(`UPDATE api_integration_usage_events SET metadata_json = ?, session_id = ?, reasoning_effort = ?, mode = ?, speed_mode = ? WHERE id = ?`, storedMetadata, metadata.SessionID, metadata.ReasoningEffort, metadata.Mode, metadata.SpeedMode, targetID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("usage event missing")
	}
	return nil
}

func (s *Store) Ready() error {
	var one int
	return s.db.QueryRow(`SELECT 1`).Scan(&one)
}
