package notify

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

const testWebhookToken = "oU0ky4SlchXuapPYbeu9aAMnWw2R6zBYR3ka2Zoj6wncWt"

var errNoSuchHost = errors.New("dial tcp: lookup discord.com: no such host")

// Transport errors from net/http embed the request URL, and callers log the
// error verbatim. For a Discord webhook the URL is the credential, so it must
// never reach the log.
func TestDiscordSender_SendContext_RedactsWebhookOnTransportError(t *testing.T) {
	sender := &DiscordSender{
		webhookURL: "https://discord.com/api/webhooks/1506031047236649005/" + testWebhookToken,
		client:     &http.Client{Transport: failingTransport{}},
	}

	err := sender.Send("subject", "body")
	if err == nil {
		t.Fatal("expected an error")
	}
	assertWebhookRedacted(t, err.Error())
}

// url.Parse failures also quote the whole URL back to the caller.
func TestDiscordSender_SendContext_RedactsWebhookOnRequestError(t *testing.T) {
	sender := &DiscordSender{
		webhookURL: "https://discord.com/api/webhooks/123/" + testWebhookToken + "\n",
		client:     &http.Client{Transport: failingTransport{}},
	}

	err := sender.Send("subject", "body")
	if err == nil {
		t.Fatal("expected an error")
	}
	assertWebhookRedacted(t, err.Error())
}

func TestRedactDiscordWebhook_LeavesUnrelatedTextAlone(t *testing.T) {
	const msg = "dial tcp: lookup discord.com: no such host"
	if got := redactDiscordWebhook(msg); got != msg {
		t.Fatalf("redactDiscordWebhook(%q) = %q, want unchanged", msg, got)
	}
}

func TestRedactDiscordWebhook_HandlesDiscordappHost(t *testing.T) {
	got := redactDiscordWebhook("post https://discordapp.com/api/webhooks/123/" + testWebhookToken + " failed")
	assertWebhookRedacted(t, got)
}

func TestRedactDiscordWebhook_KeepsSurroundingContext(t *testing.T) {
	got := redactDiscordWebhook(`Post "https://discord.com/api/webhooks/123/` + testWebhookToken + `": no such host`)
	if !strings.Contains(got, "no such host") {
		t.Fatalf("expected the underlying cause to survive redaction, got %q", got)
	}
	assertWebhookRedacted(t, got)
}

func assertWebhookRedacted(t *testing.T, msg string) {
	t.Helper()
	if strings.Contains(msg, testWebhookToken) {
		t.Fatalf("webhook token leaked into error: %q", msg)
	}
	if !strings.Contains(msg, "[REDACTED]") {
		t.Fatalf("expected a redaction marker in %q", msg)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errNoSuchHost
}
