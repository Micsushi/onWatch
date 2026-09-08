package web

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestBackupRestorePreservesAuthenticationAndRevocation(t *testing.T) {
	dir := t.TempDir()
	live, err := store.New(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	device, token, err := live.CreateDevice("restore-active", "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	revoked, revokedToken, err := live.CreateDevice("restore-revoked", "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if err := live.RevokeDevice(revoked.ID); err != nil {
		t.Fatal(err)
	}
	if err := live.SetSetting("restore-drill", "preserved"); err != nil {
		t.Fatal(err)
	}
	hash, err := HashPassword("synthetic-restore-password")
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore("restore-admin", hash, live)
	validSession, ok := sessions.Authenticate("restore-admin", "synthetic-restore-password")
	if !ok {
		t.Fatal("seed login failed")
	}
	invalidSession, ok := sessions.Authenticate("restore-admin", "synthetic-restore-password")
	if !ok {
		t.Fatal("seed revoked session failed")
	}
	sessions.Invalidate(invalidSession)
	backup := filepath.Join(dir, "snapshot.db")
	metadata, err := live.Backup(backup)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Size != int64(len(data)) || metadata.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatal("backup metadata does not match restored bytes")
	}
	restored, err := store.New(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.Ready(); err != nil {
		t.Fatal(err)
	}
	if value, err := restored.GetSetting("restore-drill"); err != nil || value != "preserved" {
		t.Fatalf("restored setting missing: %v", err)
	}
	if _, err := restored.AuthenticateDevice(device.ID, token); err != nil {
		t.Fatal("active device credential lost")
	}
	if _, err := restored.AuthenticateDevice(revoked.ID, revokedToken); err == nil {
		t.Fatal("revoked device credential restored as active")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(restored, nil, logger, nil, createTestConfigWithSynthetic())
	server := NewServer(9211, handler, logger, "restore-admin", hash, "127.0.0.1", "", "")
	for _, tc := range []struct {
		name, session string
		status        int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"invalidated", invalidSession, http.StatusUnauthorized},
		{"valid persisted session", validSession, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
			if tc.session != "" {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tc.session})
			}
			rr := httptest.NewRecorder()
			server.httpServer.Handler.ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("status=%d want=%d", rr.Code, tc.status)
			}
			if tc.status == http.StatusOK && (!strings.Contains(rr.Body.String(), device.ID) || !strings.Contains(rr.Body.String(), revoked.ID)) {
				t.Fatal("restored device list incomplete")
			}
		})
	}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=restore-admin&password=synthetic-restore-password"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound || len(rr.Result().Cookies()) == 0 {
		t.Fatal("fresh restored-server login failed")
	}
}
