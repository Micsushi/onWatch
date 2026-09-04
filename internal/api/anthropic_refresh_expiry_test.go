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

// "exit status 1" says nothing. The probe runs with --output-format json, so the
// reason is the envelope's "result" field on stdout; stderr only carries
// unrelated workspace-trust warnings.
func TestClaudeRefreshDetail(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		stderr string
		want   string
	}{
		{name: "empty"},
		{name: "blank lines only", stdout: "\n \r\n"},
		{
			name:   "stdout reason wins over stderr noise",
			stdout: "Failed to authenticate: OAuth session expired\r\n",
			stderr: "Ignoring 88 permissions.allow entries\r\n",
			want:   " (Failed to authenticate: OAuth session expired)",
		},
		{
			name:   "falls back to stderr when stdout is silent",
			stderr: "claude: command failed\n",
			want:   " (claude: command failed)",
		},
		{
			name:   "json envelope reports its result field, not the raw blob",
			stdout: `{"is_error":true,"session_id":"abc","result":"Failed to authenticate: OAuth session expired and could not be refreshed","type":"result"}`,
			stderr: "Ignoring 88 permissions.allow entries\r\n",
			want:   " (Failed to authenticate: OAuth session expired and could not be refreshed)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claudeRefreshDetail([]byte(tt.stdout), []byte(tt.stderr)); got != tt.want {
				t.Fatalf("claudeRefreshDetail = %q, want %q", got, tt.want)
			}
		})
	}
}

// A chatty CLI must not grow the agent's memory footprint.
func TestBoundedBuffer_CapsRetainedOutput(t *testing.T) {
	var b boundedBuffer
	chunk := make([]byte, maxCredentialRefreshOutput/2)
	for i := range chunk {
		chunk[i] = 'x'
	}
	for i := 0; i < 5; i++ {
		n, err := b.Write(chunk)
		if err != nil || n != len(chunk) {
			t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(chunk))
		}
	}
	if got := len(b.Bytes()); got != maxCredentialRefreshOutput {
		t.Fatalf("retained %d bytes, want %d", got, maxCredentialRefreshOutput)
	}
}
