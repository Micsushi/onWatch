package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/agent"
)

func TestAntigravityWakeStatus(t *testing.T) {
	t.Run("nil runner", func(t *testing.T) {
		h := NewHandler(nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodGet, "/api/antigravity/wake", nil)
		rec := httptest.NewRecorder()

		h.AntigravityWakeStatus(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		var resp map[string]interface{}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		if resp["configured"] != false || resp["enabled"] != false {
			t.Errorf("expected configured=false, enabled=false, got %+v", resp)
		}
	})

	t.Run("configured runner", func(t *testing.T) {
		h := NewHandler(nil, nil, nil, nil, nil)
		runner := agent.NewAntigravityWakeRunner(nil, agent.AntigravityWakeConfig{
			Enabled:     true,
			Mode:        "new-conversation",
			RecipientID: "target-123",
			Model:       "flash_lite",
			Prompt:      "hi",
			Title:       "Wake Title",
			Cooldown:    10 * time.Minute,
		}, nil)
		h.SetAntigravityWakeRunner(runner)

		req := httptest.NewRequest(http.MethodGet, "/api/antigravity/wake", nil)
		rec := httptest.NewRecorder()

		h.AntigravityWakeStatus(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		var resp map[string]interface{}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		if resp["configured"] != true || resp["enabled"] != true {
			t.Errorf("expected configured=true, enabled=true, got %+v", resp)
		}
		if resp["mode"] != "new-conversation" {
			t.Errorf("expected mode 'new-conversation', got %v", resp["mode"])
		}
		if resp["recipient_id"] != "target-123" {
			t.Errorf("expected recipient_id 'target-123', got %v", resp["recipient_id"])
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		h := NewHandler(nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/antigravity/wake", nil)
		rec := httptest.NewRecorder()

		h.AntigravityWakeStatus(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected status 405, got %d", rec.Code)
		}
	})
}

func TestAntigravityWakeTrigger(t *testing.T) {
	t.Run("nil runner", func(t *testing.T) {
		h := NewHandler(nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/antigravity/wake/trigger", nil)
		rec := httptest.NewRecorder()

		h.AntigravityWakeTrigger(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		h := NewHandler(nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodGet, "/api/antigravity/wake/trigger", nil)
		rec := httptest.NewRecorder()

		h.AntigravityWakeTrigger(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected status 405, got %d", rec.Code)
		}
	})

	t.Run("trigger success", func(t *testing.T) {
		h := NewHandler(nil, nil, nil, nil, nil)
		runner := agent.NewAntigravityWakeRunner(nil, agent.AntigravityWakeConfig{
			Enabled:  true,
			Mode:     "new-conversation",
			Prompt:   "hi",
			Cooldown: 50 * time.Millisecond,
		}, nil)
		runner.SetPathResolver(func(override string) (string, bool, error) {
			return "agentapi", false, nil
		})
		runner.SetExecutor(func(ctx context.Context, name string, env []string, args ...string) ([]byte, error) {
			return []byte(`{"conversationId":"conv-trigger-1"}`), nil
		})
		h.SetAntigravityWakeRunner(runner)

		req := httptest.NewRequest(http.MethodPost, "/api/antigravity/wake/trigger", nil)
		rec := httptest.NewRecorder()

		h.AntigravityWakeTrigger(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var resp map[string]interface{}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		if resp["success"] != true {
			t.Errorf("expected success=true, got %+v", resp)
		}
		resultMap, ok := resp["result"].(map[string]interface{})
		if !ok || resultMap["conversation_id"] != "conv-trigger-1" {
			t.Errorf("expected conversation_id 'conv-trigger-1', got %+v", resp)
		}
	})

	t.Run("trigger failure", func(t *testing.T) {
		h := NewHandler(nil, nil, nil, nil, nil)
		runner := agent.NewAntigravityWakeRunner(nil, agent.AntigravityWakeConfig{
			Enabled:  true,
			Mode:     "new-conversation",
			Prompt:   "hi",
			Cooldown: 50 * time.Millisecond,
		}, nil)
		runner.SetPathResolver(func(override string) (string, bool, error) {
			return "agentapi", false, nil
		})
		runner.SetExecutor(func(ctx context.Context, name string, env []string, args ...string) ([]byte, error) {
			return []byte("process failed"), errors.New("exit code 1")
		})
		h.SetAntigravityWakeRunner(runner)

		req := httptest.NewRequest(http.MethodPost, "/api/antigravity/wake/trigger", nil)
		rec := httptest.NewRecorder()

		h.AntigravityWakeTrigger(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected status 500, got %d", rec.Code)
		}
	})
}
