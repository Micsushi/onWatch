package api

import (
	"strconv"
	"testing"
	"time"
)

// The credentials file records when the refresh token itself dies. onWatch needs
// that date to warn before the isolated profile is signed out for good.
func TestParseFullClaudeCredentials_ReadsRefreshTokenExpiry(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).UnixMilli()
	refreshExpiresAt := time.Now().Add(48 * time.Hour).UnixMilli()
	data := []byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":` +
		strconv.FormatInt(expiresAt, 10) + `,"refreshTokenExpiresAt":` + strconv.FormatInt(refreshExpiresAt, 10) + `}}`)

	creds, err := parseFullClaudeCredentials(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if creds == nil {
		t.Fatal("credentials = nil, want parsed credentials")
	}
	if got := creds.RefreshTokenExpiresAt.UnixMilli(); got != refreshExpiresAt {
		t.Fatalf("RefreshTokenExpiresAt = %d, want %d", got, refreshExpiresAt)
	}
	if !creds.RefreshTokenExpiringSoon(72 * time.Hour) {
		t.Fatal("RefreshTokenExpiringSoon(72h) = false, want true for a 48h expiry")
	}
	if creds.RefreshTokenExpiringSoon(time.Hour) {
		t.Fatal("RefreshTokenExpiringSoon(1h) = true, want false for a 48h expiry")
	}
}

// Older credentials files omit the field. An unknown expiry must never raise a
// warning onWatch cannot substantiate.
func TestParseFullClaudeCredentials_MissingRefreshTokenExpiry(t *testing.T) {
	data := []byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":` +
		strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10) + `}}`)

	creds, err := parseFullClaudeCredentials(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !creds.RefreshTokenExpiresAt.IsZero() {
		t.Fatalf("RefreshTokenExpiresAt = %v, want zero", creds.RefreshTokenExpiresAt)
	}
	if creds.RefreshTokenExpiringSoon(365 * 24 * time.Hour) {
		t.Fatal("RefreshTokenExpiringSoon = true for an unknown expiry")
	}
}

// "exit status 1" says nothing. The CLI's own last line does.
func TestClaudeRefreshDetail(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{"empty", "", ""},
		{"blank lines only", "\n \r\n", ""},
		{
			"last line wins over the warning preamble",
			"Ignoring 88 permissions.allow entries\r\nFailed to authenticate: OAuth session expired\r\n",
			" (Failed to authenticate: OAuth session expired)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claudeRefreshDetail([]byte(tt.stderr)); got != tt.want {
				t.Fatalf("claudeRefreshDetail = %q, want %q", got, tt.want)
			}
		})
	}
}

// A chatty CLI must not grow the agent's memory footprint.
func TestBoundedBuffer_CapsRetainedOutput(t *testing.T) {
	var b boundedBuffer
	chunk := make([]byte, 3000)
	for i := range chunk {
		chunk[i] = 'x'
	}
	for i := 0; i < 5; i++ {
		n, err := b.Write(chunk)
		if err != nil || n != len(chunk) {
			t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(chunk))
		}
	}
	if got := len(b.Bytes()); got != maxCredentialRefreshStderr {
		t.Fatalf("retained %d bytes, want %d", got, maxCredentialRefreshStderr)
	}
}
