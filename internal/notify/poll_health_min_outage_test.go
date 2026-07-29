package notify

import (
	"testing"
	"time"
)

// A short outage should be visible in the app immediately but must not reach
// email, push, or Discord. Only a sustained outage is worth interrupting
// someone for.
func TestPollFailureMinOutageSuppressesShortOutage(t *testing.T) {
	engine, s, now, capture := newPollHealthEngine(t)
	engine.cfg.PollFailureMinOutage = 30 * time.Minute

	for i := 0; i < 5; i++ {
		engine.RecordPollFailure("codex", "acct-1", "provider_request", "unreachable")
		*now = now.Add(time.Minute)
	}

	if capture.count() != 0 {
		t.Fatalf("expected no external delivery inside the minimum outage window, got %d", capture.count())
	}

	state := pollState(t, s, "codex", "acct-1")
	if state.ActiveSystemAlertID == nil {
		t.Fatal("expected the in-app alert to be raised immediately")
	}
	if state.ExternalFailureDelivered {
		t.Fatal("expected no external delivery to be recorded")
	}
}

// Once the outage outlives the window, the alert goes out.
func TestPollFailureMinOutageDeliversSustainedOutage(t *testing.T) {
	engine, _, now, capture := newPollHealthEngine(t)
	engine.cfg.PollFailureMinOutage = 30 * time.Minute

	for i := 0; i < 3; i++ {
		engine.RecordPollFailure("codex", "acct-1", "provider_request", "unreachable")
		*now = now.Add(time.Minute)
	}
	if capture.count() != 0 {
		t.Fatalf("expected suppression before the window elapses, got %d", capture.count())
	}

	*now = now.Add(31 * time.Minute)
	engine.RecordPollFailure("codex", "acct-1", "provider_request", "unreachable")

	if capture.count() != 1 {
		t.Fatalf("expected exactly one external delivery after the window, got %d", capture.count())
	}
}

// The window is a floor on outage duration, not a replacement for the failure
// count: both must be satisfied.
func TestPollFailureMinOutageStillRequiresThreshold(t *testing.T) {
	engine, _, now, capture := newPollHealthEngine(t)
	engine.cfg.PollFailureMinOutage = time.Minute
	engine.cfg.PollFailureThreshold = 4

	engine.RecordPollFailure("codex", "acct-1", "provider_request", "unreachable")
	*now = now.Add(10 * time.Minute)
	engine.RecordPollFailure("codex", "acct-1", "provider_request", "unreachable")

	if capture.count() != 0 {
		t.Fatalf("expected the failure count to still gate delivery, got %d", capture.count())
	}
}

// A zero value must not silently disable external alerts for existing installs.
func TestPollFailureMinOutageZeroKeepsLegacyBehaviour(t *testing.T) {
	engine, _, now, capture := newPollHealthEngine(t)
	engine.cfg.PollFailureMinOutage = 0

	for i := 0; i < 3; i++ {
		engine.RecordPollFailure("codex", "acct-1", "provider_request", "unreachable")
		*now = now.Add(time.Second)
	}

	if capture.count() != 1 {
		t.Fatalf("expected delivery at the threshold with no window configured, got %d", capture.count())
	}
}
