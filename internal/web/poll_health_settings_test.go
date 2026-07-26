package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/notify"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestUpdateSettingsPollHealthRoundTripAndReload(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	engine := notify.New(s, slog.Default())
	h := NewHandler(s, nil, nil, nil, createTestConfigWithSynthetic())
	h.SetNotifier(engine)

	body := strings.NewReader(`{"notifications":{"warning_threshold":80,"critical_threshold":95,"notify_poll_failure":true,"notify_auth_error":false,"poll_failure_threshold":4,"poll_failure_repeat_hours":8,"notify_poll_recovery":false,"cooldown_minutes":30}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", body)
	rr := httptest.NewRecorder()
	h.UpdateSettings(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	raw, err := s.GetSetting("notifications")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	var saved map[string]any
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		t.Fatalf("saved notifications JSON: %v", err)
	}
	if saved["notify_poll_failure"] != true {
		t.Errorf("notify_poll_failure = %v, want true", saved["notify_poll_failure"])
	}
	if saved["notify_auth_error"] != true {
		t.Errorf("legacy notify_auth_error = %v, want mirrored true", saved["notify_auth_error"])
	}
	if saved["poll_failure_threshold"] != float64(4) {
		t.Errorf("poll_failure_threshold = %v, want 4", saved["poll_failure_threshold"])
	}
	if saved["poll_failure_repeat_hours"] != float64(8) {
		t.Errorf("poll_failure_repeat_hours = %v, want 8", saved["poll_failure_repeat_hours"])
	}
	if saved["notify_poll_recovery"] != false {
		t.Errorf("notify_poll_recovery = %v, want false", saved["notify_poll_recovery"])
	}

	cfg := engine.Config()
	if !cfg.NotifyPollFailure || cfg.NotifyPollRecovery || cfg.PollFailureThreshold != 4 || cfg.PollFailureRepeat != 8*time.Hour {
		t.Fatalf("reloaded poll health config = %+v", cfg)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	getRR := httptest.NewRecorder()
	h.GetSettings(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", getRR.Code, getRR.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(getRR.Body.Bytes(), &response); err != nil {
		t.Fatalf("GET response JSON: %v", err)
	}
	notifications, ok := response["notifications"].(map[string]any)
	if !ok {
		t.Fatalf("notifications response = %#v", response["notifications"])
	}
	if notifications["poll_failure_threshold"] != float64(4) || notifications["poll_failure_repeat_hours"] != float64(8) {
		t.Fatalf("poll-health settings did not round trip: %#v", notifications)
	}
}

func TestUpdateSettingsPollHealthLegacyFallbackAndDefaults(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	h := NewHandler(s, nil, nil, nil, createTestConfigWithSynthetic())

	body := strings.NewReader(`{"notifications":{"warning_threshold":80,"critical_threshold":95,"notify_auth_error":false,"cooldown_minutes":30}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", body)
	rr := httptest.NewRecorder()
	h.UpdateSettings(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	raw, _ := s.GetSetting("notifications")
	var saved map[string]any
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		t.Fatalf("saved notifications JSON: %v", err)
	}
	if saved["notify_poll_failure"] != false || saved["notify_auth_error"] != false {
		t.Fatalf("legacy false migration = poll:%v auth:%v, want both false", saved["notify_poll_failure"], saved["notify_auth_error"])
	}
	if saved["poll_failure_threshold"] != float64(3) || saved["poll_failure_repeat_hours"] != float64(6) {
		t.Fatalf("poll health defaults not saved: %#v", saved)
	}
	if saved["notify_poll_recovery"] != true {
		t.Fatalf("notify_poll_recovery = %v, want true", saved["notify_poll_recovery"])
	}
}

func TestUpdateSettingsRejectsInvalidPollHealthSettings(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "failure threshold",
			body: `{"notifications":{"warning_threshold":80,"critical_threshold":95,"poll_failure_threshold":1,"cooldown_minutes":30}}`,
			want: "poll failure threshold must be at least 2",
		},
		{
			name: "repeat hours",
			body: `{"notifications":{"warning_threshold":80,"critical_threshold":95,"poll_failure_repeat_hours":0,"cooldown_minutes":30}}`,
			want: "poll failure repeat hours must be at least 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := store.New(":memory:")
			if err != nil {
				t.Fatalf("store.New: %v", err)
			}
			defer s.Close()
			h := NewHandler(s, nil, nil, nil, createTestConfigWithSynthetic())
			req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(tt.body))
			rr := httptest.NewRecorder()
			h.UpdateSettings(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tt.want) {
				t.Fatalf("body = %q, want %q", rr.Body.String(), tt.want)
			}
		})
	}
}

func TestPollHealthSettingsControlsAndClientMapping(t *testing.T) {
	templateData, err := os.ReadFile("templates/settings.html")
	if err != nil {
		t.Fatalf("ReadFile settings.html: %v", err)
	}
	templateText := string(templateData)
	for _, want := range []string{
		`id="notify-poll-failure"`,
		`id="poll-failure-threshold"`,
		`id="poll-failure-repeat-hours"`,
		`id="notify-poll-recovery"`,
	} {
		if !strings.Contains(templateText, want) {
			t.Errorf("settings.html missing %s", want)
		}
	}
	if strings.Contains(templateText, `id="notify-auth-error"`) {
		t.Error("settings.html still exposes the legacy auth-error control")
	}

	appData, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("ReadFile app.js: %v", err)
	}
	appText := string(appData)
	for _, want := range []string{
		"notify_poll_failure",
		"poll_failure_threshold",
		"poll_failure_repeat_hours",
		"notify_poll_recovery",
		"notify_auth_error",
	} {
		if !strings.Contains(appText, want) {
			t.Errorf("app.js missing %s mapping", want)
		}
	}
}
