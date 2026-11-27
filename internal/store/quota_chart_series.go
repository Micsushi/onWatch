package store

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

const quotaChartBackfillPointLimit = 2000

func codexChartSeries(accountID int64) string         { return strconv.FormatInt(accountID, 10) }
func antigravityLocalChartSeries(email string) string { return "0:" + email }
func antigravityCentralChartSeries(id string) string  { return "1:" + id }

func makeQuotaChartChunk[T quotaChartSnapshot](series string, points []*T) (quotaChartChunk, error) {
	if len(points) == 0 {
		return quotaChartChunk{}, nil
	}
	data, err := encodeQuotaChart(sampleQuotaChart(points, quotaChartPointsPerDay))
	return quotaChartChunk{series, data}, err
}

func (s *Store) buildQuotaChartChunks(ctx context.Context, provider string, start, end time.Time) ([]quotaChartChunk, error) {
	lower, upper := start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)
	var chunks []quotaChartChunk
	add := func(chunk quotaChartChunk, err error) error {
		if chunk.points != nil && err == nil {
			chunks = append(chunks, chunk)
		}
		return err
	}
	switch provider {
	case "anthropic":
		points, err := s.queryAnthropicRangeSampled(start, end, quotaChartBackfillPointLimit)
		if err != nil {
			return nil, err
		}
		if err := add(makeQuotaChartChunk("", points)); err != nil {
			return nil, err
		}
	case "codex":
		accounts, err := s.quotaChartSeriesKeys(ctx, `SELECT DISTINCT CAST(account_id AS TEXT) FROM codex_snapshots
			WHERE captured_at COLLATE ONWATCH_RFC3339>=? AND captured_at COLLATE ONWATCH_RFC3339<?`, lower, upper)
		if err != nil {
			return nil, err
		}
		for _, key := range accounts {
			id, err := strconv.ParseInt(key, 10, 64)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			points, err := s.queryCodexRangeSampled(id, start, end, quotaChartBackfillPointLimit)
			if err != nil {
				return nil, err
			}
			if err := add(makeQuotaChartChunk(key, points)); err != nil {
				return nil, err
			}
		}
	case "antigravity":
		points, err := s.queryAntigravityRange(start, end, "", 0, quotaChartBackfillPointLimit)
		if err != nil {
			return nil, err
		}
		if err := add(makeQuotaChartChunk("", points)); err != nil {
			return nil, err
		}
		keys, err := s.quotaChartSeriesKeys(ctx, `SELECT DISTINCT '0:' || trim(email) FROM antigravity_snapshots
			WHERE length(trim(email))>0 AND captured_at COLLATE ONWATCH_RFC3339>=? AND captured_at COLLATE ONWATCH_RFC3339<?
			UNION SELECT DISTINCT '1:' || external_account_id FROM central_quota_snapshots
			WHERE provider='antigravity' AND external_account_id<>'' AND captured_at COLLATE ONWATCH_RFC3339>=? AND captured_at COLLATE ONWATCH_RFC3339<?`, lower, upper, lower, upper)
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			account := AntigravityAccount{ExternalID: key[2:]}
			if key[0] == '0' {
				account = AntigravityAccount{Local: true, Email: key[2:]}
			}
			points, err := s.queryAntigravityAccountRangeSampled(account, start, end, quotaChartBackfillPointLimit)
			if err != nil {
				return nil, err
			}
			if err := add(makeQuotaChartChunk(key, points)); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unsupported quota chart provider %q", provider)
	}
	return chunks, nil
}

func (s *Store) quotaChartSeriesKeys(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
