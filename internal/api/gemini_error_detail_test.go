package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const geminiLicenseError = `{"error":{"code":403,"message":"You do not have a valid license of this product. (#3501)","status":"PERMISSION_DENIED"}}`

// A bare "gemini: forbidden" gives no way to tell an expired token from a
// retired product tier. Carry the API's own message into the error.
func TestGeminiClient_ForbiddenCarriesAPIMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(geminiLicenseError))
	}))
	defer server.Close()

	client := NewGeminiClient("token", nil, WithGeminiBaseURL(server.URL))

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"quotas", func() error { _, err := client.FetchQuotas(context.Background()); return err }},
		{"tier", func() error { _, err := client.FetchTier(context.Background()); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, ErrGeminiForbidden) {
				t.Fatalf("err = %v, want ErrGeminiForbidden", err)
			}
			if !strings.Contains(err.Error(), "valid license") {
				t.Fatalf("err = %v, want the API's own message", err)
			}
		})
	}
}

// An unauthorized response must stay distinguishable, since the agent refreshes
// the token on 401 but not on 403.
func TestGeminiClient_UnauthorizedStillMatches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid Credentials"}}`))
	}))
	defer server.Close()

	client := NewGeminiClient("token", nil, WithGeminiBaseURL(server.URL))
	_, err := client.FetchQuotas(context.Background())
	if !errors.Is(err, ErrGeminiUnauthorized) {
		t.Fatalf("err = %v, want ErrGeminiUnauthorized", err)
	}
	if !strings.Contains(err.Error(), "Invalid Credentials") {
		t.Fatalf("err = %v, want the API's own message", err)
	}
}

// A body that is not the usual error envelope must not produce a confusing
// error or panic.
func TestGeminiAPIErrorMessage_NonEnvelopeBody(t *testing.T) {
	if got := geminiAPIErrorMessage([]byte("<html>gateway error</html>")); got != "" {
		t.Fatalf("geminiAPIErrorMessage = %q, want empty for a non-envelope body", got)
	}
	if got := geminiAPIErrorMessage(nil); got != "" {
		t.Fatalf("geminiAPIErrorMessage = %q, want empty for an empty body", got)
	}
}
