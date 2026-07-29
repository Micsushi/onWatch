package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// discordWebhookURL matches a webhook URL anywhere in free text. Errors from
// net/http and net/url quote the request URL, and for a webhook that URL is
// the credential, so it must be stripped before the error reaches a log.
var discordWebhookURL = regexp.MustCompile(
	`https://discord(?:app)?\.com/api/webhooks/[^\s"'\\]+`,
)

func redactDiscordWebhook(message string) string {
	return discordWebhookURL.ReplaceAllString(message, "https://discord.com/api/webhooks/[REDACTED]")
}

// redactErr strips this sender's webhook URL from an error, including hosts
// the generic pattern does not cover such as a test server.
func (d *DiscordSender) redactErr(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if d != nil && d.webhookURL != "" {
		message = strings.ReplaceAll(message, d.webhookURL, "[REDACTED]")
	}
	return errors.New(redactDiscordWebhook(message))
}

// DiscordSender delivers notifications to a Discord webhook.
type DiscordSender struct {
	webhookURL string
	client     *http.Client
}

// NewDiscordSender validates a Discord webhook URL and returns a sender.
func NewDiscordSender(webhookURL string) (*DiscordSender, error) {
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		return nil, fmt.Errorf("webhook URL is empty")
	}
	if !strings.HasPrefix(webhookURL, "https://discord.com/api/webhooks/") &&
		!strings.HasPrefix(webhookURL, "https://discordapp.com/api/webhooks/") {
		return nil, fmt.Errorf("webhook URL must be a Discord webhook URL")
	}
	return &DiscordSender{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// Send posts a compact notification message to Discord.
func (d *DiscordSender) Send(subject, body string) error {
	return d.SendContext(context.Background(), subject, body)
}

// SendContext posts a compact notification message and stops promptly when
// the caller's lifecycle is canceled.
func (d *DiscordSender) SendContext(ctx context.Context, subject, body string) error {
	if d == nil {
		return fmt.Errorf("discord sender is nil")
	}
	content := strings.TrimSpace(subject)
	description := strings.TrimSpace(body)
	if len(description) > 1800 {
		description = description[:1800] + "..."
	}
	payload := map[string]interface{}{
		"content": content,
		"embeds": []map[string]interface{}{
			{
				"description": description,
				"timestamp":   time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(data))
	if err != nil {
		return d.redactErr(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return d.redactErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook returned %s", resp.Status)
	}
	return nil
}
