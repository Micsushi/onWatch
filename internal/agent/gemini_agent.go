package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/notify"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

// maxGeminiAuthFailures is the number of consecutive auth failures before pausing polling.
const maxGeminiAuthFailures = 3

// geminiTokenRefreshThreshold is how soon before expiry we proactively refresh.
// Google tokens expire in ~1hr, so 15 minutes provides a comfortable buffer.
const geminiTokenRefreshThreshold = 15 * time.Minute

const (
	geminiAuthRetryBase = time.Hour
	geminiAuthRetryMax  = 24 * time.Hour
)

// GeminiCredentialsRefreshFunc returns fresh credentials from disk.
type GeminiCredentialsRefreshFunc func() *api.GeminiCredentials
type GeminiRefreshRequestFunc func(context.Context, string, string, string) (*api.GeminiOAuthTokenResponse, error)

// isGeminiAuthError returns true if the error is an authentication/authorization error.
func isGeminiAuthError(err error) bool {
	return errors.Is(err, api.ErrGeminiUnauthorized) || errors.Is(err, api.ErrGeminiForbidden)
}

// GeminiAgent manages the background polling loop for Gemini quota tracking.
type GeminiAgent struct {
	client         *api.GeminiClient
	store          *store.Store
	tracker        *tracker.GeminiTracker
	interval       time.Duration
	logger         *slog.Logger
	sm             *SessionManager
	notifier       agentNotifier
	pollingCheck   func() bool
	credsRefresh   GeminiCredentialsRefreshFunc
	clientCreds    *api.GeminiClientCredentials
	refreshRequest GeminiRefreshRequestFunc
	lastToken      string

	// Auth failure rate limiting
	authFailCount   int
	authPaused      bool
	lastFailedToken string
	authRetryCount  int
	authRetryAt     time.Time
	failedRefresh   string
	now             func() time.Time
	random          func() float64

	// Tier caching
	tierFetched bool
}

// NewGeminiAgent creates a new GeminiAgent with the given dependencies.
func NewGeminiAgent(client *api.GeminiClient, st *store.Store, tracker *tracker.GeminiTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *GeminiAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &GeminiAgent{
		client:         client,
		store:          st,
		tracker:        tracker,
		interval:       interval,
		logger:         logger,
		sm:             sm,
		refreshRequest: api.RefreshGeminiToken,
		now:            time.Now,
		random:         rand.Float64,
	}
}

func geminiAuthRetryDelay(attempt int, random float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := geminiAuthRetryBase
	for i := 1; i < attempt && delay < geminiAuthRetryMax; i++ {
		if delay > geminiAuthRetryMax/2 {
			delay = geminiAuthRetryMax
			break
		}
		delay *= 2
	}
	if random < 0 {
		random = 0
	} else if random > 1 {
		random = 1
	}
	delay = time.Duration(float64(delay) * (0.8 + 0.4*random))
	if delay > geminiAuthRetryMax {
		return geminiAuthRetryMax
	}
	return delay
}

func (a *GeminiAgent) currentTime() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *GeminiAgent) pauseAuthentication(accessToken, refreshToken string) {
	a.authPaused = true
	a.lastFailedToken = accessToken
	a.failedRefresh = refreshToken
	a.authRetryCount++
	random := 0.5
	if a.random != nil {
		random = a.random()
	}
	a.authRetryAt = a.currentTime().Add(geminiAuthRetryDelay(a.authRetryCount, random))
	a.logger.Error("Gemini polling PAUSED due to repeated auth failures",
		"retry_at", a.authRetryAt,
		"retry_attempt", a.authRetryCount)
}

func (a *GeminiAgent) resetAuthenticationFailures() {
	a.authFailCount = 0
	a.authPaused = false
	a.lastFailedToken = ""
	a.failedRefresh = ""
	a.authRetryCount = 0
	a.authRetryAt = time.Time{}
}

// SetPollingCheck sets a function called before each poll.
func (a *GeminiAgent) SetPollingCheck(fn func() bool) {
	a.pollingCheck = fn
}

// SetNotifier sets notification engine for sending alerts.
func (a *GeminiAgent) SetNotifier(n *notify.NotificationEngine) {
	a.notifier = n
}

func (a *GeminiAgent) recordPollFailure(category, message string) {
	if a.notifier != nil {
		a.notifier.RecordPollFailure("gemini", "default", category, message)
	}
}

func (a *GeminiAgent) recordPollSuccess() {
	if a.notifier != nil {
		a.notifier.RecordPollSuccess("gemini", "default")
	}
}

func (a *GeminiAgent) recordPollSkipped() {
	if a.notifier != nil {
		a.notifier.RecordPollSkipped("gemini", "default")
	}
}

// SetCredentialsRefresh sets a function that returns fresh credentials for proactive OAuth refresh.
func (a *GeminiAgent) SetCredentialsRefresh(fn GeminiCredentialsRefreshFunc) {
	a.credsRefresh = fn
}

// SetClientCredentials sets the OAuth client credentials for token refresh.
func (a *GeminiAgent) SetClientCredentials(creds *api.GeminiClientCredentials) {
	a.clientCreds = creds
}

// Run starts the agent polling loop.
func (a *GeminiAgent) Run(ctx context.Context) error {
	a.logger.Info("Gemini agent started", "interval", a.interval)
	if a.notifier != nil {
		a.notifier.RegisterPoller("gemini", "default", a.interval)
	}

	defer func() {
		if a.notifier != nil {
			a.notifier.UnregisterPoller("gemini", "default")
		}
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("Gemini agent stopped")
	}()

	a.poll(ctx)

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.poll(ctx)
		case <-ctx.Done():
			return nil
		}
	}
}

func (a *GeminiAgent) poll(ctx context.Context) {
	if ctx.Err() != nil {
		a.recordPollSkipped()
		return
	}
	if a.pollingCheck != nil && !a.pollingCheck() {
		a.recordPollSkipped()
		return
	}

	// Inspect credentials before any network request. A paused agent retries on
	// its backoff deadline, while a new refresh-token lineage resumes at once.
	var currentCreds *api.GeminiCredentials
	if a.credsRefresh != nil && a.clientCreds != nil {
		currentCreds = a.credsRefresh()
		if currentCreds != nil {
			copy := *currentCreds
			currentCreds = &copy
		}
		if a.authPaused {
			credentialsChanged := currentCreds != nil && currentCreds.RefreshToken != "" && currentCreds.RefreshToken != a.failedRefresh
			if !credentialsChanged && a.currentTime().Before(a.authRetryAt) {
				// No outcome: missed-interval supervision advances the active incident.
				return
			}
			if credentialsChanged {
				a.resetAuthenticationFailures()
				a.logger.Info("Gemini auth failure pause lifted - new credentials detected")
			} else {
				a.authPaused = false
				a.logger.Info("Gemini authentication retry backoff elapsed",
					"retry_attempt", a.authRetryCount)
			}
		}

		if creds := currentCreds; creds != nil {
			if creds.IsExpiringSoon(geminiTokenRefreshThreshold) && creds.RefreshToken != "" {
				a.logger.Info("Gemini token expiring soon, attempting proactive OAuth refresh",
					"expires_in", creds.ExpiresIn.Round(time.Second))

				newTokens, err := a.refreshGeminiToken(ctx,
					creds.RefreshToken,
					a.clientCreds.ClientID,
					a.clientCreds.ClientSecret,
				)
				if err != nil {
					a.logger.Error("Proactive Gemini OAuth refresh failed", "error", err)
				} else {
					// Save to file (local users)
					if err := api.WriteGeminiCredentials(newTokens.AccessToken, newTokens.ExpiresIn); err != nil {
						a.logger.Debug("Failed to save Gemini credentials to file", "error", err)
					}
					// Save to DB (survives Docker container restarts)
					a.saveTokensToDB(newTokens.AccessToken, creds.RefreshToken, newTokens.ExpiresIn)

					a.client.SetToken(newTokens.AccessToken)
					a.lastToken = newTokens.AccessToken
					creds.AccessToken = newTokens.AccessToken
					a.logger.Info("Proactively refreshed Gemini OAuth token",
						"expires_in_seconds", newTokens.ExpiresIn)
				}
			}

			// Check if credentials changed on disk (user re-authed via CLI)
			if creds.AccessToken != "" && creds.AccessToken != a.lastToken {
				a.client.SetToken(creds.AccessToken)
				a.lastToken = creds.AccessToken
				a.logger.Info("Gemini token refreshed from credentials file")

			}
		}
	}

	if a.authPaused {
		// No outcome: missed-interval supervision advances the active incident.
		return
	}

	// Fetch tier on first poll to get project ID
	if !a.tierFetched {
		tierResp, err := a.client.FetchTier(ctx)
		if err != nil {
			if ctx.Err() != nil {
				a.recordPollSkipped()
				return
			}
			a.logger.Warn("Failed to fetch Gemini tier", "error", err)
		} else {
			if tierResp.CloudAICompanionProject != "" {
				a.client.SetProjectID(tierResp.CloudAICompanionProject)
				a.logger.Info("Gemini tier detected",
					"tier", tierResp.Tier,
					"project", tierResp.CloudAICompanionProject)
			}
			a.tierFetched = true
		}
	}

	resp, err := a.client.FetchQuotas(ctx)
	if err != nil {
		if ctx.Err() != nil {
			a.recordPollSkipped()
			return
		}

		if isGeminiAuthError(err) && a.credsRefresh != nil && a.clientCreds != nil {
			a.logger.Warn("Gemini auth error, attempting token refresh", "error", err)

			if creds := a.credsRefresh(); creds != nil && creds.RefreshToken != "" {
				newTokens, refreshErr := a.refreshGeminiToken(ctx,
					creds.RefreshToken,
					a.clientCreds.ClientID,
					a.clientCreds.ClientSecret,
				)
				if refreshErr == nil {
					if err := api.WriteGeminiCredentials(newTokens.AccessToken, newTokens.ExpiresIn); err != nil {
						a.logger.Debug("Failed to save Gemini credentials to file", "error", err)
					}
					a.saveTokensToDB(newTokens.AccessToken, creds.RefreshToken, newTokens.ExpiresIn)
					a.client.SetToken(newTokens.AccessToken)
					a.lastToken = newTokens.AccessToken
					a.logger.Info("Retrying Gemini poll with refreshed token")

					resp, err = a.client.FetchQuotas(ctx)
					if err != nil {
						if ctx.Err() != nil {
							a.recordPollSkipped()
							return
						}
						if isGeminiAuthError(err) {
							a.authFailCount++
							a.logger.Error("Gemini auth retry failed",
								"error", err,
								"failure_count", a.authFailCount,
								"max_failures", maxGeminiAuthFailures)

							if a.authFailCount >= maxGeminiAuthFailures {
								a.pauseAuthentication(newTokens.AccessToken, creds.RefreshToken)
							}
						} else {
							a.logger.Error("Gemini retry failed with non-auth error", "error", err)
						}
						category := "provider_request"
						if isGeminiAuthError(err) {
							category = "authentication"
						}
						a.recordPollFailure(category,
							"Gemini quota could not be fetched after credential refresh. Reauthenticate if the incident persists.")
						return
					}
					a.authFailCount = 0
				} else {
					if ctx.Err() != nil {
						a.recordPollSkipped()
						return
					}
					a.logger.Error("Gemini OAuth refresh failed on auth error", "error", refreshErr)
					a.authFailCount++
					if a.authFailCount >= maxGeminiAuthFailures {
						a.pauseAuthentication(a.lastToken, creds.RefreshToken)
					}
					a.recordPollFailure("authentication",
						"Gemini credentials could not be refreshed. Reauthenticate the isolated Gemini profile.")
					return
				}
			} else {
				a.logger.Error("No Gemini refresh token available for retry")
				a.recordPollFailure("missing_credentials",
					"Gemini has no refresh token. Reauthenticate the isolated Gemini profile.")
				return
			}
		} else {
			a.logger.Error("Failed to fetch Gemini quotas", "error", err)
			category := "provider_request"
			if isGeminiAuthError(err) {
				category = "authentication"
			}
			a.recordPollFailure(category,
				"Gemini quota could not be fetched. Check connectivity, credentials, and provider availability.")
			return
		}
	} else {
		a.resetAuthenticationFailures()
	}

	now := time.Now().UTC()
	snapshot := resp.ToSnapshot(now)

	if _, err := a.store.InsertGeminiSnapshot(snapshot); err != nil {
		a.logger.Error("Failed to insert Gemini snapshot", "error", err)
		a.recordPollFailure("storage",
			"Gemini quota was fetched but could not be saved. Check onWatch database access.")
		return
	}
	a.recordPollSuccess()
	a.resetAuthenticationFailures()

	if a.tracker != nil {
		if err := a.tracker.Process(snapshot); err != nil {
			a.logger.Error("Gemini tracker processing failed", "error", err)
		}
	}

	if a.notifier != nil {
		for _, q := range snapshot.Quotas {
			a.notifier.Check(notify.QuotaStatus{
				Provider:    "gemini",
				QuotaKey:    q.ModelID,
				Utilization: q.UsagePercent,
				Limit:       100,
			})
		}
	}

	if a.sm != nil {
		values := make([]float64, 0, len(snapshot.Quotas))
		for _, q := range snapshot.Quotas {
			values = append(values, q.UsagePercent)
		}
		a.sm.ReportPoll(values)
	}

	for _, q := range snapshot.Quotas {
		a.logger.Info("Gemini poll complete",
			"model", q.ModelID,
			"remaining", fmt.Sprintf("%.1f%%", q.RemainingFraction*100),
			"usage", fmt.Sprintf("%.1f%%", q.UsagePercent))
	}
}

func (a *GeminiAgent) refreshGeminiToken(ctx context.Context, refreshToken, clientID, clientSecret string) (*api.GeminiOAuthTokenResponse, error) {
	refreshRequest := a.refreshRequest
	if refreshRequest == nil {
		refreshRequest = api.RefreshGeminiToken
	}
	return refreshRequest(ctx, refreshToken, clientID, clientSecret)
}

// saveTokensToDB persists tokens to the DB so they survive Docker restarts.
func (a *GeminiAgent) saveTokensToDB(accessToken, refreshToken string, expiresInSec int) {
	if a.store == nil {
		return
	}
	expiresAt := time.Now().Add(time.Duration(expiresInSec) * time.Second).UnixMilli()
	if err := a.store.SaveGeminiTokens(accessToken, refreshToken, expiresAt); err != nil {
		a.logger.Error("Failed to persist Gemini tokens to DB", "error", err)
	} else {
		a.logger.Debug("Persisted Gemini tokens to DB")
	}
}
