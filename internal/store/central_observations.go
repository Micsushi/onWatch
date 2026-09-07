package store

import (
	"encoding/json"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

type CentralQuotaObservation struct {
	ID         int64
	Provider   string
	ExternalID string
	AccountID  int64
	CapturedAt time.Time
	Snapshot   ingest.QuotaSnapshot
}

// CentralQuotaObservations returns the latest observation of each account after
// the durable monitor cursor. Late offline batches cannot replace current state.
func (s *Store) CentralQuotaObservations(after int64) ([]CentralQuotaObservation, error) {
	rows, err := s.db.Query(`SELECT q.id, q.provider, q.external_account_id, q.captured_at, r.payload_json,
		COALESCE((SELECT a.id FROM provider_accounts a WHERE a.provider = 'codex' AND
		a.external_id = q.external_account_id ORDER BY a.id LIMIT 1),
		(SELECT a.id FROM provider_accounts a WHERE a.provider = 'codex' AND
		CAST(a.id AS TEXT) = q.external_account_id LIMIT 1), 1)
		FROM central_quota_snapshots q JOIN ingest_receipts r
		ON r.target_table = 'central_quota_snapshots' AND r.target_record_id = CAST(q.id AS TEXT)
		WHERE q.id > ? AND NOT EXISTS (SELECT 1 FROM central_quota_snapshots newer
		WHERE newer.provider = q.provider AND newer.external_account_id = q.external_account_id
		AND (newer.captured_at COLLATE ONWATCH_RFC3339 > q.captured_at OR
		(newer.captured_at COLLATE ONWATCH_RFC3339 = q.captured_at AND newer.id > q.id)))
		ORDER BY q.id LIMIT 100`, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var observations []CentralQuotaObservation
	for rows.Next() {
		var observation CentralQuotaObservation
		var captured, payload string
		if err := rows.Scan(&observation.ID, &observation.Provider, &observation.ExternalID, &captured, &payload, &observation.AccountID); err != nil {
			return nil, err
		}
		if observation.CapturedAt, err = time.Parse(time.RFC3339Nano, captured); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(payload), &observation.Snapshot); err != nil {
			return nil, err
		}
		observations = append(observations, observation)
	}
	return observations, rows.Err()
}
