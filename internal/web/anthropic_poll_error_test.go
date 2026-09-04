package web

import (
	"testing"

	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestBuildAnthropicCurrent_ExposesPollError(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	h := NewHandler(s, nil, nil, nil, createTestConfigWithSynthetic())

	if _, ok := h.buildAnthropicCurrent()["pollError"]; ok {
		t.Fatal("pollError present with no recorded failure")
	}

	raw := `{"category":"authentication","message":"Claude credentials expired.","at":"2026-09-03T18:00:00Z"}`
	if err := s.SetSetting(store.AnthropicPollErrorSetting, raw); err != nil {
		t.Fatalf("set setting: %v", err)
	}

	pollError, ok := h.buildAnthropicCurrent()["pollError"].(map[string]interface{})
	if !ok {
		t.Fatal("pollError missing after a recorded failure")
	}
	if pollError["category"] != "authentication" {
		t.Fatalf("category = %v, want authentication", pollError["category"])
	}
	if pollError["message"] != "Claude credentials expired." {
		t.Fatalf("message = %v", pollError["message"])
	}
	if pollError["at"] != "2026-09-03T18:00:00Z" {
		t.Fatalf("at = %v", pollError["at"])
	}
}

func TestBuildAnthropicCurrent_IgnoresCorruptPollError(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	h := NewHandler(s, nil, nil, nil, createTestConfigWithSynthetic())
	if err := s.SetSetting(store.AnthropicPollErrorSetting, "not-json"); err != nil {
		t.Fatalf("set setting: %v", err)
	}

	if _, ok := h.buildAnthropicCurrent()["pollError"]; ok {
		t.Fatal("corrupt pollError should be ignored")
	}
}
