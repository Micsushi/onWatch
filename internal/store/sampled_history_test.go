package store

import (
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

func TestSampledQuotaHistoryBoundsRowsAndPreservesEndpoints(t *testing.T) {
	t.Parallel()

	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	const snapshotCount = 12
	const maxPoints = 5

	for i := range snapshotCount {
		capturedAt := base.Add(time.Duration(i) * time.Hour)
		resetAt := capturedAt.Add(5 * time.Hour)

		codex := newTestCodexSnapshot(capturedAt, &resetAt)
		codex.Quotas[0].Utilization = float64(i)
		if _, err := s.InsertCodexSnapshot(codex); err != nil {
			t.Fatalf("InsertCodexSnapshot[%d]: %v", i, err)
		}

		gemini := &api.GeminiSnapshot{
			CapturedAt: capturedAt,
			Tier:       "standard",
			ProjectID:  "sampled-test",
			Quotas: []api.GeminiQuota{{
				ModelID:           "gemini-test",
				RemainingFraction: 1 - float64(i)/100,
				UsagePercent:      float64(i),
				ResetTime:         &resetAt,
			}},
		}
		if _, err := s.InsertGeminiSnapshot(gemini); err != nil {
			t.Fatalf("InsertGeminiSnapshot[%d]: %v", i, err)
		}

		anthropic := &api.AnthropicSnapshot{
			CapturedAt: capturedAt,
			Quotas: []api.AnthropicQuota{{
				Name:        "five_hour",
				Utilization: float64(i),
				ResetsAt:    &resetAt,
			}},
		}
		if _, err := s.InsertAnthropicSnapshot(anthropic); err != nil {
			t.Fatalf("InsertAnthropicSnapshot[%d]: %v", i, err)
		}
	}

	start := base.Add(-time.Hour)
	end := base.Add(snapshotCount * time.Hour)

	codexRows, err := s.QueryCodexRangeSampled(DefaultCodexAccountID, start, end, maxPoints)
	if err != nil {
		t.Fatalf("QueryCodexRangeSampled: %v", err)
	}
	assertSampledCodexRows(t, codexRows, base, snapshotCount, maxPoints)

	geminiRows, err := s.QueryGeminiRangeSampled(start, end, maxPoints)
	if err != nil {
		t.Fatalf("QueryGeminiRangeSampled: %v", err)
	}
	assertSampledGeminiRows(t, geminiRows, base, snapshotCount, maxPoints)

	anthropicRows, err := s.QueryAnthropicRangeSampled(start, end, maxPoints)
	if err != nil {
		t.Fatalf("QueryAnthropicRangeSampled: %v", err)
	}
	assertSampledAnthropicRows(t, anthropicRows, base, snapshotCount, maxPoints)

	singleEnd := base.Add(30 * time.Minute)
	if rows, err := s.QueryCodexRangeSampled(DefaultCodexAccountID, base, singleEnd, maxPoints); err != nil || len(rows) != 1 {
		t.Fatalf("single Codex sampled row count=%d err=%v", len(rows), err)
	}
	if rows, err := s.QueryGeminiRangeSampled(base, singleEnd, maxPoints); err != nil || len(rows) != 1 {
		t.Fatalf("single Gemini sampled row count=%d err=%v", len(rows), err)
	}
	if rows, err := s.QueryAnthropicRangeSampled(base, singleEnd, maxPoints); err != nil || len(rows) != 1 {
		t.Fatalf("single Anthropic sampled row count=%d err=%v", len(rows), err)
	}
}

func TestCodexSampledHistoryPreservesSparseEarlyRows(t *testing.T) {
	t.Parallel()

	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	const sparseDays = 10
	for day := range sparseDays {
		capturedAt := base.Add(time.Duration(day) * 24 * time.Hour)
		resetAt := capturedAt.Add(5 * time.Hour)
		if _, err := s.InsertCodexSnapshot(newTestCodexSnapshot(capturedAt, &resetAt)); err != nil {
			t.Fatalf("InsertCodexSnapshot sparse[%d]: %v", day, err)
		}
	}

	denseStart := base.Add(20 * 24 * time.Hour)
	const denseRows = 600
	for index := range denseRows {
		capturedAt := denseStart.Add(time.Duration(index) * time.Minute)
		resetAt := capturedAt.Add(5 * time.Hour)
		if _, err := s.InsertCodexSnapshot(newTestCodexSnapshot(capturedAt, &resetAt)); err != nil {
			t.Fatalf("InsertCodexSnapshot dense[%d]: %v", index, err)
		}
	}

	const maxPoints = 50
	rows, err := s.QueryCodexRangeSampled(
		DefaultCodexAccountID,
		base.Add(-time.Hour),
		denseStart.Add((denseRows+1)*time.Minute),
		maxPoints,
	)
	if err != nil {
		t.Fatalf("QueryCodexRangeSampled: %v", err)
	}
	if len(rows) > maxPoints {
		t.Fatalf("sampled rows=%d want <=%d", len(rows), maxPoints)
	}

	seen := make(map[time.Time]bool, len(rows))
	for _, row := range rows {
		seen[row.CapturedAt] = true
	}
	for day := range sparseDays {
		capturedAt := base.Add(time.Duration(day) * 24 * time.Hour)
		if !seen[capturedAt] {
			t.Fatalf("sparse Codex history lost day %d at %v", day, capturedAt)
		}
	}
}

func TestAnthropicSampledHistoryPreservesSparseEarlyRows(t *testing.T) {
	t.Parallel()

	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	const sparseDays = 10
	for day := range sparseDays {
		capturedAt := base.Add(time.Duration(day) * 24 * time.Hour)
		resetAt := capturedAt.Add(5 * time.Hour)
		if _, err := s.InsertAnthropicSnapshot(&api.AnthropicSnapshot{
			CapturedAt: capturedAt,
			Quotas: []api.AnthropicQuota{{
				Name:        "five_hour",
				Utilization: float64(day),
				ResetsAt:    &resetAt,
			}},
		}); err != nil {
			t.Fatalf("InsertAnthropicSnapshot sparse[%d]: %v", day, err)
		}
	}

	denseStart := base.Add(20 * 24 * time.Hour)
	const denseRows = 600
	for index := range denseRows {
		capturedAt := denseStart.Add(time.Duration(index) * time.Minute)
		resetAt := capturedAt.Add(5 * time.Hour)
		if _, err := s.InsertAnthropicSnapshot(&api.AnthropicSnapshot{
			CapturedAt: capturedAt,
			Quotas: []api.AnthropicQuota{{
				Name:        "five_hour",
				Utilization: float64(index),
				ResetsAt:    &resetAt,
			}},
		}); err != nil {
			t.Fatalf("InsertAnthropicSnapshot dense[%d]: %v", index, err)
		}
	}

	const maxPoints = 50
	rows, err := s.QueryAnthropicRangeSampled(
		base.Add(-time.Hour),
		denseStart.Add((denseRows+1)*time.Minute),
		maxPoints,
	)
	if err != nil {
		t.Fatalf("QueryAnthropicRangeSampled: %v", err)
	}
	if len(rows) > maxPoints {
		t.Fatalf("sampled rows=%d want <=%d", len(rows), maxPoints)
	}

	seen := make(map[time.Time]bool, len(rows))
	for _, row := range rows {
		seen[row.CapturedAt] = true
	}
	for day := range sparseDays {
		capturedAt := base.Add(time.Duration(day) * 24 * time.Hour)
		if !seen[capturedAt] {
			t.Fatalf("sparse Anthropic history lost day %d at %v", day, capturedAt)
		}
	}
}

func TestCodexSampledHistoryKeepsAllRowsBelowLimit(t *testing.T) {
	t.Parallel()

	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	base := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{0, time.Minute, 24 * time.Hour} {
		capturedAt := base.Add(offset)
		resetAt := capturedAt.Add(5 * time.Hour)
		if _, err := s.InsertCodexSnapshot(newTestCodexSnapshot(capturedAt, &resetAt)); err != nil {
			t.Fatalf("InsertCodexSnapshot[%s]: %v", offset, err)
		}
	}

	rows, err := s.QueryCodexRangeSampled(DefaultCodexAccountID, base, base.Add(25*time.Hour), 5)
	if err != nil {
		t.Fatalf("QueryCodexRangeSampled: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("sampled rows=%d want every row below the limit", len(rows))
	}
}

func assertSampledCodexRows(t *testing.T, rows []*api.CodexSnapshot, base time.Time, total, max int) {
	t.Helper()
	if len(rows) > max {
		t.Fatalf("Codex sampled rows=%d want <=%d", len(rows), max)
	}
	if len(rows) == 0 {
		t.Fatal("Codex sampled rows are empty")
	}
	if !rows[0].CapturedAt.Equal(base) || !rows[len(rows)-1].CapturedAt.Equal(base.Add(time.Duration(total-1)*time.Hour)) {
		t.Fatalf("Codex sampled endpoints=%v..%v", rows[0].CapturedAt, rows[len(rows)-1].CapturedAt)
	}
	if len(rows[0].Quotas) != 2 {
		t.Fatalf("Codex sampled quotas=%d want 2", len(rows[0].Quotas))
	}
}

func assertSampledGeminiRows(t *testing.T, rows []*api.GeminiSnapshot, base time.Time, total, max int) {
	t.Helper()
	if len(rows) > max {
		t.Fatalf("Gemini sampled rows=%d want <=%d", len(rows), max)
	}
	if len(rows) == 0 {
		t.Fatal("Gemini sampled rows are empty")
	}
	if !rows[0].CapturedAt.Equal(base) || !rows[len(rows)-1].CapturedAt.Equal(base.Add(time.Duration(total-1)*time.Hour)) {
		t.Fatalf("Gemini sampled endpoints=%v..%v", rows[0].CapturedAt, rows[len(rows)-1].CapturedAt)
	}
	if len(rows[0].Quotas) != 1 {
		t.Fatalf("Gemini sampled quotas=%d want 1", len(rows[0].Quotas))
	}
}

func assertSampledAnthropicRows(t *testing.T, rows []*api.AnthropicSnapshot, base time.Time, total, max int) {
	t.Helper()
	if len(rows) > max {
		t.Fatalf("Anthropic sampled rows=%d want <=%d", len(rows), max)
	}
	if len(rows) == 0 {
		t.Fatal("Anthropic sampled rows are empty")
	}
	if !rows[0].CapturedAt.Equal(base) || !rows[len(rows)-1].CapturedAt.Equal(base.Add(time.Duration(total-1)*time.Hour)) {
		t.Fatalf("Anthropic sampled endpoints=%v..%v", rows[0].CapturedAt, rows[len(rows)-1].CapturedAt)
	}
	if len(rows[0].Quotas) != 1 {
		t.Fatalf("Anthropic sampled quotas=%d want 1", len(rows[0].Quotas))
	}
}
