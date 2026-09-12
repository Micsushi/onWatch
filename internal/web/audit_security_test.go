package web

import (
	"encoding/json"
	"github.com/onllm-dev/onwatch/v2/internal/menubar"
	"github.com/onllm-dev/onwatch/v2/internal/notify"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuditBasicAuthSharesLoginBlockAndIgnoresSpoofedIP(t *testing.T) {
	sessions := NewSessionStore("admin", legacyHashPassword("secret"), nil)
	for range maxFailedAttempts {
		sessions.limiter.RecordFailure("127.0.0.1")
	}
	next := SessionAuthMiddleware(sessions)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("blocked request reached handler") }))
	req := httptest.NewRequest("GET", "/api/current", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	req.SetBasicAuth("admin", "secret")
	out := httptest.NewRecorder()
	next.ServeHTTP(out, req)
	if out.Code != 429 {
		t.Fatalf("got %d", out.Code)
	}
}
func TestAuditMenubarRequiresScopedCredential(t *testing.T) {
	sessions := NewSessionStore("admin", legacyHashPassword("secret"), nil)
	next := SessionAuthMiddleware(sessions)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for _, tc := range []struct {
		path, token string
		want        int
	}{{"/api/menubar/preferences", "", 401}, {"/api/menubar/preferences", "wrong", 401}, {"/api/menubar/preferences", menubar.LocalToken(sessions.PasswordHash()), 200}, {"/api/current", menubar.LocalToken(sessions.PasswordHash()), 401}} {
		req := httptest.NewRequest("GET", tc.path, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Onwatch-Menubar", tc.token)
		out := httptest.NewRecorder()
		next.ServeHTTP(out, req)
		if out.Code != tc.want {
			t.Fatalf("%s got %d want %d", tc.path, out.Code, tc.want)
		}
	}
}

type auditNotifier struct {
	mockNotifier
	key string
}

func (n *auditNotifier) SetEncryptionKey(key string) { n.key = key }
func TestAuditPasswordRotationUpdatesNotificationKey(t *testing.T) {
	db, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := legacyHashPassword("oldpass")
	if err := db.UpsertUser("admin", old); err != nil {
		t.Fatal(err)
	}
	cipher, err := notify.Encrypt("fixture-password", DeriveEncryptionKey(old, nil))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"password": cipher})
	if err := db.SetSetting("smtp", string(payload)); err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore("admin", old, db)
	h := NewHandler(db, nil, nil, sessions, createTestConfigWithSynthetic())
	n := &auditNotifier{}
	h.SetNotifier(n)
	req := httptest.NewRequest("PUT", "/api/password", strings.NewReader(`{"current_password":"oldpass","new_password":"newpassword"}`))
	out := httptest.NewRecorder()
	h.ChangePassword(out, req)
	if out.Code != 200 {
		t.Fatalf("%d %s", out.Code, out.Body.String())
	}
	value, err := db.GetSetting("smtp")
	if err != nil {
		t.Fatal(err)
	}
	var smtp map[string]string
	if err := json.Unmarshal([]byte(value), &smtp); err != nil {
		t.Fatal(err)
	}
	plain, err := notify.Decrypt(smtp["password"], n.key)
	if err != nil || plain != "fixture-password" || !n.reloadCalled {
		t.Fatalf("notification rotation failed: %v", err)
	}
}
