package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

// AntigravityAccount identifies a collector account or an email seen by a local poller.
type AntigravityAccount struct {
	ExternalID string
	Email      string
	Local      bool
}

func (s *Store) AntigravityAccounts() ([]AntigravityAccount, error) {
	rows, err := s.db.Query(`SELECT external_account_id FROM central_quota_snapshots WHERE provider='antigravity'
		UNION SELECT external_account_id FROM provider_poll_owners WHERE provider='antigravity' AND ended_at IS NULL`)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]string)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		byID[id] = ""
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	devices, err := s.ListDevices()
	if err != nil {
		return nil, err
	}
	for _, device := range devices {
		if device.RevokedAt != nil {
			continue
		}
		for _, assignment := range device.DesiredConfig.Assignments {
			if assignment.Provider == "antigravity" {
				if _, known := byID[assignment.ExternalID]; known {
					byID[assignment.ExternalID] = strings.TrimSpace(assignment.AntigravityAccountEmail)
				}
			}
		}
	}
	accounts := make([]AntigravityAccount, 0, len(byID))
	bound := make(map[string]int)
	for id, email := range byID {
		accounts = append(accounts, AntigravityAccount{ExternalID: id, Email: email})
		bound[email]++
	}
	// An email assigned to multiple external IDs is only a display label.
	// Keep those account series distinct and don't attach local data to either.
	rows, err = s.db.Query(`SELECT DISTINCT trim(email) FROM antigravity_snapshots WHERE length(trim(email))>0 ORDER BY trim(email)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		if bound[email] == 0 {
			accounts = append(accounts, AntigravityAccount{Email: email, Local: true})
		} else if bound[email] == 1 {
			for i := range accounts {
				if accounts[i].Email == email {
					accounts[i].Local = true
					break
				}
			}
		}
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Email != accounts[j].Email {
			return accounts[i].Email < accounts[j].Email
		}
		return accounts[i].ExternalID < accounts[j].ExternalID
	})
	return accounts, rows.Err()
}

func (s *Store) CentralCodexAccountLabel(externalID string) (string, error) {
	var label string
	err := s.db.QueryRow(`SELECT account_label FROM central_quota_snapshots
		WHERE provider='openai' AND external_account_id=? AND account_label!=''
		ORDER BY captured_at COLLATE ONWATCH_RFC3339 DESC,id DESC LIMIT 1`, externalID).Scan(&label)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return label, err
}

// QueryAntigravityAccountRange uses the ingested receipt to recover the exact
// account and quota windows. Old mirror rows don't record the assigned identity.
func (s *Store) QueryAntigravityAccountRange(account AntigravityAccount, start, end time.Time, limit int) ([]*api.AntigravitySnapshot, error) {
	var snapshots []*api.AntigravitySnapshot
	if account.Local && account.Email != "" {
		var err error
		snapshots, err = s.queryAntigravityRange(start, end, account.Email, limit, 0)
		if err != nil {
			return nil, err
		}
	}
	if account.ExternalID != "" {
		rows, err := s.db.Query(`SELECT c.captured_at,r.payload_json FROM central_quota_snapshots c
			JOIN observation_provenance p ON p.target_table='central_quota_snapshots' AND p.target_record_id=CAST(c.id AS TEXT)
			JOIN ingest_receipts r ON r.device_id=p.device_id AND r.event_id=p.event_id AND r.status='accepted'
			WHERE c.provider='antigravity' AND c.external_account_id=?
			AND c.captured_at COLLATE ONWATCH_RFC3339>=? AND c.captured_at COLLATE ONWATCH_RFC3339<?
			ORDER BY c.captured_at COLLATE ONWATCH_RFC3339 DESC,c.id DESC LIMIT ?`,
			account.ExternalID, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano), limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var captured, raw string
			if err := rows.Scan(&captured, &raw); err != nil {
				return nil, err
			}
			snapshot, err := decodeAntigravityReceipt(account.Email, captured, raw)
			if err != nil {
				return nil, err
			}
			snapshots = append(snapshots, snapshot)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(snapshots, func(i, j int) bool { return snapshots[i].CapturedAt.Before(snapshots[j].CapturedAt) })
	return snapshots, nil
}

// QueryAntigravityAccountRangeSampled samples the combined local and collector
// timeline before loading local detail rows or decoding collector receipts.
func (s *Store) QueryAntigravityAccountRangeSampled(account AntigravityAccount, start, end time.Time, maxPoints int) ([]*api.AntigravitySnapshot, error) {
	var series []string
	if account.Local && account.Email != "" {
		series = append(series, antigravityLocalChartSeries(account.Email))
	}
	if account.ExternalID != "" {
		series = append(series, antigravityCentralChartSeries(account.ExternalID))
	}
	return readQuotaChart(s, "antigravity", series, start, end, maxPoints, func(start, end time.Time, limit int) ([]*api.AntigravitySnapshot, error) {
		return s.queryAntigravityAccountRangeSampled(account, start, end, limit)
	})
}

func (s *Store) queryAntigravityAccountRangeSampled(account AntigravityAccount, start, end time.Time, maxPoints int) ([]*api.AntigravitySnapshot, error) {
	if maxPoints < 1 {
		return nil, fmt.Errorf("antigravity sample size must be positive")
	}
	lower, upper := start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)
	// Start from the indexed account range before looking up provenance and receipts.
	query := `SELECT 0 AS source, id, captured_at, NULL AS device_id, NULL AS event_id
		FROM antigravity_snapshots
		WHERE ? AND trim(email)=? AND captured_at COLLATE ONWATCH_RFC3339>=? AND captured_at COLLATE ONWATCH_RFC3339<?
		UNION ALL
		SELECT 1, -c.id, c.captured_at, r.device_id, r.event_id
		FROM central_quota_snapshots c
		CROSS JOIN observation_provenance p ON p.target_table='central_quota_snapshots' AND p.target_record_id=CAST(c.id AS TEXT)
		CROSS JOIN ingest_receipts r ON r.device_id=p.device_id AND r.event_id=p.event_id AND r.status='accepted'
		WHERE c.provider='antigravity' AND c.external_account_id=? AND c.external_account_id<>''
		AND c.captured_at COLLATE ONWATCH_RFC3339>=? AND c.captured_at COLLATE ONWATCH_RFC3339<?`
	rows, err := s.db.Query(`SELECT points.id, points.captured_at, receipt.payload_json
		FROM (`+sampleStridedHistory(query, "source ASC, id ASC")+`) AS points
		LEFT JOIN ingest_receipts receipt ON receipt.device_id=points.device_id AND receipt.event_id=points.event_id
		ORDER BY points.position`, account.Local && account.Email != "", account.Email, lower, upper,
		account.ExternalID, lower, upper, maxPoints, maxPoints)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snapshots []*api.AntigravitySnapshot
	var localIDs []any
	localPositions := make(map[int64]int)
	for rows.Next() {
		var id int64
		var captured string
		var raw sql.NullString
		if err := rows.Scan(&id, &captured, &raw); err != nil {
			return nil, err
		}
		if !raw.Valid {
			localIDs = append(localIDs, id)
			localPositions[id] = len(snapshots)
			snapshots = append(snapshots, nil)
			continue
		}
		snapshot, err := decodeAntigravityReceipt(account.Email, captured, raw.String)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(localIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(localIDs)), ",")
		local, err := s.queryAntigravitySnapshots(`SELECT id, captured_at, email, plan_name, prompt_credits,
			monthly_credits, model_count FROM antigravity_snapshots WHERE id IN (`+placeholders+`)`, localIDs...)
		if err != nil {
			return nil, err
		}
		for _, snapshot := range local {
			snapshots[localPositions[snapshot.ID]] = snapshot
		}
	}
	return slices.DeleteFunc(snapshots, func(snapshot *api.AntigravitySnapshot) bool { return snapshot == nil }), nil
}

func decodeAntigravityReceipt(email, captured, raw string) (*api.AntigravitySnapshot, error) {
	at, err := time.Parse(time.RFC3339Nano, captured)
	if err != nil {
		return nil, err
	}
	var payload ingest.QuotaSnapshot
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("decode Antigravity quota receipt: %w", err)
	}
	return antigravityReceiptSnapshot(email, at, payload), nil
}

func antigravityReceiptSnapshot(email string, at time.Time, payload ingest.QuotaSnapshot) *api.AntigravitySnapshot {
	snapshot := &api.AntigravitySnapshot{Email: email, CapturedAt: at, PlanName: payload.Plan}
	groups := make(map[string]int)
	for _, metric := range payload.Metrics {
		remaining := math.Max(0, 100-metric.Value)
		if metric.Group == "" {
			window := api.AntigravityQuotaWindowKind(metric.ResetsAt, at)
			snapshot.Models = append(snapshot.Models, api.AntigravityModelQuota{
				ModelID: metric.Name, Label: metric.Name, RemainingPercent: remaining, RemainingFraction: remaining / 100,
				IsExhausted: remaining == 0, ResetTime: metric.ResetsAt, WindowKind: window, WindowLabel: api.AntigravityQuotaWindowLabel(window),
			})
			continue
		}
		index, found := groups[metric.Group]
		if !found {
			index = len(snapshot.SummaryGroups)
			groups[metric.Group] = index
			snapshot.SummaryGroups = append(snapshot.SummaryGroups, api.AntigravityQuotaSummaryGroup{
				GroupKey: metric.Group, DisplayName: api.AntigravityQuotaGroupDisplayName(metric.Group),
			})
		}
		group := &snapshot.SummaryGroups[index]
		group.Buckets = append(group.Buckets, api.AntigravityQuotaSummaryBucket{
			BucketID: metric.Name, DisplayName: metric.Name, RemainingFraction: remaining / 100,
			RemainingPercent: remaining, UsagePercent: metric.Value, Window: metric.Window, ResetTime: metric.ResetsAt,
		})
	}
	return snapshot
}
