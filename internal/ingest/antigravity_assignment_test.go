package ingest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAntigravityAssignmentBinding(t *testing.T) {
	var legacy DesiredConfig
	if err := json.Unmarshal([]byte(`{"assignments":[{"provider":"antigravity","external_id":"opaque-42","poll_interval":"1m"}]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDesiredConfig(legacy); err != nil {
		t.Fatalf("legacy config rejected: %v", err)
	}
	if legacy.Assignments[0].ValidateAntigravityBinding() == nil {
		t.Fatal("legacy missing binding permits polling")
	}
	for _, tc := range []struct {
		name, provider, email string
		invalid               bool
	}{
		{"plain", "antigravity", "synthetic@example.invalid", false},
		{"spaces", "antigravity", " synthetic@example.invalid ", false},
		{"display name", "antigravity", "Name <synthetic@example.invalid>", true},
		{"angle address", "antigravity", "<synthetic@example.invalid>", true},
		{"newline", "antigravity", "synthetic@example.invalid\n", true},
		{"tab", "antigravity", "\tsynthetic@example.invalid", true},
		{"control", "antigravity", "synthetic\x7f@example.invalid", true},
		{"long", "antigravity", strings.Repeat("a", 250) + "@example.invalid", true},
		{"not address", "antigravity", "opaque-id", true},
		{"blank supplied", "antigravity", "  ", true},
		{"other provider bound", "codex", "synthetic@example.invalid", true},
		{"other provider unchanged", "codex", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := DesiredConfig{Assignments: []ProviderAssignment{{Provider: tc.provider, ExternalID: "opaque-42", PollInterval: "1m", AntigravityAccountEmail: tc.email}}}
			err := ValidateDesiredConfig(config)
			if (err != nil) != tc.invalid {
				t.Fatalf("err=%v invalid=%v", err, tc.invalid)
			}
			if err != nil && strings.Contains(err.Error(), "@") {
				t.Fatal("validation error leaks email")
			}
		})
	}
	config := DesiredConfig{Assignments: []ProviderAssignment{
		{Provider: "antigravity", ExternalID: "one", PollInterval: "1m", AntigravityAccountEmail: "synthetic@example.invalid"},
		{Provider: "antigravity", ExternalID: "two", PollInterval: "1m", AntigravityAccountEmail: " synthetic@example.invalid "},
	}}
	if err := ValidateDesiredConfig(config); err == nil || err.Error() != "duplicate_antigravity_account_email" {
		t.Fatalf("duplicate binding accepted: %v", err)
	}
	config.Assignments[1].AntigravityAccountEmail = "second@example.invalid"
	if err := ValidateDesiredConfig(config); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DesiredConfig
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Assignments[0].AntigravityAccountEmail != "synthetic@example.invalid" || decoded.Assignments[0].ExternalID != "one" {
		t.Fatal("binding or opaque ID lost in desired config round trip")
	}
}
