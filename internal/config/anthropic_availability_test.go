package config

import "testing"

func TestAvailableProviders_AnthropicWithoutConfigToken(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"config token", Config{AnthropicToken: "token"}, true},
		{"isolated credential owner", Config{AnthropicCredentialOwner: true}, true},
		{"statusline source", Config{AnthropicSource: "statusline"}, true},
		{"nothing configured", Config{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := false
			for _, p := range tc.cfg.AvailableProviders() {
				if p == "anthropic" {
					got = true
				}
			}
			if got != tc.want {
				t.Fatalf("anthropic available = %v, want %v", got, tc.want)
			}
			if got != tc.cfg.HasProvider("anthropic") {
				t.Fatal("AvailableProviders and HasProvider disagree about anthropic")
			}
		})
	}
}
