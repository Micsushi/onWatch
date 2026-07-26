package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPollHealthStatePersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "poll-health.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	firstFailure := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	lastFailure := firstFailure.Add(2 * time.Minute)
	lastSuccess := firstFailure.Add(-time.Hour)
	lastCompleted := lastFailure
	firstExternal := lastFailure.Add(time.Minute)
	lastAttempt := firstExternal.Add(5 * time.Minute)
	lastExternalSuccess := lastAttempt
	alertID := int64(42)
	want := &PollHealthState{
		Provider:                 "codex",
		AccountID:                "work",
		IntervalSeconds:          60,
		State:                    "failing",
		ConsecutiveFailures:      3,
		FirstFailureAt:           &firstFailure,
		LastFailureAt:            &lastFailure,
		LastSuccessAt:            &lastSuccess,
		LastCompletedPollAt:      &lastCompleted,
		LastErrorCategory:        "auth",
		LastErrorMessage:         "authentication failed",
		FirstExternalAlertAt:     &firstExternal,
		LastExternalAttemptAt:    &lastAttempt,
		LastExternalSuccessAt:    &lastExternalSuccess,
		ExternalFailureDelivered: true,
		ActiveSystemAlertID:      &alertID,
	}

	if err := s.UpsertPollHealthState(want); err != nil {
		t.Fatalf("UpsertPollHealthState: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s, err = New(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()

	got, err := s.GetPollHealthState("codex", "work")
	if err != nil {
		t.Fatalf("GetPollHealthState: %v", err)
	}
	assertPollHealthStateEqual(t, got, want)
}

func TestPollHealthStateUpsertUpdatesExistingRow(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	firstFailure := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	initial := &PollHealthState{
		Provider:            "anthropic",
		AccountID:           "default",
		IntervalSeconds:     300,
		State:               "failing",
		ConsecutiveFailures: 1,
		FirstFailureAt:      &firstFailure,
		LastFailureAt:       &firstFailure,
		LastErrorCategory:   "network",
		LastErrorMessage:    "request timed out",
	}
	if err := s.UpsertPollHealthState(initial); err != nil {
		t.Fatalf("initial UpsertPollHealthState: %v", err)
	}

	recoveredAt := firstFailure.Add(5 * time.Minute)
	updated := &PollHealthState{
		Provider:            "anthropic",
		AccountID:           "default",
		IntervalSeconds:     600,
		State:               "healthy",
		LastSuccessAt:       &recoveredAt,
		LastCompletedPollAt: &recoveredAt,
	}
	if err := s.UpsertPollHealthState(updated); err != nil {
		t.Fatalf("updated UpsertPollHealthState: %v", err)
	}

	got, err := s.GetPollHealthState("anthropic", "default")
	if err != nil {
		t.Fatalf("GetPollHealthState: %v", err)
	}
	assertPollHealthStateEqual(t, got, updated)
}

func TestPollHealthStateIsolatedByProviderAndAccount(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	states := []*PollHealthState{
		{Provider: "codex", AccountID: "work", State: "failing", ConsecutiveFailures: 2},
		{Provider: "codex", AccountID: "personal", State: "stalled", ConsecutiveFailures: 3},
		{Provider: "anthropic", AccountID: "default", State: "healthy"},
	}
	for _, state := range states {
		if err := s.UpsertPollHealthState(state); err != nil {
			t.Fatalf("UpsertPollHealthState(%s/%s): %v", state.Provider, state.AccountID, err)
		}
	}

	got, err := s.GetPollHealthState("codex", "personal")
	if err != nil {
		t.Fatalf("GetPollHealthState: %v", err)
	}
	assertPollHealthStateEqual(t, got, states[1])

	missing, err := s.GetPollHealthState("codex", "missing")
	if err != nil {
		t.Fatalf("GetPollHealthState missing: %v", err)
	}
	if missing != nil {
		t.Fatalf("missing state = %#v, want nil", missing)
	}

	unhealthy, err := s.ListUnhealthyPollHealthStates()
	if err != nil {
		t.Fatalf("ListUnhealthyPollHealthStates: %v", err)
	}
	if len(unhealthy) != 2 {
		t.Fatalf("len(unhealthy) = %d, want 2", len(unhealthy))
	}
	if unhealthy[0].Provider != "codex" || unhealthy[0].AccountID != "personal" {
		t.Fatalf("unhealthy[0] = %s/%s, want codex/personal", unhealthy[0].Provider, unhealthy[0].AccountID)
	}
	if unhealthy[1].Provider != "codex" || unhealthy[1].AccountID != "work" {
		t.Fatalf("unhealthy[1] = %s/%s, want codex/work", unhealthy[1].Provider, unhealthy[1].AccountID)
	}
}

func TestPollHealthUpdateSystemAlertKeepsID(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	id, err := s.CreateSystemAlert("codex", "poll_failure", "First failure", "count 1", "warning", `{"account_id":"work"}`)
	if err != nil {
		t.Fatalf("CreateSystemAlert: %v", err)
	}
	if err := s.UpdateSystemAlert(id, "Still failing", "count 3", "error", `{"account_id":"work","count":3}`); err != nil {
		t.Fatalf("UpdateSystemAlert: %v", err)
	}

	alerts, err := s.GetActiveSystemAlerts()
	if err != nil {
		t.Fatalf("GetActiveSystemAlerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("len(alerts) = %d, want 1", len(alerts))
	}
	got := alerts[0]
	if got.ID != id {
		t.Errorf("alert ID = %d, want %d", got.ID, id)
	}
	if got.Title != "Still failing" || got.Message != "count 3" || got.Severity != "error" {
		t.Errorf("updated alert = %#v", got)
	}
	if got.Metadata != `{"account_id":"work","count":3}` {
		t.Errorf("metadata = %q", got.Metadata)
	}
}

func TestPollHealthSystemAlertsRemainAccountIsolated(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	workID, err := s.CreateSystemAlert(
		"codex",
		"poll_failure",
		"Work failing",
		"work count 1",
		"warning",
		`{"account_id":"work"}`,
	)
	if err != nil {
		t.Fatalf("CreateSystemAlert(work): %v", err)
	}
	personalID, err := s.CreateSystemAlert(
		"codex",
		"poll_failure",
		"Personal failing",
		"personal count 1",
		"warning",
		`{"account_id":"personal"}`,
	)
	if err != nil {
		t.Fatalf("CreateSystemAlert(personal): %v", err)
	}

	if err := s.UpdateSystemAlert(
		workID,
		"Work still failing",
		"work count 3",
		"error",
		`{"account_id":"work","count":3}`,
	); err != nil {
		t.Fatalf("UpdateSystemAlert(work): %v", err)
	}
	if err := s.DismissSystemAlert(workID); err != nil {
		t.Fatalf("DismissSystemAlert(work): %v", err)
	}

	alerts, err := s.GetActiveSystemAlerts()
	if err != nil {
		t.Fatalf("GetActiveSystemAlerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("len(alerts) = %d, want 1", len(alerts))
	}
	got := alerts[0]
	if got.ID != personalID {
		t.Fatalf("active alert ID = %d, want personal ID %d", got.ID, personalID)
	}
	if got.Title != "Personal failing" ||
		got.Message != "personal count 1" ||
		got.Severity != "warning" ||
		got.Metadata != `{"account_id":"personal"}` {
		t.Errorf("personal alert changed: %#v", got)
	}

	var workDismissedAt *string
	if err := s.db.QueryRow(
		`SELECT dismissed_at FROM system_alerts WHERE id = ?`,
		workID,
	).Scan(&workDismissedAt); err != nil {
		t.Fatalf("query work dismissed_at: %v", err)
	}
	if workDismissedAt == nil || *workDismissedAt == "" {
		t.Error("work alert was not dismissed")
	}
}

func assertPollHealthStateEqual(t *testing.T, got, want *PollHealthState) {
	t.Helper()
	if got == nil {
		t.Fatal("state is nil")
	}
	if got.Provider != want.Provider ||
		got.AccountID != want.AccountID ||
		got.IntervalSeconds != want.IntervalSeconds ||
		got.State != want.State ||
		got.ConsecutiveFailures != want.ConsecutiveFailures ||
		got.LastErrorCategory != want.LastErrorCategory ||
		got.LastErrorMessage != want.LastErrorMessage ||
		got.ExternalFailureDelivered != want.ExternalFailureDelivered {
		t.Errorf("state = %#v, want %#v", got, want)
	}
	assertPollHealthTimeEqual(t, "FirstFailureAt", got.FirstFailureAt, want.FirstFailureAt)
	assertPollHealthTimeEqual(t, "LastFailureAt", got.LastFailureAt, want.LastFailureAt)
	assertPollHealthTimeEqual(t, "LastSuccessAt", got.LastSuccessAt, want.LastSuccessAt)
	assertPollHealthTimeEqual(t, "LastCompletedPollAt", got.LastCompletedPollAt, want.LastCompletedPollAt)
	assertPollHealthTimeEqual(t, "FirstExternalAlertAt", got.FirstExternalAlertAt, want.FirstExternalAlertAt)
	assertPollHealthTimeEqual(t, "LastExternalAttemptAt", got.LastExternalAttemptAt, want.LastExternalAttemptAt)
	assertPollHealthTimeEqual(t, "LastExternalSuccessAt", got.LastExternalSuccessAt, want.LastExternalSuccessAt)
	if !equalInt64Pointers(got.ActiveSystemAlertID, want.ActiveSystemAlertID) {
		t.Errorf("ActiveSystemAlertID = %v, want %v", got.ActiveSystemAlertID, want.ActiveSystemAlertID)
	}
}

func assertPollHealthTimeEqual(t *testing.T, name string, got, want *time.Time) {
	t.Helper()
	if got == nil || want == nil {
		if got != nil || want != nil {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
		return
	}
	if !got.Equal(*want) {
		t.Errorf("%s = %s, want %s", name, got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}

func equalInt64Pointers(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
