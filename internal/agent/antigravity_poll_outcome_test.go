package agent

import (
	"errors"
	"fmt"
	"testing"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

// Antigravity is a local-only provider. A closed IDE is an expected absence,
// not an outage, so it must not open a poll-failure incident that can only be
// resolved by reopening the IDE.
func TestAntigravityPollOutcome_ProcessNotFoundIsSkipped(t *testing.T) {
	outcome := antigravityPollOutcome(api.ErrAntigravityProcessNotFound)
	if !outcome.skip {
		t.Fatal("expected a not-running provider to be recorded as skipped")
	}
}

func TestAntigravityPollOutcome_WrappedProcessNotFoundIsSkipped(t *testing.T) {
	err := fmt.Errorf("antigravity: detect: %w", api.ErrAntigravityProcessNotFound)
	if !antigravityPollOutcome(err).skip {
		t.Fatal("expected wrapped not-found error to be recorded as skipped")
	}
}

// A running IDE that cannot be reached is a real fault and must still alert.
func TestAntigravityPollOutcome_ReachabilityErrorsStayFailures(t *testing.T) {
	for _, err := range []error{
		api.ErrAntigravityPortNotFound,
		api.ErrAntigravityConnectionFailed,
		api.ErrAntigravityNotAuthenticated,
		errors.New("boom"),
	} {
		outcome := antigravityPollOutcome(err)
		if outcome.skip {
			t.Fatalf("expected %v to remain a failure", err)
		}
		if outcome.category == "" || outcome.message == "" {
			t.Fatalf("expected a category and message for %v", err)
		}
	}
}
