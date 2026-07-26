# Poller Health Notifications Implementation Plan

> REQUIRED SUB-SKILL: Use superpowers:executing-plans.

Goal: Add persistent, account-scoped poll failure escalation, repeating external reminders, recovery messages, and in-app failure/recovery toasts while deploying the existing Codex profile fixes.

Architecture: `notify.NotificationEngine` will own a shared poll-health state machine backed by a new `poll_health_state` table. Provider agents report final poll outcomes and register their expected intervals; a background evaluator detects missed polls and sends reminders while paused agents are idle. Existing system alerts provide persistent dashboard state, while the dashboard shows each new poll-health alert ID as a toast once.

Tech Stack: Go, SQLite, existing onWatch notification senders, vanilla JavaScript, HTML, `app.sh`.

## Task 1: Persist account-scoped poll health

Files:

- Modify `internal/store/store.go`
- Create `internal/store/poll_health_store_test.go`

- [ ] Step 1: Write failing store tests for creating, updating, reopening, listing, and isolating poll-health rows by provider/account.
- [ ] Step 2: Run `go test ./internal/store -run 'TestPollHealth' -count=1` and confirm missing table/API failures.
- [ ] Step 3: Add `poll_health_state` to new-database schema with `(provider, account_id)` primary key and fields for interval, state, failure count, timing, latest sanitized error, external-delivery state, and active system-alert ID.
- [ ] Step 4: Add `PollHealthState`, `GetPollHealthState`, `UpsertPollHealthState`, and `ListUnhealthyPollHealthStates`.
- [ ] Step 5: Add `UpdateSystemAlert` so one active failure alert can be updated without changing its ID.
- [ ] Step 6: Run the focused store tests and `go test ./internal/store -count=1`.
- [ ] Step 7: Commit with `feat: persist poller health state`.

## Task 2: Add the shared poll-health state machine

Files:

- Create `internal/notify/poll_health.go`
- Create `internal/notify/poll_health_test.go`
- Modify `internal/notify/notify.go`

- [ ] Step 1: Write failing tests for first-failure in-app alerting, third-failure external escalation, recovery, six-hour reminders, restart persistence, account isolation, skipped outcomes, failed-delivery cooldown, and missed intervals.
- [ ] Step 2: Run `go test ./internal/notify -run 'TestPollHealth' -count=1` and confirm the new API is absent.
- [ ] Step 3: Add `PollIdentity`, `PollFailure`, registration state, and methods:

```go
func (e *NotificationEngine) RegisterPoller(provider, accountID string, interval time.Duration)
func (e *NotificationEngine) UnregisterPoller(provider, accountID string)
func (e *NotificationEngine) RecordPollFailure(provider, accountID, category, message string)
func (e *NotificationEngine) RecordPollSuccess(provider, accountID string)
func (e *NotificationEngine) EvaluatePollHealth()
func (e *NotificationEngine) RunPollHealthMonitor(ctx context.Context)
```

- [ ] Step 4: Create/update one `poll_failure` system alert on failure, independent of the external toggle.
- [ ] Step 5: Send externally only at the configured threshold, repeat when due, and retry failed delivery after the existing cooldown.
- [ ] Step 6: On success, dismiss the active failure alert, create one `poll_recovered` alert, and externally announce recovery only if failure delivery previously succeeded.
- [ ] Step 7: Detect missed poll intervals from registered identities without counting intentional unregistration.
- [ ] Step 8: Refactor channel delivery into a reusable internal sender while preserving current quota notification behavior.
- [ ] Step 9: Run focused and full `internal/notify` tests.
- [ ] Step 10: Commit with `feat: add poller health monitor`.

## Task 3: Wire Codex and Claude final poll outcomes

Files:

- Modify `internal/agent/codex_agent.go`
- Modify `internal/agent/anthropic_agent.go`
- Modify `internal/agent/codex_agent_test.go`
- Modify `internal/agent/agent_extra_test.go`

- [ ] Step 1: Write failing tests proving one final failure is reported after internal retries, a successful retry reports success only, auth-paused agents do not directly duplicate external notifications, and recovery reports success.
- [ ] Step 2: Run the focused Codex and Anthropic tests and observe the missing reports.
- [ ] Step 3: Register each agent on `Run`, report final failures at terminal error returns, and report success only after usable snapshot processing.
- [ ] Step 4: Preserve auth-specific categories/action text but remove direct threshold-bypassing `SendAuthErrorNotification` calls.
- [ ] Step 5: Use Codex database account ID as the health account key and `default` for Anthropic.
- [ ] Step 6: Run focused tests and `go test ./internal/agent -run 'Codex|Anthropic' -count=1`.
- [ ] Step 7: Commit with `feat: report Codex and Claude poll health`.

## Task 4: Wire remaining external provider pollers

Files:

- Modify `internal/agent/agent.go`
- Modify `internal/agent/zai_agent.go`
- Modify `internal/agent/copilot_agent.go`
- Modify `internal/agent/antigravity_agent.go`
- Modify `internal/agent/minimax_agent.go`
- Modify `internal/agent/openrouter_agent.go`
- Modify `internal/agent/gemini_agent.go`
- Modify `internal/agent/cursor_agent.go`
- Modify relevant focused agent test files

- [ ] Step 1: Add failing table-driven or focused tests proving each agent reports a terminal failure and a later success without counting disabled/cancelled polls.
- [ ] Step 2: Run the focused tests and observe the missing reports.
- [ ] Step 3: Register single-account agents with `default`; register MiniMax using its database account ID.
- [ ] Step 4: Report one final result per scheduled poll, after internal refresh/retry logic completes.
- [ ] Step 5: Remove provider-specific direct auth escalation where it would duplicate the shared monitor.
- [ ] Step 6: Run all focused tests and `go test ./internal/agent -count=1`.
- [ ] Step 7: Commit with `feat: report provider poll health`.

## Task 5: Add poll-health settings and migration

Files:

- Modify `internal/notify/notify.go`
- Modify `internal/web/handlers.go`
- Modify `internal/web/templates/settings.html`
- Modify `internal/web/static/app.js`
- Modify `internal/notify/notify_test.go`
- Modify `internal/web/handlers_test.go`

- [ ] Step 1: Write failing tests for defaults, legacy `notify_auth_error` fallback, validation, round-trip persistence, and notifier reload.
- [ ] Step 2: Run focused settings tests and confirm missing fields.
- [ ] Step 3: Add JSON fields:

```json
{
  "notify_poll_failure": true,
  "poll_failure_threshold": 3,
  "poll_failure_repeat_hours": 6,
  "notify_poll_recovery": true
}
```

- [ ] Step 4: Apply defaults and validation: threshold minimum `2`, repeat hours minimum `1`.
- [ ] Step 5: Fall back to legacy `notify_auth_error` only when `notify_poll_failure` is absent; continue reading/writing the legacy value for compatibility.
- [ ] Step 6: Replace the Auth Error label with Poller Failure and Recovery controls in Settings.
- [ ] Step 7: Run focused notify/web tests.
- [ ] Step 8: Commit with `feat: configure poller health alerts`.

## Task 6: Show new poll-health alerts as dashboard toasts

Files:

- Modify `internal/web/static/app.js`
- Modify `internal/web/dashboard_performance_test.go`

- [ ] Step 1: Add failing static behavior tests for ten-second alert refresh, new-ID toast display, recovery success styling, and bounded local-storage deduplication.
- [ ] Step 2: Run `go test ./internal/web -run 'PollHealth|Notification' -count=1` and confirm failure.
- [ ] Step 3: Compare fetched alerts against stored seen IDs and call the existing `showDashboardToast` only for new `poll_failure` and `poll_recovered` IDs.
- [ ] Step 4: Keep at most 100 seen IDs and refresh system alerts every ten seconds.
- [ ] Step 5: Run focused and full web tests.
- [ ] Step 6: Commit with `feat: toast poller health changes`.

## Task 7: Start the monitor with application lifecycle

Files:

- Modify `main.go`
- Modify `main_test.go` or an appropriate startup wiring test

- [ ] Step 1: Add a failing wiring test or test seam proving the poll-health monitor uses the application context.
- [ ] Step 2: Start `notifier.RunPollHealthMonitor(ctx)` after the application context is created and before agents begin polling.
- [ ] Step 3: Ensure cancellation stops the monitor without creating failures during shutdown.
- [ ] Step 4: Run focused startup tests and `go test ./... -short -count=1`.
- [ ] Step 5: Commit with `feat: run poller health supervision`.

## Task 8: Full verification and live deployment

Files:

- No planned source edits unless verification exposes a regression.

- [ ] Step 1: Run `git diff --check`.
- [ ] Step 2: Run `CGO_ENABLED=0 ./app.sh --smoke`.
- [ ] Step 3: Run `./app.sh --build`.
- [ ] Step 4: Back up `C:\Users\sushi\.onwatch\bin\onwatch.exe` with a timestamped name.
- [ ] Step 5: Stop the installed onWatch process, replace the executable with the verified build, and restart it.
- [ ] Step 6: Enable `notify_poll_failure`, threshold `3`, repeat hours `6`, and recovery notifications in the live notification settings while preserving all other settings.
- [ ] Step 7: Verify the installed executable version/hash and HTTP 200 health.
- [ ] Step 8: Observe the live logs across at least two 30-second scanner boundaries and confirm the Codex agent is not restarted.
- [ ] Step 9: Confirm a current-account Codex poll succeeds and no stale `imported` account is active.
- [ ] Step 10: Create a safe simulated poll-health system alert, verify it through `/api/alerts`, and dismiss it without touching real provider credentials.
- [ ] Step 11: Report any remaining limitation: whole-process death still requires the separate Windows watchdog follow-up.
