package api

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// claudeCredentials represents the Claude Code credentials JSON structure.
type claudeCredentials struct {
	ClaudeAiOauth struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"` // Unix milliseconds
		// RefreshTokenExpiresAt is when the refresh token itself dies. Once it
		// passes, the profile is signed out for good and only an interactive
		// login brings it back, so onWatch warns before that happens.
		RefreshTokenExpiresAt int64    `json:"refreshTokenExpiresAt"` // Unix milliseconds
		Scopes                []string `json:"scopes"`
		SubscriptionType      string   `json:"subscriptionType"`
		RateLimitTier         string   `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
}

// AnthropicCredentials contains the parsed OAuth credentials with computed fields.
type AnthropicCredentials struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	ExpiresIn    time.Duration // time until expiry
	Scopes       []string

	// RefreshTokenExpiresAt is the refresh token's own expiry. Zero when the
	// credentials file does not carry it.
	RefreshTokenExpiresAt time.Time
}

// RefreshTokenExpiringSoon reports whether the refresh token dies within the
// given window. Unknown expiry (zero) is never "soon" - it must not raise a
// warning onWatch cannot substantiate.
func (c *AnthropicCredentials) RefreshTokenExpiringSoon(threshold time.Duration) bool {
	if c.RefreshTokenExpiresAt.IsZero() {
		return false
	}
	return time.Until(c.RefreshTokenExpiresAt) < threshold
}

// IsExpiringSoon returns true if the token expires within the given duration.
// Returns false if expiry is unknown (zero ExpiresAt) to avoid spurious refreshes.
func (c *AnthropicCredentials) IsExpiringSoon(threshold time.Duration) bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return c.ExpiresIn < threshold
}

// IsExpired returns true if the token has already expired.
func (c *AnthropicCredentials) IsExpired() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return c.ExpiresIn <= 0
}

// parseClaudeCredentials extracts the OAuth access token from Claude Code credentials JSON.
func parseClaudeCredentials(data []byte) (string, error) {
	var creds claudeCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", err
	}
	return creds.ClaudeAiOauth.AccessToken, nil
}

// parseFullClaudeCredentials extracts all OAuth fields from Claude Code credentials JSON.
func parseFullClaudeCredentials(data []byte) (*AnthropicCredentials, error) {
	var creds claudeCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}

	oauth := creds.ClaudeAiOauth
	if oauth.AccessToken == "" {
		return nil, nil // no credentials
	}

	var expiresAt time.Time
	var expiresIn time.Duration
	if oauth.ExpiresAt > 0 {
		// Convert expiresAt from Unix milliseconds to time.Time.
		expiresAt = time.UnixMilli(oauth.ExpiresAt)
		expiresIn = time.Until(expiresAt)
	}

	var refreshExpiresAt time.Time
	if oauth.RefreshTokenExpiresAt > 0 {
		refreshExpiresAt = time.UnixMilli(oauth.RefreshTokenExpiresAt)
	}

	return &AnthropicCredentials{
		AccessToken:           oauth.AccessToken,
		RefreshToken:          oauth.RefreshToken,
		ExpiresAt:             expiresAt,
		ExpiresIn:             expiresIn,
		Scopes:                oauth.Scopes,
		RefreshTokenExpiresAt: refreshExpiresAt,
	}, nil
}

// DetectAnthropicToken attempts to auto-detect the Anthropic OAuth token
// from the Claude Code credentials stored in the system keychain or file.
// Returns empty string if not found.
func DetectAnthropicToken(logger *slog.Logger) string {
	return detectAnthropicTokenPlatform(logger)
}

// DetectAnthropicCredentials attempts to auto-detect the full Anthropic OAuth credentials
// from the Claude Code credentials stored in the system keychain or file.
// Returns nil if not found.
func DetectAnthropicCredentials(logger *slog.Logger) *AnthropicCredentials {
	return detectAnthropicCredentialsPlatform(logger)
}

// DefaultClaudeConfigDir returns Claude Code's default profile directory,
// ignoring CLAUDE_CONFIG_DIR. Used to reach the main profile's credentials
// when an isolated profile is configured but unusable.
func DefaultClaudeConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// DetectAnthropicTokenInDir reads an unexpired OAuth access token from
// <dir>/.credentials.json. Returns "" when the file is missing, unreadable,
// signed out, or expired. This is a read-only view: it never rotates anything,
// so it is safe to point at a profile another application owns.
func DetectAnthropicTokenInDir(dir string) string {
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return ""
	}
	creds, err := parseFullClaudeCredentials(data)
	if err != nil || creds == nil || creds.AccessToken == "" {
		return ""
	}
	if creds.IsExpired() {
		return ""
	}
	return strings.TrimSpace(creds.AccessToken)
}
