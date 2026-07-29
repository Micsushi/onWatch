package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/onllm-dev/onwatch/v2/internal/store"
)

const (
	defaultPollHealthAccount  = "default"
	maxPollErrorCategoryRunes = 64
	maxPollErrorMessageRunes  = 512
)

var (
	pollBearerSecret = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;]+`)
	pollHeaderSecret = regexp.MustCompile(
		`(?im)\b(authorization|proxy[_-]?authorization|cookie|set-cookie)\b\s*:\s*[^\r\n]+`,
	)
	pollSchemeAssignmentSecret = regexp.MustCompile(
		`(?i)\b(authorization|proxy[_-]?authorization|auth(?:[_-]?(?:key|token))?)\b["']?\s*[:=]\s*(?:basic|bearer|token)\s+[^\s,;}\]]+`,
	)
	pollQuotedSecret = regexp.MustCompile(
		`(?i)["']?(access[_-]?token|refresh[_-]?token|id[_-]?token|token|client[_-]?secret|api[_-]?key|authorization|proxy[_-]?authorization|password|passwd|set-cookie|cookie|session(?:[_-]?id)?|auth(?:[_-]?(?:key|token))?)["']?\s*[:=]\s*["'][^"']*["']`,
	)
	pollNamedSecret = regexp.MustCompile(
		`(?i)\b(access[_-]?token|refresh[_-]?token|id[_-]?token|token|client[_-]?secret|api[_-]?key|authorization|proxy[_-]?authorization|password|passwd|set-cookie|cookie|session(?:[_-]?id)?|auth(?:[_-]?(?:key|token))?)\b["']?\s*[:=]\s*[^\s,;}\]]+`,
	)
	pollCredentialPath = regexp.MustCompile(
		`(?i)(?:[a-z]:\\|/)[^\r\n]*?(?:auth\.json|credentials(?:\.json)?)`,
	)
)

// PollIdentity is the stable provider/account key used by the health monitor.
type PollIdentity struct {
	Provider  string
	AccountID string
}

type pollRegistration struct {
	interval     time.Duration
	registeredAt time.Time
	observedAt   time.Time
}

type pollDeliveryAttempt struct {
	id              uint64
	kind            string
	incident        time.Time
	attemptedAt     time.Time
	cancel          context.CancelFunc
	pendingRecovery *pollPendingRecovery
}

type pollPendingRecovery struct {
	previouslyDelivered bool
	subject             string
	body                string
}

type pollHealthMutationStage string

const (
	pollHealthBeforeMissedMutation pollHealthMutationStage = "before_missed_mutation"
	pollHealthBeforeDeliveryClaim  pollHealthMutationStage = "before_delivery_claim"
)

type pollHealthMutationHookKey struct{}

type pollDeliveryClaim struct {
	identity        PollIdentity
	attempt         pollDeliveryAttempt
	ctx             context.Context
	previousAttempt *time.Time
	cfg             NotificationConfig
	mailer          *SMTPMailer
	pushSender      *PushSender
	discord         *DiscordSender
	subject         string
	body            string
}

// PollFailure is the sanitized failure information persisted for a poll.
type PollFailure struct {
	Category string
	Message  string
}

// RegisterPoller begins missed-interval supervision for a provider/account.
func (e *NotificationEngine) RegisterPoller(provider, accountID string, interval time.Duration) {
	if interval <= 0 {
		return
	}
	identity := normalizePollIdentity(provider, accountID)
	now := e.now().UTC()

	e.pollHealthMu.Lock()
	defer e.pollHealthMu.Unlock()

	// Supervision must remain active even when SQLite is briefly busy during
	// concurrent agent startup. Persistence can recover on a later outcome or
	// missed-interval evaluation; dropping the registration cannot.
	e.pollHealthPollers[identity] = pollRegistration{
		interval:     interval,
		registeredAt: now,
		observedAt:   now,
	}

	state, err := e.store.GetPollHealthState(identity.Provider, identity.AccountID)
	if err != nil {
		e.logger.Error("failed to load poll health state", "error", err,
			"provider", identity.Provider, "account", identity.AccountID)
		return
	}
	if state == nil {
		state = newPollHealthState(identity)
	}
	state.IntervalSeconds = durationSecondsCeil(interval)
	if err := e.store.UpsertPollHealthState(state); err != nil {
		e.logger.Error("failed to register poll health state", "error", err,
			"provider", identity.Provider, "account", identity.AccountID)
		return
	}
}

// UnregisterPoller stops missed-interval supervision without changing the
// persisted incident. This makes intentional shutdown a non-failure.
func (e *NotificationEngine) UnregisterPoller(provider, accountID string) {
	identity := normalizePollIdentity(provider, accountID)
	e.pollHealthMu.Lock()
	inFlightAttempt, hasInFlightAttempt := e.pollHealthAttempts[identity]
	if hasInFlightAttempt {
		inFlightAttempt.cancel()
	}
	delete(e.pollHealthPollers, identity)
	e.pollHealthMu.Unlock()
}

// RecordPollSkipped records that an expected poll was intentionally deferred.
// It advances only the in-memory supervision deadline and does not change
// persistent health or failure counts.
func (e *NotificationEngine) RecordPollSkipped(provider, accountID string) {
	identity := normalizePollIdentity(provider, accountID)
	e.pollHealthMu.Lock()
	defer e.pollHealthMu.Unlock()
	registration, ok := e.pollHealthPollers[identity]
	if !ok {
		return
	}
	registration.observedAt = e.now().UTC()
	e.pollHealthPollers[identity] = registration
}

// RecordPollFailure records one final scheduled-poll failure. Provider-level
// retries must complete before this method is called.
func (e *NotificationEngine) RecordPollFailure(provider, accountID, category, message string) {
	if isIntentionalPollCancellation(category, message) {
		return
	}
	identity := normalizePollIdentity(provider, accountID)
	now := e.now().UTC()

	e.pollHealthMu.Lock()
	registration, ok := e.pollHealthPollers[identity]
	if !ok {
		e.pollHealthMu.Unlock()
		return
	}
	registration.observedAt = now
	e.pollHealthPollers[identity] = registration

	state, err := e.loadRegisteredPollState(identity, registration)
	if err != nil {
		e.pollHealthMu.Unlock()
		return
	}
	completedAt := now
	claim := e.recordPollFailureLocked(context.Background(), state, "failing", 1, completedAt, PollFailure{
		Category: sanitizePollHealthText(category, maxPollErrorCategoryRunes),
		Message:  sanitizePollHealthText(message, maxPollErrorMessageRunes),
	})
	e.pollHealthMu.Unlock()
	e.dispatchPollDelivery(claim)
}

// RecordPollSuccess resolves an active incident and records the completed poll.
func (e *NotificationEngine) RecordPollSuccess(provider, accountID string) {
	identity := normalizePollIdentity(provider, accountID)
	now := e.now().UTC()

	e.pollHealthMu.Lock()
	registration, ok := e.pollHealthPollers[identity]
	if !ok {
		e.pollHealthMu.Unlock()
		return
	}
	registration.observedAt = now
	e.pollHealthPollers[identity] = registration

	state, err := e.loadRegisteredPollState(identity, registration)
	if err != nil {
		e.pollHealthMu.Unlock()
		return
	}
	wasUnhealthy := state.State != "healthy" || state.ConsecutiveFailures > 0
	if !wasUnhealthy {
		state.LastSuccessAt = timePointer(now)
		state.LastCompletedPollAt = timePointer(now)
		if err := e.store.UpsertPollHealthState(state); err != nil {
			e.logPollHealthStoreError("record success", identity, err)
		}
		e.pollHealthMu.Unlock()
		return
	}

	inFlightAttempt, hasInFlightAttempt := e.pollHealthAttempts[identity]
	if hasInFlightAttempt {
		inFlightAttempt.cancel()
	}

	firstFailureAt := state.FirstFailureAt
	failureCount := state.ConsecutiveFailures
	failureWasExternal := state.ExternalFailureDelivered
	if state.ActiveSystemAlertID != nil {
		if err := e.store.DismissSystemAlert(*state.ActiveSystemAlertID); err != nil {
			e.logPollHealthStoreError("dismiss failure alert", identity, err)
		}
	}

	state.State = "healthy"
	state.ConsecutiveFailures = 0
	state.FirstFailureAt = nil
	state.LastFailureAt = nil
	state.LastSuccessAt = timePointer(now)
	state.LastCompletedPollAt = timePointer(now)
	state.LastErrorCategory = ""
	state.LastErrorMessage = ""
	state.FirstExternalAlertAt = nil
	state.LastExternalAttemptAt = nil
	state.LastExternalSuccessAt = nil
	state.ExternalFailureDelivered = false
	state.ActiveSystemAlertID = nil
	if err := e.store.UpsertPollHealthState(state); err != nil {
		e.logPollHealthStoreError("record recovery", identity, err)
		e.pollHealthMu.Unlock()
		return
	}

	duration := time.Duration(0)
	if firstFailureAt != nil {
		duration = now.Sub(*firstFailureAt)
	}
	title := fmt.Sprintf("%s polling recovered", titleCase(identity.Provider))
	message := fmt.Sprintf("Polling recovered for account %s after %d failed or missed poll(s)",
		identity.AccountID, failureCount)
	if duration > 0 {
		message += fmt.Sprintf(" over %s", concisePollDuration(duration))
	}
	metadata := pollHealthMetadata(identity, "healthy", 0)
	if _, err := e.store.CreateSystemAlert(identity.Provider, "poll_recovered", title, message, "info", metadata); err != nil {
		e.logPollHealthStoreError("create recovery alert", identity, err)
	}

	var claim *pollDeliveryClaim
	cfg, _, _, _ := e.pollHealthDeliverySnapshot()
	if cfg.NotifyPollRecovery {
		subject := fmt.Sprintf("[RECOVERED] %s polling recovered", titleCase(identity.Provider))
		body := buildPollRecoveryBody(identity, failureCount, duration, now)
		if hasInFlightAttempt && inFlightAttempt.kind == "poll_failure" {
			inFlightAttempt.pendingRecovery = &pollPendingRecovery{
				previouslyDelivered: failureWasExternal,
				subject:             subject,
				body:                body,
			}
			e.pollHealthAttempts[identity] = inFlightAttempt
		} else if failureWasExternal {
			claim = e.claimPollDeliveryLocked(context.Background(), identity, "poll_recovered", time.Time{}, nil, subject, body)
		}
	}
	e.pollHealthMu.Unlock()
	e.dispatchPollDelivery(claim)
}

// EvaluatePollHealth detects missed intervals and sends due failure reminders.
func (e *NotificationEngine) EvaluatePollHealth() {
	e.evaluatePollHealth(context.Background())
}

func (e *NotificationEngine) evaluatePollHealth(ctx context.Context) {
	now := e.now().UTC()
	e.pollHealthMu.Lock()
	if ctx.Err() != nil {
		e.pollHealthMu.Unlock()
		return
	}
	var claims []*pollDeliveryClaim

	for identity, registration := range e.pollHealthPollers {
		if ctx.Err() != nil {
			e.pollHealthMu.Unlock()
			return
		}
		state, err := e.loadRegisteredPollState(identity, registration)
		if err != nil {
			continue
		}

		baseline := registration.registeredAt
		if registration.observedAt.After(baseline) {
			baseline = registration.observedAt
		}
		if state.LastCompletedPollAt != nil && state.LastCompletedPollAt.After(baseline) {
			baseline = *state.LastCompletedPollAt
		}

		missed, firstDeadline, lastDeadline := e.missedPollWindow(state, registration, baseline, now)
		if missed > 0 {
			if ctx.Err() != nil {
				e.pollHealthMu.Unlock()
				return
			}
			if claim := e.recordMissedPollsLocked(ctx, state, missed, firstDeadline, lastDeadline); claim != nil {
				claims = append(claims, claim)
			}
			continue
		}

		if state.State != "healthy" {
			if ctx.Err() != nil {
				e.pollHealthMu.Unlock()
				return
			}
			if claim := e.claimPollFailureDeliveryLocked(ctx, state, now); claim != nil {
				claims = append(claims, claim)
			}
		}
	}
	e.pollHealthMu.Unlock()

	for _, claim := range claims {
		e.dispatchPollDelivery(claim)
	}
}

func (e *NotificationEngine) missedPollWindow(
	state *store.PollHealthState,
	registration pollRegistration,
	baseline time.Time,
	now time.Time,
) (int, time.Time, time.Time) {
	deadline := baseline.Add(registration.interval + e.pollHealthGrace)
	if state.State == "stalled" && state.LastErrorCategory == "missed_poll" &&
		state.LastFailureAt != nil && !state.LastFailureAt.Before(baseline) {
		deadline = state.LastFailureAt.Add(registration.interval)
	}
	if now.Before(deadline) {
		return 0, time.Time{}, time.Time{}
	}
	missed := 1 + int(now.Sub(deadline)/registration.interval)
	lastDeadline := deadline.Add(time.Duration(missed-1) * registration.interval)
	return missed, deadline, lastDeadline
}

// RunPollHealthMonitor evaluates health until the application context stops.
func (e *NotificationEngine) RunPollHealthMonitor(ctx context.Context) {
	interval := e.pollHealthTick
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var evaluationDone <-chan struct{}
	for {
		select {
		case <-ctx.Done():
			if evaluationDone != nil {
				<-evaluationDone
			}
			return
		case <-evaluationDone:
			evaluationDone = nil
		case <-ticker.C:
			if evaluationDone != nil {
				continue
			}
			done := make(chan struct{})
			evaluationDone = done
			go func() {
				defer close(done)
				e.evaluatePollHealth(ctx)
			}()
		}
	}
}

func (e *NotificationEngine) recordPollFailureLocked(
	ctx context.Context,
	state *store.PollHealthState,
	healthState string,
	increment int,
	completedAt time.Time,
	failure PollFailure,
) *pollDeliveryClaim {
	if increment < 1 {
		return nil
	}
	now := e.now().UTC()
	wasHealthy := state.State == "healthy" || state.ConsecutiveFailures == 0
	if wasHealthy {
		state.FirstFailureAt = timePointer(now)
	}
	state.State = healthState
	state.ConsecutiveFailures += increment
	state.LastFailureAt = timePointer(now)
	state.LastCompletedPollAt = timePointer(completedAt)
	state.LastErrorCategory = failure.Category
	state.LastErrorMessage = failure.Message

	if err := e.upsertPollFailureAlertLocked(state); err != nil {
		e.logPollHealthStoreError("upsert failure alert", PollIdentity{state.Provider, state.AccountID}, err)
	}
	if err := e.store.UpsertPollHealthState(state); err != nil {
		e.logPollHealthStoreError("record failure", PollIdentity{state.Provider, state.AccountID}, err)
		return nil
	}
	return e.claimPollFailureDeliveryLocked(ctx, state, now)
}

func (e *NotificationEngine) recordMissedPollsLocked(
	ctx context.Context,
	state *store.PollHealthState,
	missed int,
	firstDeadline time.Time,
	lastDeadline time.Time,
) *pollDeliveryClaim {
	if missed < 1 {
		return nil
	}
	runPollHealthMutationHook(ctx, pollHealthBeforeMissedMutation)
	if ctx.Err() != nil {
		return nil
	}
	now := e.now().UTC()
	if state.State == "healthy" || state.ConsecutiveFailures == 0 {
		state.FirstFailureAt = timePointer(firstDeadline)
	}
	state.State = "stalled"
	state.ConsecutiveFailures += missed
	state.LastFailureAt = timePointer(lastDeadline)
	state.LastErrorCategory = "missed_poll"
	state.LastErrorMessage = fmt.Sprintf("No poll outcome reported for %d expected interval(s)", missed)

	identity := PollIdentity{Provider: state.Provider, AccountID: state.AccountID}
	if ctx.Err() != nil {
		return nil
	}
	if err := e.upsertPollFailureAlertLocked(state); err != nil {
		e.logPollHealthStoreError("upsert failure alert", identity, err)
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := e.store.UpsertPollHealthState(state); err != nil {
		e.logPollHealthStoreError("record missed polls", identity, err)
		return nil
	}
	return e.claimPollFailureDeliveryLocked(ctx, state, now)
}

func (e *NotificationEngine) upsertPollFailureAlertLocked(state *store.PollHealthState) error {
	identity := PollIdentity{Provider: state.Provider, AccountID: state.AccountID}
	stateLabel := "failed"
	if state.State == "stalled" {
		stateLabel = "missed"
	}
	title := fmt.Sprintf("%s polling %s", titleCase(identity.Provider), stateLabel)
	message := fmt.Sprintf("%d consecutive failed or missed poll(s) for account %s",
		state.ConsecutiveFailures, identity.AccountID)
	if state.LastErrorMessage != "" {
		message += ": " + state.LastErrorMessage
	}
	severity := "warning"
	if state.ConsecutiveFailures >= e.pollFailureThreshold() {
		severity = "error"
	}
	metadata := pollHealthMetadata(identity, state.State, state.ConsecutiveFailures)

	if state.ActiveSystemAlertID != nil {
		return e.store.UpdateSystemAlert(*state.ActiveSystemAlertID, title, message, severity, metadata)
	}
	id, err := e.store.CreateSystemAlert(identity.Provider, "poll_failure", title, message, severity, metadata)
	if err != nil {
		return err
	}
	state.ActiveSystemAlertID = &id
	return nil
}

func (e *NotificationEngine) claimPollFailureDeliveryLocked(
	ctx context.Context,
	state *store.PollHealthState,
	now time.Time,
) *pollDeliveryClaim {
	runPollHealthMutationHook(ctx, pollHealthBeforeDeliveryClaim)
	if ctx.Err() != nil {
		return nil
	}
	cfg, mailer, pushSender, discord := e.pollHealthDeliverySnapshot()
	threshold := cfg.PollFailureThreshold
	if threshold < 2 {
		threshold = 3
	}
	if !cfg.NotifyPollFailure || state.ConsecutiveFailures < threshold {
		return nil
	}
	// An external alert should mean the provider has been down long enough to
	// act on. The in-app alert is raised on the first failure regardless, so
	// the dashboard stays current while short blips stay silent.
	if cfg.PollFailureMinOutage > 0 && state.FirstFailureAt != nil &&
		now.Sub(*state.FirstFailureAt) < cfg.PollFailureMinOutage {
		return nil
	}
	identity := PollIdentity{Provider: state.Provider, AccountID: state.AccountID}
	if _, inFlight := e.pollHealthAttempts[identity]; inFlight {
		return nil
	}

	if state.ExternalFailureDelivered {
		repeat := cfg.PollFailureRepeat
		if repeat <= 0 {
			repeat = 6 * time.Hour
		}
		if state.LastExternalSuccessAt != nil && now.Sub(*state.LastExternalSuccessAt) < repeat {
			return nil
		}
		if state.LastExternalAttemptAt != nil && state.LastExternalSuccessAt != nil &&
			state.LastExternalAttemptAt.After(*state.LastExternalSuccessAt) {
			cooldown := cfg.Cooldown
			if cooldown <= 0 {
				cooldown = 30 * time.Minute
			}
			if now.Sub(*state.LastExternalAttemptAt) < cooldown {
				return nil
			}
		}
	} else if state.LastExternalAttemptAt != nil {
		cooldown := cfg.Cooldown
		if cooldown <= 0 {
			cooldown = 30 * time.Minute
		}
		if now.Sub(*state.LastExternalAttemptAt) < cooldown {
			return nil
		}
	}

	if ctx.Err() != nil {
		return nil
	}
	previousAttempt := cloneTimePointer(state.LastExternalAttemptAt)
	state.LastExternalAttemptAt = timePointer(now)
	subject := fmt.Sprintf("[POLL FAILURE] %s polling %s", titleCase(identity.Provider), state.State)
	body := buildPollFailureBody(identity, state, now)
	if err := e.store.UpsertPollHealthState(state); err != nil {
		e.logPollHealthStoreError("claim external delivery", identity, err)
		return nil
	}
	return e.claimPollDeliveryWithSnapshotLocked(
		ctx, identity, "poll_failure", dereferenceTime(state.FirstFailureAt), previousAttempt,
		cfg, mailer, pushSender, discord, subject, body,
	)
}

func (e *NotificationEngine) claimPollDeliveryLocked(
	ctx context.Context,
	identity PollIdentity,
	kind string,
	incident time.Time,
	previousAttempt *time.Time,
	subject, body string,
) *pollDeliveryClaim {
	cfg, mailer, pushSender, discord := e.pollHealthDeliverySnapshot()
	return e.claimPollDeliveryWithSnapshotLocked(
		ctx, identity, kind, incident, previousAttempt,
		cfg, mailer, pushSender, discord, subject, body,
	)
}

func (e *NotificationEngine) claimPollDeliveryWithSnapshotLocked(
	parent context.Context,
	identity PollIdentity,
	kind string,
	incident time.Time,
	previousAttempt *time.Time,
	cfg NotificationConfig,
	mailer *SMTPMailer,
	pushSender *PushSender,
	discord *DiscordSender,
	subject, body string,
) *pollDeliveryClaim {
	if _, exists := e.pollHealthAttempts[identity]; exists {
		return nil
	}
	e.pollHealthAttemptID++
	ctx, cancel := context.WithCancel(parent)
	attempt := pollDeliveryAttempt{
		id:          e.pollHealthAttemptID,
		kind:        kind,
		incident:    incident,
		attemptedAt: e.now().UTC(),
		cancel:      cancel,
	}
	e.pollHealthAttempts[identity] = attempt
	return &pollDeliveryClaim{
		identity:        identity,
		attempt:         attempt,
		ctx:             ctx,
		previousAttempt: cloneTimePointer(previousAttempt),
		cfg:             cfg,
		mailer:          mailer,
		pushSender:      pushSender,
		discord:         discord,
		subject:         subject,
		body:            body,
	}
}

func (e *NotificationEngine) executePollDelivery(claim *pollDeliveryClaim) {
	if claim == nil {
		return
	}
	sent := e.sendViaChannelsContext(
		claim.ctx,
		claim.mailer,
		claim.pushSender,
		claim.discord,
		claim.cfg.Channels,
		claim.subject,
		claim.body,
		"provider", claim.identity.Provider,
		"account", claim.identity.AccountID,
		"type", claim.attempt.kind,
	)
	deliveryCanceled := claim.ctx.Err() != nil
	claim.attempt.cancel()

	e.pollHealthMu.Lock()
	currentAttempt, ok := e.pollHealthAttempts[claim.identity]
	if !ok || currentAttempt.id != claim.attempt.id {
		e.pollHealthMu.Unlock()
		return
	}
	delete(e.pollHealthAttempts, claim.identity)
	if claim.attempt.kind != "poll_failure" {
		e.pollHealthMu.Unlock()
		return
	}

	state, err := e.store.GetPollHealthState(claim.identity.Provider, claim.identity.AccountID)
	if err != nil {
		e.logPollHealthStoreError("complete external delivery", claim.identity, err)
	} else if state != nil && state.State != "healthy" &&
		sameTimePointer(state.FirstFailureAt, claim.attempt.incident) {
		if deliveryCanceled && !sent {
			state.LastExternalAttemptAt = cloneTimePointer(claim.previousAttempt)
		} else if sent {
			if state.FirstExternalAlertAt == nil {
				state.FirstExternalAlertAt = timePointer(claim.attempt.attemptedAt)
			}
			state.LastExternalSuccessAt = timePointer(claim.attempt.attemptedAt)
			state.ExternalFailureDelivered = true
		}
		if err := e.store.UpsertPollHealthState(state); err != nil {
			e.logPollHealthStoreError("persist external delivery result", claim.identity, err)
		}
	}

	var recoveryClaim *pollDeliveryClaim
	pendingRecovery := currentAttempt.pendingRecovery
	if pendingRecovery != nil && (pendingRecovery.previouslyDelivered || sent) {
		recoveryClaim = e.claimPollDeliveryLocked(
			context.Background(),
			claim.identity,
			"poll_recovered",
			time.Time{},
			nil,
			pendingRecovery.subject,
			pendingRecovery.body,
		)
	}
	e.pollHealthMu.Unlock()
	e.executePollDelivery(recoveryClaim)
}

func (e *NotificationEngine) dispatchPollDelivery(claim *pollDeliveryClaim) {
	if claim == nil {
		return
	}
	if !e.pollHealthAsync {
		e.executePollDelivery(claim)
		return
	}
	e.pollHealthDeliveryWG.Add(1)
	go func() {
		defer e.pollHealthDeliveryWG.Done()
		e.executePollDelivery(claim)
	}()
}

// ShutdownPollHealthDeliveries cancels and joins all external poll-health sends.
// Call it only after every poller and the monitor have stopped producing work.
func (e *NotificationEngine) ShutdownPollHealthDeliveries() {
	e.pollHealthMu.Lock()
	for _, attempt := range e.pollHealthAttempts {
		attempt.cancel()
	}
	e.pollHealthMu.Unlock()
	e.pollHealthDeliveryWG.Wait()
}

func (e *NotificationEngine) loadRegisteredPollState(
	identity PollIdentity,
	registration pollRegistration,
) (*store.PollHealthState, error) {
	state, err := e.store.GetPollHealthState(identity.Provider, identity.AccountID)
	if err != nil {
		e.logPollHealthStoreError("load state", identity, err)
		return nil, err
	}
	if state == nil {
		state = newPollHealthState(identity)
	}
	state.IntervalSeconds = durationSecondsCeil(registration.interval)
	return state, nil
}

func (e *NotificationEngine) pollHealthDeliverySnapshot() (NotificationConfig, *SMTPMailer, *PushSender, *DiscordSender) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg, e.mailer, e.pushSender, e.discord
}

func (e *NotificationEngine) pollFailureThreshold() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.cfg.PollFailureThreshold < 2 {
		return 3
	}
	return e.cfg.PollFailureThreshold
}

func newPollHealthState(identity PollIdentity) *store.PollHealthState {
	return &store.PollHealthState{
		Provider:  identity.Provider,
		AccountID: identity.AccountID,
		State:     "healthy",
	}
}

func normalizePollIdentity(provider, accountID string) PollIdentity {
	provider = normalizeNotificationProvider(provider)
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		accountID = defaultPollHealthAccount
	}
	return PollIdentity{Provider: provider, AccountID: accountID}
}

func durationSecondsCeil(interval time.Duration) int64 {
	seconds := int64(interval / time.Second)
	if interval%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

func isIntentionalPollCancellation(category, message string) bool {
	category = strings.ToLower(strings.TrimSpace(category))
	message = strings.ToLower(strings.TrimSpace(message))
	return category == "shutdown" || category == "cancelled" || category == "canceled" ||
		strings.Contains(message, context.Canceled.Error())
}

func sanitizePollHealthText(value string, maxRunes int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	value = pollBearerSecret.ReplaceAllString(value, "Bearer [REDACTED]")
	value = pollHeaderSecret.ReplaceAllString(value, "$1: [REDACTED]")
	value = pollSchemeAssignmentSecret.ReplaceAllString(value, "$1=[REDACTED]")
	value = pollQuotedSecret.ReplaceAllString(value, "$1=[REDACTED]")
	value = pollNamedSecret.ReplaceAllString(value, "$1=[REDACTED]")
	value = pollCredentialPath.ReplaceAllString(value, "[credential path redacted]")
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes])
}

func pollHealthMetadata(identity PollIdentity, state string, count int) string {
	metadata, err := json.Marshal(map[string]any{
		"account_id": identity.AccountID,
		"provider":   identity.Provider,
		"state":      state,
		"count":      count,
	})
	if err != nil {
		return "{}"
	}
	return string(metadata)
}

func buildPollFailureBody(identity PollIdentity, state *store.PollHealthState, now time.Time) string {
	var body strings.Builder
	fmt.Fprintf(&body, "Provider: %s\n", titleCase(identity.Provider))
	fmt.Fprintf(&body, "Account: %s\n", identity.AccountID)
	fmt.Fprintf(&body, "State: %s\n", state.State)
	fmt.Fprintf(&body, "Consecutive failed or missed polls: %d\n", state.ConsecutiveFailures)
	if state.FirstFailureAt != nil {
		fmt.Fprintf(&body, "Outage duration: %s\n", concisePollDuration(now.Sub(*state.FirstFailureAt)))
	}
	if state.LastErrorCategory != "" {
		fmt.Fprintf(&body, "Error category: %s\n", state.LastErrorCategory)
	}
	if state.LastErrorMessage != "" {
		fmt.Fprintf(&body, "Latest error: %s\n", state.LastErrorMessage)
	}
	fmt.Fprintf(&body, "Time: %s\n\n-- Sent by onWatch", now.Format(time.RFC3339))
	return body.String()
}

func buildPollRecoveryBody(identity PollIdentity, failureCount int, duration time.Duration, now time.Time) string {
	var body strings.Builder
	fmt.Fprintf(&body, "Provider: %s\n", titleCase(identity.Provider))
	fmt.Fprintf(&body, "Account: %s\n", identity.AccountID)
	fmt.Fprintf(&body, "Polling recovered after %d failed or missed poll(s)", failureCount)
	if duration > 0 {
		fmt.Fprintf(&body, " over %s", concisePollDuration(duration))
	}
	fmt.Fprintf(&body, ".\nTime: %s\n\n-- Sent by onWatch", now.Format(time.RFC3339))
	return body.String()
}

func concisePollDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	return duration.Round(time.Second).String()
}

func timePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	return timePointer(*value)
}

func dereferenceTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func sameTimePointer(value *time.Time, expected time.Time) bool {
	return value != nil && value.Equal(expected)
}

func runPollHealthMutationHook(ctx context.Context, stage pollHealthMutationStage) {
	hook, _ := ctx.Value(pollHealthMutationHookKey{}).(func(pollHealthMutationStage))
	if hook != nil {
		hook(stage)
	}
}

func (e *NotificationEngine) logPollHealthStoreError(action string, identity PollIdentity, err error) {
	e.logger.Error("poll health store operation failed", "action", action, "error", err,
		"provider", identity.Provider, "account", identity.AccountID)
}
