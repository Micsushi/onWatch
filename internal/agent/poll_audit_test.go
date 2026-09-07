package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestAuditCodexOwnerIdentityAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	p := CodexProfile{CredentialHome: dir, AccountID: "expected"}
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"tokens":{"account_id":"wrong","access_token":"token","refresh_token":"never-use"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if readCodexOwnedCredentials(p) != nil {
		t.Fatal("accepted another account")
	}
	if err := os.WriteFile(path, []byte(`{"tokens":{"account_id":"expected","access_token":"token","refresh_token":"never-use"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	creds := readCodexOwnedCredentials(p)
	if creds == nil || creds.AccessToken != "token" || creds.RefreshToken != "" {
		t.Fatal("owner credentials must be read-only")
	}
}

func TestAuditCodexDoesNotRotateSharedCredentials(t *testing.T) {
	a, _, _ := setupCodexTest(t)
	a.credsRefresh = func() *api.CodexCredentials {
		return &api.CodexCredentials{AccessToken: "oauth_token", RefreshToken: "shared", ExpiresAt: time.Now().Add(-time.Hour), ExpiresIn: -time.Hour}
	}
	a.refreshRequest = func(context.Context, string) (*api.CodexOAuthTokenResponse, error) {
		t.Fatal("rotated shared credential")
		return nil, nil
	}
	a.poll(context.Background())
}

func TestAuditCodexProfileUsesAccessExpiry(t *testing.T) {
	jwt := func(exp time.Time) string {
		return "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + ".sig"
	}
	var p CodexProfile
	p.Tokens.AccessToken = jwt(time.Now().Add(48 * time.Hour))
	p.Tokens.IDToken = jwt(time.Now().Add(-time.Hour))
	if codexCredentialsFromProfile(p).IsExpiringSoon(codexTokenRefreshThreshold) {
		t.Fatal("valid access token treated as expired ID token")
	}
}

func TestAuditGeminiKeepsRefreshedToken(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var staleRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			staleRequests++
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"buckets":[{"modelId":"gemini","remainingFraction":0.9}]}`)
	}))
	defer srv.Close()
	a := NewGeminiAgent(api.NewGeminiClient("old", nil, api.WithGeminiBaseURL(srv.URL)), st, nil, time.Minute, nil, nil)
	a.tierFetched = true
	a.credsRefresh = func() *api.GeminiCredentials {
		return &api.GeminiCredentials{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour), ExpiresIn: -time.Hour}
	}
	a.clientCreds = &api.GeminiClientCredentials{ClientID: "test", ClientSecret: "test"}
	a.refreshRequest = func(context.Context, string, string, string) (*api.GeminiOAuthTokenResponse, error) {
		return &api.GeminiOAuthTokenResponse{AccessToken: "fresh", ExpiresIn: 3600}, nil
	}
	a.poll(context.Background())
	if staleRequests != 0 {
		t.Fatalf("sent %d requests with stale token after refresh", staleRequests)
	}
}
