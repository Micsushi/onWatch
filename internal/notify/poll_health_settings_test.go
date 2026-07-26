package notify

import (
	"log/slog"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestNotificationEngineReloadPollHealthSettings(t *testing.T) {
	tests := []struct {
		name           string
		settings       string
		wantFailure    bool
		wantRecovery   bool
		wantThreshold  int
		wantRepeatTime time.Duration
	}{
		{
			name:           "new settings",
			settings:       `{"notify_poll_failure":false,"poll_failure_threshold":5,"poll_failure_repeat_hours":9,"notify_poll_recovery":false}`,
			wantFailure:    false,
			wantRecovery:   false,
			wantThreshold:  5,
			wantRepeatTime: 9 * time.Hour,
		},
		{
			name:           "legacy enabled fallback",
			settings:       `{"notify_auth_error":true}`,
			wantFailure:    true,
			wantRecovery:   true,
			wantThreshold:  3,
			wantRepeatTime: 6 * time.Hour,
		},
		{
			name:           "legacy disabled fallback",
			settings:       `{"notify_auth_error":false}`,
			wantFailure:    false,
			wantRecovery:   true,
			wantThreshold:  3,
			wantRepeatTime: 6 * time.Hour,
		},
		{
			name:           "new value wins over legacy",
			settings:       `{"notify_poll_failure":true,"notify_auth_error":false}`,
			wantFailure:    true,
			wantRecovery:   true,
			wantThreshold:  3,
			wantRepeatTime: 6 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := store.New(":memory:")
			if err != nil {
				t.Fatalf("store.New: %v", err)
			}
			defer s.Close()
			if err := s.SetSetting("notifications", tt.settings); err != nil {
				t.Fatalf("SetSetting: %v", err)
			}

			engine := New(s, slog.Default())
			if err := engine.Reload(); err != nil {
				t.Fatalf("Reload: %v", err)
			}
			cfg := engine.Config()
			if cfg.NotifyPollFailure != tt.wantFailure {
				t.Errorf("NotifyPollFailure = %v, want %v", cfg.NotifyPollFailure, tt.wantFailure)
			}
			if cfg.NotifyPollRecovery != tt.wantRecovery {
				t.Errorf("NotifyPollRecovery = %v, want %v", cfg.NotifyPollRecovery, tt.wantRecovery)
			}
			if cfg.PollFailureThreshold != tt.wantThreshold {
				t.Errorf("PollFailureThreshold = %d, want %d", cfg.PollFailureThreshold, tt.wantThreshold)
			}
			if cfg.PollFailureRepeat != tt.wantRepeatTime {
				t.Errorf("PollFailureRepeat = %v, want %v", cfg.PollFailureRepeat, tt.wantRepeatTime)
			}
		})
	}
}

func TestNotificationEnginePollHealthDefaults(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	cfg := New(s, slog.Default()).Config()
	if !cfg.NotifyPollFailure || !cfg.NotifyPollRecovery {
		t.Fatalf("poll health defaults = failure:%v recovery:%v, want both true", cfg.NotifyPollFailure, cfg.NotifyPollRecovery)
	}
	if cfg.PollFailureThreshold != 3 {
		t.Fatalf("PollFailureThreshold = %d, want 3", cfg.PollFailureThreshold)
	}
	if cfg.PollFailureRepeat != 6*time.Hour {
		t.Fatalf("PollFailureRepeat = %v, want 6h", cfg.PollFailureRepeat)
	}
}
