package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/notify"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

type CursorTokenRefreshFunc func() string
type CursorCredentialsRefreshFunc func() *api.CursorCredentials
type CursorTokenSaveFunc func(accessToken, refreshToken string) error
type CursorRefreshRequestFunc func(context.Context, string) (*api.CursorOAuthResponse, error)

const cursorMaxAuthFailures = 3
const cursorRefreshBuffer = 5 * time.Minute

type CursorAgent struct {
	client             *api.CursorClient
	store              *store.Store
	tracker            *tracker.CursorTracker
	interval           time.Duration
	logger             *slog.Logger
	sm                 *SessionManager
	tokenRefresh       CursorTokenRefreshFunc
	credentialsRefresh CursorCredentialsRefreshFunc
	tokenSave          CursorTokenSaveFunc
	refreshRequest     CursorRefreshRequestFunc
	lastToken          string
	notifier           agentNotifier
	pollingCheck       func() bool

	authFailCount   int
	authPaused      bool
	lastFailedToken string
}

func (a *CursorAgent) SetPollingCheck(fn func() bool) {
	a.pollingCheck = fn
}

func (a *CursorAgent) SetNotifier(n *notify.NotificationEngine) {
	a.notifier = n
}

func (a *CursorAgent) recordPollFailure(category, message string) {
	if a.notifier != nil {
		a.notifier.RecordPollFailure("cursor", "default", category, message)
	}
}

func (a *CursorAgent) recordPollSuccess() {
	if a.notifier != nil {
		a.notifier.RecordPollSuccess("cursor", "default")
	}
}

func (a *CursorAgent) recordPollSkipped() {
	if a.notifier != nil {
		a.notifier.RecordPollSkipped("cursor", "default")
	}
}

func (a *CursorAgent) SetTokenRefresh(fn CursorTokenRefreshFunc) {
	a.tokenRefresh = fn
}

func (a *CursorAgent) SetCredentialsRefresh(fn CursorCredentialsRefreshFunc) {
	a.credentialsRefresh = fn
}

func (a *CursorAgent) SetTokenSave(fn CursorTokenSaveFunc) {
	a.tokenSave = fn
}

func NewCursorAgent(client *api.CursorClient, store *store.Store, tracker *tracker.CursorTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *CursorAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &CursorAgent{
		client:         client,
		store:          store,
		tracker:        tracker,
		interval:       interval,
		logger:         logger,
		sm:             sm,
		refreshRequest: api.RefreshCursorToken,
	}
}

func (a *CursorAgent) Run(ctx context.Context) error {
	a.logger.Info("Cursor agent started", "interval", a.interval)
	if a.notifier != nil {
		a.notifier.RegisterPoller("cursor", "default", a.interval)
	}

	defer func() {
		if a.notifier != nil {
			a.notifier.UnregisterPoller("cursor", "default")
		}
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("Cursor agent stopped")
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

func (a *CursorAgent) poll(ctx context.Context) {
	if ctx.Err() != nil {
		a.recordPollSkipped()
		return
	}
	if a.pollingCheck != nil && !a.pollingCheck() {
		a.recordPollSkipped()
		return
	}

	if a.authPaused {
		if a.tokenRefresh != nil {
			newToken := a.tokenRefresh()
			if newToken != "" && newToken != a.lastFailedToken {
				a.authPaused = false
				a.authFailCount = 0
				a.client.SetToken(newToken)
				a.logger.Info("Cursor auth recovered, resuming polling")
			}
		}
		if a.authPaused {
			// No outcome: missed-interval supervision advances the active incident.
			return
		}
	}

	refreshedThisPoll := false
	if a.credentialsRefresh != nil {
		creds := a.credentialsRefresh()
		if creds != nil && api.NeedsCursorRefresh(creds) && creds.RefreshToken != "" {
			a.logger.Info("Cursor token expiring soon, refreshing", "expires_in", creds.ExpiresIn.Round(time.Minute))
			refreshedThisPoll = a.refreshToken(ctx, creds.RefreshToken)
		}
	}

	if !refreshedThisPoll && a.tokenRefresh != nil {
		newToken := a.tokenRefresh()
		if newToken != "" && newToken != a.lastFailedToken && newToken != a.lastToken {
			a.client.SetToken(newToken)
			a.lastToken = newToken
		}
	}

	snapshot, err := a.client.FetchQuotas(ctx)
	if err != nil {
		if ctx.Err() != nil {
			a.recordPollSkipped()
			return
		}

		if api.IsCursorAuthError(err) {
			a.authFailCount++
			a.lastFailedToken = a.client.GetToken()
			a.logger.Warn("Cursor auth failure",
				"error", err,
				"fail_count", a.authFailCount,
			)

			if a.authFailCount >= cursorMaxAuthFailures {
				if a.credentialsRefresh != nil {
					creds := a.credentialsRefresh()
					if creds != nil && creds.RefreshToken != "" {
						a.logger.Info("Cursor: attempting token refresh due to auth failure")
						if a.refreshToken(ctx, creds.RefreshToken) {
							snapshot, err = a.client.FetchQuotas(ctx)
							if err == nil {
								a.authFailCount = 0
								a.authPaused = false
								goto processSnapshot
							}
							if ctx.Err() != nil {
								a.recordPollSkipped()
								return
							}
							if !api.IsCursorAuthError(err) && !api.IsCursorSessionExpired(err) {
								a.logger.Error("Cursor retry failed with non-auth error", "error", err)
								a.recordPollFailure("provider_request",
									"Cursor quotas could not be fetched. Check connectivity and provider availability.")
								return
							}
						}
					}
				}
				if ctx.Err() != nil {
					a.recordPollSkipped()
					return
				}
				a.authPaused = true
				a.logger.Warn("Cursor polling paused due to auth failures",
					"fail_count", a.authFailCount,
				)
			}
			a.recordPollFailure("authentication",
				"Cursor authentication failed. Re-authenticate in Cursor to resume polling.")
			return
		}

		if api.IsCursorSessionExpired(err) {
			a.logger.Error("Cursor session expired - user must re-authenticate", "error", err)
			a.authPaused = true
			a.recordPollFailure("authentication",
				"Cursor session expired. Re-authenticate in Cursor to resume polling.")
			return
		}

		a.logger.Error("Failed to fetch Cursor quotas", "error", err)
		a.recordPollFailure("provider_request",
			"Cursor quotas could not be fetched. Check connectivity and provider availability.")
		return
	}

	a.authFailCount = 0
	a.authPaused = false

processSnapshot:
	if _, err := a.store.InsertCursorSnapshot(snapshot); err != nil {
		a.logger.Error("Failed to insert Cursor snapshot", "error", err)
		a.recordPollFailure("storage",
			"Cursor usage was fetched but could not be saved. Check onWatch database access.")
	} else {
		a.recordPollSuccess()
	}

	if err := a.tracker.Process(snapshot); err != nil {
		a.logger.Error("Cursor tracker processing failed", "error", err)
	}

	if a.notifier != nil {
		for _, q := range snapshot.Quotas {
			a.notifier.Check(notify.QuotaStatus{
				Provider:    "cursor",
				QuotaKey:    q.Name,
				Utilization: q.Utilization,
				Limit:       q.Limit,
			})
		}
	}

	if a.sm != nil {
		var values []float64
		for _, q := range snapshot.Quotas {
			values = append(values, q.Utilization)
		}
		a.sm.ReportPoll(values)
	}

	a.logger.Info("Cursor poll complete",
		"account_type", snapshot.AccountType,
		"plan_name", snapshot.PlanName,
		"quota_count", len(snapshot.Quotas),
	)
}

func (a *CursorAgent) refreshToken(ctx context.Context, refreshToken string) bool {
	a.logger.Info("Cursor: refreshing OAuth token")

	refreshRequest := a.refreshRequest
	if refreshRequest == nil {
		refreshRequest = api.RefreshCursorToken
	}
	oauthResp, err := refreshRequest(ctx, refreshToken)
	if err != nil {
		if errors.Is(err, api.ErrCursorSessionExpired) {
			a.logger.Error("Cursor session expired during refresh - user must re-authenticate")
			a.authPaused = true
		} else {
			a.logger.Warn("Cursor token refresh failed", "error", err)
		}
		return false
	}

	return a.applyRefreshedCredentials(oauthResp)
}

func (a *CursorAgent) applyRefreshedCredentials(oauthResp *api.CursorOAuthResponse) bool {
	if oauthResp == nil || oauthResp.AccessToken == "" {
		return false
	}

	saveFn := a.tokenSave
	if saveFn == nil {
		saveFn = api.WriteCursorCredentials
	}
	if err := saveFn(oauthResp.AccessToken, oauthResp.RefreshToken); err != nil {
		a.logger.Error("Failed to save refreshed Cursor credentials", "error", err)
		return false
	}

	a.client.SetToken(oauthResp.AccessToken)
	a.lastToken = oauthResp.AccessToken
	a.lastFailedToken = ""
	a.logger.Info("Cursor token refreshed successfully")
	return true
}
