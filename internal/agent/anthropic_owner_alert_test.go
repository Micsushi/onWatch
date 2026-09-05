package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/notify"
)

// ownerAlertSpy captures the auth alerts an expiring isolated profile raises.
type ownerAlertSpy struct {
	pollHealthNotifierSpy
	alerts []notify.AuthErrorAlert
}

func (s *ownerAlertSpy) SendAuthErrorNotification(alert notify.AuthErrorAlert) bool {
	s.alerts = append(s.alerts, alert)
	return true
}

// The dashboard banner is only seen by someone already looking at the dashboard.
// The whole point of the 72h warning is to reach the user before the profile
// dies, so it has to leave the browser.
func TestAnthropicAgent_NotifiesBeforeCredentialOwnerLoginExpires(t *testing.T) {
	creds := &api.AnthropicCredentials{
		AccessToken:           "owner-token",
		RefreshToken:          "owner-refresh",
		ExpiresAt:             time.Now().Add(time.Hour),
		ExpiresIn:             time.Hour,
		RefreshTokenExpiresAt: time.Now().Add(60 * time.Hour),
	}
	agent, _ := anthropicOwnerAgent(t, creds)
	spy := &ownerAlertSpy{}
	agent.notifier = spy

	agent.poll(context.Background())

	if len(spy.alerts) != 1 {
		t.Fatalf("auth alerts = %d, want 1 before the login expires", len(spy.alerts))
	}
	alert := spy.alerts[0]
	if alert.Provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", alert.Provider)
	}
	if alert.IsRecovable {
		t.Fatal("alert marked recoverable, but only an interactive login fixes it")
	}
	if !strings.Contains(alert.Message, "/login") {
		t.Fatalf("message = %q, want the login instruction", alert.Message)
	}
	if !strings.Contains(alert.Message, `C:\profiles\onwatch`) {
		t.Fatalf("message = %q, want the profile named", alert.Message)
	}
}

// Polling runs every few minutes. Re-alerting on every poll would bury the
// warning in noise and train the user to ignore it.
func TestAnthropicAgent_CredentialOwnerAlertDoesNotRepeatEveryPoll(t *testing.T) {
	creds := &api.AnthropicCredentials{
		AccessToken:           "owner-token",
		RefreshToken:          "owner-refresh",
		ExpiresAt:             time.Now().Add(time.Hour),
		ExpiresIn:             time.Hour,
		RefreshTokenExpiresAt: time.Now().Add(60 * time.Hour),
	}
	agent, _ := anthropicOwnerAgent(t, creds)
	spy := &ownerAlertSpy{}
	agent.notifier = spy

	agent.poll(context.Background())
	agent.poll(context.Background())
	agent.poll(context.Background())

	if len(spy.alerts) != 1 {
		t.Fatalf("auth alerts = %d, want 1 across repeated polls", len(spy.alerts))
	}
}

// A healthy login must stay silent, or the warning means nothing.
func TestAnthropicAgent_NoCredentialOwnerAlertWhenLoginIsHealthy(t *testing.T) {
	creds := &api.AnthropicCredentials{
		AccessToken:           "owner-token",
		RefreshToken:          "owner-refresh",
		ExpiresAt:             time.Now().Add(time.Hour),
		ExpiresIn:             time.Hour,
		RefreshTokenExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}
	agent, _ := anthropicOwnerAgent(t, creds)
	spy := &ownerAlertSpy{}
	agent.notifier = spy

	agent.poll(context.Background())

	if len(spy.alerts) != 0 {
		t.Fatalf("auth alerts = %d, want none for a healthy login", len(spy.alerts))
	}
}
