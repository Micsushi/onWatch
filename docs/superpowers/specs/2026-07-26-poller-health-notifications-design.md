# Poller Health and Recovery Notifications Design

Date: 2026-07-26

## Summary

onWatch will track poll health per provider and account, show an in-app warning after the first failed or missed poll, escalate externally after three consecutive failures or missed poll intervals, repeat external failure notifications every six hours while the failure remains active, and announce recovery.

Failure state and notification timestamps will be persisted so restarts do not erase the failure count or bypass the repeat cooldown. The failure threshold and repeat interval will be configurable in Settings.

## Confirmed Incident

The Codex incident had two independent causes:

1. The Codex manager considered the existence of any saved profile sufficient and did not start the current credentials from `CODEX_HOME/auth.json`. A stale `imported` profile therefore suppressed the valid current account, polled with an invalid token, failed three authentication retries, and paused.
2. When onWatch synchronized fresh credentials into a named Codex profile, its 30-second profile scanner treated the resulting file modification as an external edit and restarted the Codex agent. Each restart wrote the profile again, creating a restart loop.

The current `main` source includes regression-tested fixes for both causes:

- Start the current Codex credentials when no running saved profile represents the same account and user.
- Record profile modification times after onWatch writes the profile so the scanner ignores its own writes.

The installed onWatch binary predates the commit containing these fixes and must be rebuilt and restarted.

The missing notification had a separate cause. `SendAuthErrorNotification` returns immediately when external auth notifications are disabled, even though the code below the return claims to always create an in-dashboard alert. Consequently, disabling external auth notifications also suppresses the in-app alert. Ordinary non-auth poll failures do not enter this notification path at all.

## Goals

- Track every configured external provider poller consistently.
- Track multi-account providers independently by provider and account.
- Distinguish a transient first failure from a sustained outage.
- Show an in-app warning after the first explicit failure or first missed poll interval.
- Send external failure alerts after three consecutive failures or three missed intervals.
- Repeat external failure alerts every configurable interval while still failing.
- Send recovery messages when polling succeeds again.
- Preserve health state and notification cooldowns across onWatch restarts.
- Detect an agent that is stalled or absent while the onWatch process remains alive.
- Avoid duplicate messages from existing provider-specific auth failure paths.

## Non-goals

- Detecting the entire onWatch process being stopped, crashed, or unable to reach its database. A process cannot report its own death; the Windows watchdog must cover this separately.
- Retrying or changing provider authentication flows beyond the existing Codex profile fixes.
- Sending a Discord alert for a single transient poll failure.
- Treating an intentionally disabled provider or application shutdown as a failure.

## Considered Approaches

### Provider-specific counters

Each provider agent could maintain its own counters and send its own messages. This is the smallest local edit but duplicates timing, persistence, recovery, and account-scoping logic across every agent. Behavior would drift between providers.

### Snapshot staleness only

A background job could infer failures from old snapshot timestamps. This catches a missing agent but cannot distinguish disabled polling, known backoff, a failed request, or providers that legitimately produce no snapshot. It also cannot provide an immediate first-failure toast.

### Shared poll-health monitor

A shared monitor accepts final poll outcomes from every provider, persists a state machine per provider/account, and independently checks registered pollers for missed intervals. This centralizes escalation, repetition, recovery, deduplication, and settings.

This is the selected approach.

## Architecture

### Poll identity

Each monitored poller has a stable key:

```text
provider + account ID
```

Single-account providers use `default`. Codex and MiniMax use their database account identifiers. All alert queries and deduplication use the full key so one broken account cannot suppress or affect another.

### Poll outcome contract

Provider agents report only the final result of a scheduled poll:

- `Success`: the provider returned usable data and required persistence completed.
- `Failure`: the final request or required processing failed.
- `Skipped`: polling was disabled, the application was shutting down, or the provider intentionally deferred an attempt. Skips do not change health state.

Internal retries do not increment the failure counter. For example, an initial 401 followed by a successful credential refresh is one successful poll, not one failure followed by one success.

Errors stored for display are sanitized summaries. Tokens, response bodies that may contain secrets, and credential paths are excluded.

### Registration and missed polls

Each expected provider/account registers its polling interval with the monitor when its agent starts and unregisters when intentionally stopped.

The monitor records the last completed poll outcome. If an expected poller does not report an outcome within one polling interval plus a small scheduling grace period, it enters a missed-poll warning state and creates the in-app alert. If three expected intervals elapse without an outcome, it qualifies for external escalation.

This catches an agent that never starts, stops unexpectedly, hangs, or is repeatedly restarted while onWatch itself remains alive.

### Persistent state

A dedicated table stores one row per poll identity:

- provider
- account ID
- registered polling interval
- state (`healthy`, `failing`, or `stalled`)
- consecutive failures
- first failure time
- last failure time
- last success time
- last completed poll time
- sanitized last error category and message
- first external alert time
- last external notification attempt
- last successful external failure notification
- whether an external failure notification was successfully delivered

The unique key is `(provider, account_id)`.

On restart, the monitor loads this state. It does not send a fresh external alert merely because onWatch restarted. It sends only when the configured threshold or repeat deadline is due.

### Failure state machine

On the first failed poll:

1. Change the identity to `failing`.
2. Set the consecutive count to one.
3. Create or update one active `poll_failure` system alert.
4. Allow the dashboard to show one warning toast for that alert.
5. Do not send externally.

On later failed polls:

1. Increment the consecutive count.
2. Update the same system alert in place so the notification center shows the current count and latest sanitized error.
3. Do not create a new toast for every poll.
4. At the configured threshold, attempt external delivery.

On missed intervals:

1. Create the warning after the first missed deadline.
2. Update the same alert as additional deadlines pass.
3. Externally escalate when the number of missed intervals reaches the configured threshold.

### External escalation and repetition

External poll-health notifications are sent through every enabled and configured channel.

Defaults:

- Failure threshold: three consecutive failed or missed polls.
- Repeat interval: six hours.
- Delivery retry after all channels fail: the existing notification cooldown, currently 30 minutes.

After a successful external failure notification, a background monitor checks active failures independently of the provider agent. When six hours have elapsed, it sends a reminder even if an auth-paused provider is no longer making requests.

The reminder contains:

- provider and account
- failure or stalled state
- consecutive failures or missed intervals
- duration of the outage
- sanitized latest error
- required action when known

### Recovery

The first successful poll after a failure:

1. Changes the persistent state to `healthy`.
2. Resets the consecutive failure count.
3. Automatically dismisses the active failure alert for that provider/account.
4. Creates one `poll_recovered` system alert so an open dashboard shows a success toast.
5. Sends an external recovery message only if at least one external failure notification was successfully delivered.
6. Clears the external-alert marker after recovery delivery is attempted so later incidents start cleanly.

A transient failure that recovered before the external threshold therefore produces in-app failure and recovery messages without unnecessary Discord noise.

### Existing auth notification path

Provider-specific authentication pause logic will report a final poll failure to the shared monitor rather than directly sending a separate auth notification. Authentication remains an error category and can provide a provider-specific action message, but thresholding, repeats, and recovery are owned by the shared monitor.

In-app health alerts are independent of the external notification toggle. Turning external poller alerts off never suppresses dashboard alerts.

## Settings

The Notifications settings section will expose:

- `Poller failure and recovery alerts`: external alert toggle.
- `Failures before external alert`: integer, default `3`, minimum `2`.
- `Repeat active failure every`: hours, default `6`, minimum `1`.
- `Send recovery alerts`: enabled by default.

The existing `notify_auth_error` value remains readable for backward compatibility. The new poller-health toggle is written under a new setting name. When the new value is absent, migration uses the existing auth setting. For the current installation, the new external poller-health toggle will be enabled as requested.

Channel settings remain unchanged. Discord, email, and push receive poll-health messages only when their existing channel toggle is enabled and the sender is configured.

## Dashboard behavior

The existing notification center remains the source of persistent in-app alerts.

The dashboard will:

- Refresh system alerts every ten seconds instead of every sixty seconds.
- Show a toast when it observes a new `poll_failure` or `poll_recovered` alert ID.
- Remember a bounded set of displayed alert IDs in local storage so page refreshes do not replay the same toast.
- Use warning/error styling for failures and success styling for recovery.
- Keep the notification center badge and dismiss controls unchanged.

Updating the existing failure alert does not change its ID and therefore does not create repeated toasts.

## Provider coverage

The shared monitor will cover externally configured quota pollers, including:

- Codex
- Anthropic/Claude
- Gemini
- Cursor
- Copilot
- MiniMax
- Antigravity
- OpenRouter
- Z.ai
- other providers using the generic quota agent

Local maintenance jobs and intentionally event-driven collectors are excluded unless they already define a scheduled provider poll contract.

## Testing

Tests will be written before production changes.

### Store tests

- Create and load poll-health state.
- Isolate state by provider/account.
- Persist notification timestamps.
- Update and dismiss the correct account-scoped system alert.
- Preserve state across store reopen.

### Monitor tests

- First failure creates an in-app alert but no external message.
- Third consecutive failure sends exactly one external message.
- A success between failures resets the count.
- Active failure repeats after six hours and not before.
- Auth-paused failure repeats without another provider poll.
- Recovery sends externally only after an externally announced failure.
- Restart does not duplicate an alert.
- Disabled/cancelled/skipped polls do not count.
- Missed poll intervals create a warning and eventually escalate.
- Multi-account failures remain isolated.
- Failed external delivery respects the notification cooldown.

### Agent integration tests

- Codex reports one final outcome per poll despite internal auth retry.
- Anthropic reports one final outcome per poll despite refresh and rate-limit paths.
- Existing direct auth notifications do not duplicate monitor messages.
- Every configured provider wires a reporter and registers its expected interval.

### Dashboard tests

- New poll-health alerts trigger the existing toast component.
- Updated alert IDs are not replayed.
- Recovery uses success styling.
- Polling interval is ten seconds.

### Live verification

After all automated tests pass:

1. Build using `app.sh`.
2. Back up the installed executable.
3. Replace and restart the installed onWatch process.
4. Confirm the process uses the new executable.
5. Confirm HTTP health responds successfully.
6. Confirm Codex starts once and does not restart at the 30-second scanner boundary.
7. Confirm Codex completes a normal poll with the current account.
8. Confirm poll-health settings are persisted and externally enabled.
9. Confirm a simulated in-app poll-health alert appears in the API and dashboard without modifying real credentials.

## Whole-process watchdog follow-up

The existing Windows watchdog must be inspected separately for whole-process outage detection and external notification. Its state is outside the running onWatch process and is deliberately not combined with provider poll-health state.
