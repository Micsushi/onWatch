package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/store"
)

type pollHealthDiscordCapture struct {
	mu       sync.Mutex
	messages []string
}

func newPollHealthDiscord(t *testing.T, status *atomic.Int32) (*DiscordSender, *pollHealthDiscordCapture) {
	t.Helper()
	capture := &pollHealthDiscordCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		capture.mu.Lock()
		capture.messages = append(capture.messages, string(body))
		capture.mu.Unlock()
		code := int(status.Load())
		if code == 0 {
			code = http.StatusNoContent
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(server.Close)
	return &DiscordSender{webhookURL: server.URL, client: server.Client()}, capture
}

func (c *pollHealthDiscordCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.messages)
}

func newPollHealthEngine(t *testing.T) (*NotificationEngine, *store.Store, *time.Time, *pollHealthDiscordCapture) {
	t.Helper()
	s := newTestStore(t)
	t.Cleanup(func() { _ = s.Close() })
	engine := newTestEngine(t, s)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return now }
	engine.pollHealthGrace = 0
	engine.pollHealthAsync = false
	engine.cfg.PollFailureThreshold = 3
	// These cases cover failure-count semantics, so the outage window is
	// disabled here. It has dedicated coverage in poll_health_min_outage_test.go.
	engine.cfg.PollFailureMinOutage = 0
	engine.cfg.PollFailureRepeat = 6 * time.Hour
	engine.cfg.NotifyPollFailure = true
	engine.cfg.NotifyPollRecovery = true
	engine.cfg.Channels = NotificationChannels{Discord: true}
	var status atomic.Int32
	status.Store(http.StatusNoContent)
	discord, capture := newPollHealthDiscord(t, &status)
	engine.discord = discord
	engine.RegisterPoller("codex", "acct-1", time.Minute)
	return engine, s, &now, capture
}

func pollState(t *testing.T, s *store.Store, provider, accountID string) *store.PollHealthState {
	t.Helper()
	state, err := s.GetPollHealthState(provider, accountID)
	if err != nil {
		t.Fatalf("GetPollHealthState: %v", err)
	}
	if state == nil {
		t.Fatalf("poll state %s/%s not found", provider, accountID)
	}
	return state
}

func TestRegisterPollerKeepsSupervisionWhenPersistenceUnavailable(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	engine := newTestEngine(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	engine.RegisterPoller("codex", "account-1", time.Minute)

	identity := normalizePollIdentity("codex", "account-1")
	engine.pollHealthMu.Lock()
	_, registered := engine.pollHealthPollers[identity]
	engine.pollHealthMu.Unlock()
	if !registered {
		t.Fatal("transient persistence failure disabled in-memory poll supervision")
	}
}

func TestPollHealthFirstFailureCreatesInAppOnly(t *testing.T) {
	engine, s, _, capture := newPollHealthEngine(t)
	engine.cfg.NotifyPollFailure = false

	engine.RecordPollFailure("codex", "acct-1", "auth", "token expired")

	state := pollState(t, s, "codex", "acct-1")
	if state.State != "failing" || state.ConsecutiveFailures != 1 {
		t.Fatalf("unexpected state: %+v", state)
	}
	if state.ActiveSystemAlertID == nil {
		t.Fatal("first failure did not create a system alert")
	}
	alerts, err := s.GetActiveSystemAlerts()
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].AlertType != "poll_failure" {
		t.Fatalf("unexpected alerts: %+v", alerts)
	}
	if capture.count() != 0 {
		t.Fatalf("first failure sent %d external messages", capture.count())
	}
}

func TestPollHealthExternalDeliveryDoesNotBlockPollOutcome(t *testing.T) {
	engine, _, _, _ := newPollHealthEngine(t)
	engine.cfg.PollFailureThreshold = 2
	engine.pollHealthAsync = true

	deliveryStarted := make(chan struct{})
	releaseDelivery := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(deliveryStarted)
		<-releaseDelivery
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	engine.discord = &DiscordSender{webhookURL: server.URL, client: server.Client()}

	engine.RecordPollFailure("codex", "acct-1", "network", "first")
	recorded := make(chan struct{})
	go func() {
		engine.RecordPollFailure("codex", "acct-1", "network", "second")
		close(recorded)
	}()

	select {
	case <-deliveryStarted:
	case <-time.After(time.Second):
		t.Fatal("external delivery did not start")
	}
	select {
	case <-recorded:
	case <-time.After(100 * time.Millisecond):
		close(releaseDelivery)
		t.Fatal("poll outcome waited for external delivery")
	}
	close(releaseDelivery)
	engine.ShutdownPollHealthDeliveries()
}

func TestPollHealthThresholdSendsExternalOnce(t *testing.T) {
	engine, s, _, capture := newPollHealthEngine(t)

	for range 3 {
		engine.RecordPollFailure("codex", "acct-1", "auth", "token expired")
	}
	engine.EvaluatePollHealth()

	if capture.count() != 1 {
		t.Fatalf("external messages = %d, want 1", capture.count())
	}
	state := pollState(t, s, "codex", "acct-1")
	if !state.ExternalFailureDelivered || state.LastExternalSuccessAt == nil {
		t.Fatalf("successful external delivery not persisted: %+v", state)
	}
	alerts, _ := s.GetActiveSystemAlerts()
	if len(alerts) != 1 || !strings.Contains(alerts[0].Message, "3 consecutive") {
		t.Fatalf("failure alert was not updated in place: %+v", alerts)
	}
}

func TestPollHealthSuccessResetsAndRecoveryRules(t *testing.T) {
	engine, s, _, capture := newPollHealthEngine(t)

	engine.RecordPollFailure("codex", "acct-1", "network", "temporary")
	engine.RecordPollSuccess("codex", "acct-1")
	if capture.count() != 0 {
		t.Fatalf("transient recovery sent externally: %d", capture.count())
	}
	state := pollState(t, s, "codex", "acct-1")
	if state.State != "healthy" || state.ConsecutiveFailures != 0 || state.ActiveSystemAlertID != nil {
		t.Fatalf("recovery did not reset state: %+v", state)
	}
	alerts, _ := s.GetActiveSystemAlerts()
	if len(alerts) != 1 || alerts[0].AlertType != "poll_recovered" {
		t.Fatalf("expected one active recovery alert, got %+v", alerts)
	}

	for range 3 {
		engine.RecordPollFailure("codex", "acct-1", "auth", "expired")
	}
	engine.RecordPollSuccess("codex", "acct-1")
	if capture.count() != 2 {
		t.Fatalf("announced failure and recovery messages = %d, want 2", capture.count())
	}
	state = pollState(t, s, "codex", "acct-1")
	if state.ExternalFailureDelivered {
		t.Fatalf("external delivery marker survived recovery: %+v", state)
	}
}

func TestPollHealthRepeatUsesSuccessfulDeliveryTime(t *testing.T) {
	engine, _, now, capture := newPollHealthEngine(t)
	for range 3 {
		engine.RecordPollFailure("codex", "acct-1", "auth", "expired")
	}
	*now = now.Add(6*time.Hour - time.Second)
	engine.EvaluatePollHealth()
	if capture.count() != 1 {
		t.Fatalf("message repeated early: %d", capture.count())
	}
	*now = now.Add(time.Second)
	engine.EvaluatePollHealth()
	if capture.count() != 2 {
		t.Fatalf("message did not repeat at 6h: %d", capture.count())
	}
}

func TestPollHealthRestartDoesNotDuplicateAlertOrExternalMessage(t *testing.T) {
	engine, s, now, capture := newPollHealthEngine(t)
	for range 3 {
		engine.RecordPollFailure("codex", "acct-1", "auth", "expired")
	}
	before := pollState(t, s, "codex", "acct-1")

	restarted := newTestEngine(t, s)
	restarted.now = func() time.Time { return *now }
	restarted.pollHealthGrace = 0
	restarted.cfg = engine.cfg
	restarted.discord = engine.discord
	restarted.RegisterPoller("codex", "acct-1", time.Minute)
	restarted.EvaluatePollHealth()

	after := pollState(t, s, "codex", "acct-1")
	if before.ActiveSystemAlertID == nil || after.ActiveSystemAlertID == nil ||
		*before.ActiveSystemAlertID != *after.ActiveSystemAlertID {
		t.Fatalf("restart replaced active alert: before=%+v after=%+v", before, after)
	}
	alerts, _ := s.GetActiveSystemAlerts()
	if len(alerts) != 1 {
		t.Fatalf("restart duplicated active alerts: %+v", alerts)
	}
	if capture.count() != 1 {
		t.Fatalf("restart duplicated external message: %d", capture.count())
	}
}

func TestPollHealthAccountsAreIsolated(t *testing.T) {
	engine, s, _, capture := newPollHealthEngine(t)
	engine.RegisterPoller("codex", "acct-2", time.Minute)

	for range 3 {
		engine.RecordPollFailure("codex", "acct-1", "auth", "one")
	}
	engine.RecordPollFailure("codex", "acct-2", "network", "two")

	one := pollState(t, s, "codex", "acct-1")
	two := pollState(t, s, "codex", "acct-2")
	if one.ConsecutiveFailures != 3 || two.ConsecutiveFailures != 1 {
		t.Fatalf("account state leaked: one=%+v two=%+v", one, two)
	}
	if one.ActiveSystemAlertID == nil || two.ActiveSystemAlertID == nil ||
		*one.ActiveSystemAlertID == *two.ActiveSystemAlertID {
		t.Fatalf("account alert IDs not isolated: one=%+v two=%+v", one, two)
	}
	if capture.count() != 1 {
		t.Fatalf("unexpected external message count: %d", capture.count())
	}
}

func TestPollHealthSkippedAndCancellationDoNotCount(t *testing.T) {
	engine, s, now, capture := newPollHealthEngine(t)

	engine.RecordPollSkipped("codex", "acct-1")
	engine.RecordPollFailure("codex", "acct-1", "shutdown", context.Canceled.Error())
	engine.EvaluatePollHealth()

	state := pollState(t, s, "codex", "acct-1")
	if state.ConsecutiveFailures != 0 || state.State != "healthy" {
		t.Fatalf("skip or cancellation counted: %+v", state)
	}
	engine.UnregisterPoller("codex", "acct-1")
	*now = now.Add(24 * time.Hour)
	engine.EvaluatePollHealth()
	if capture.count() != 0 {
		t.Fatalf("unregistered poller sent external messages: %d", capture.count())
	}
}

func TestPollHealthFailedDeliveryUsesCooldown(t *testing.T) {
	engine, s, now, _ := newPollHealthEngine(t)
	var attempts atomic.Int32
	var response atomic.Int32
	response.Store(http.StatusInternalServerError)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(int(response.Load()))
	}))
	t.Cleanup(server.Close)
	engine.discord = &DiscordSender{webhookURL: server.URL, client: server.Client()}
	engine.cfg.Cooldown = 30 * time.Minute

	for range 3 {
		engine.RecordPollFailure("codex", "acct-1", "auth", "expired")
	}
	if attempts.Load() != 1 {
		t.Fatalf("initial delivery attempts = %d, want 1", attempts.Load())
	}
	engine.EvaluatePollHealth()
	*now = now.Add(29 * time.Minute)
	engine.EvaluatePollHealth()
	if attempts.Load() != 1 {
		t.Fatalf("retried before cooldown: %d", attempts.Load())
	}
	response.Store(http.StatusNoContent)
	*now = now.Add(time.Minute + time.Nanosecond)
	engine.EvaluatePollHealth()
	if attempts.Load() != 2 {
		t.Fatalf("did not retry after cooldown: %d", attempts.Load())
	}
	state := pollState(t, s, "codex", "acct-1")
	if !state.ExternalFailureDelivered || state.LastExternalSuccessAt == nil {
		t.Fatalf("retry success not persisted: %+v", state)
	}
}

func TestPollHealthFailedRepeatUsesCooldownFromAttempt(t *testing.T) {
	engine, _, now, _ := newPollHealthEngine(t)
	var attempts atomic.Int32
	var response atomic.Int32
	response.Store(http.StatusNoContent)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(int(response.Load()))
	}))
	t.Cleanup(server.Close)
	engine.discord = &DiscordSender{webhookURL: server.URL, client: server.Client()}
	engine.cfg.Cooldown = 30 * time.Minute

	for range 3 {
		engine.RecordPollFailure("codex", "acct-1", "auth", "expired")
	}
	response.Store(http.StatusInternalServerError)
	*now = now.Add(6 * time.Hour)
	engine.EvaluatePollHealth()
	engine.EvaluatePollHealth()
	*now = now.Add(29 * time.Minute)
	engine.EvaluatePollHealth()
	if attempts.Load() != 2 {
		t.Fatalf("failed repeat retried before cooldown: %d", attempts.Load())
	}
	response.Store(http.StatusNoContent)
	*now = now.Add(time.Minute)
	engine.EvaluatePollHealth()
	if attempts.Load() != 3 {
		t.Fatalf("failed repeat did not retry after cooldown: %d", attempts.Load())
	}
}

func TestPollHealthBlockedDeliveryDoesNotHoldStateLockOrDuplicate(t *testing.T) {
	engine, s, _, _ := newPollHealthEngine(t)
	engine.RegisterPoller("codex", "acct-2", time.Minute)
	var attempts atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	engine.discord = &DiscordSender{webhookURL: server.URL, client: server.Client()}

	engine.RecordPollFailure("codex", "acct-1", "network", "one")
	engine.RecordPollFailure("codex", "acct-1", "network", "two")
	deliveryDone := make(chan struct{})
	go func() {
		engine.RecordPollFailure("codex", "acct-1", "network", "three")
		close(deliveryDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("external delivery did not start")
	}

	otherAccountDone := make(chan struct{})
	go func() {
		engine.RecordPollFailure("codex", "acct-2", "network", "independent")
		close(otherAccountDone)
	}()
	select {
	case <-otherAccountDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("blocked delivery held poll health lock for another account")
	}

	evaluateDone := make(chan struct{})
	go func() {
		engine.EvaluatePollHealth()
		close(evaluateDone)
	}()
	engine.RecordPollFailure("codex", "acct-1", "network", "four")
	select {
	case <-evaluateDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("concurrent evaluation blocked behind delivery")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("concurrent failure/evaluation duplicated delivery: %d attempts", got)
	}
	if got := pollState(t, s, "codex", "acct-2").ConsecutiveFailures; got != 1 {
		t.Fatalf("other account outcome was not recorded: %d", got)
	}

	close(release)
	select {
	case <-deliveryDone:
	case <-time.After(time.Second):
		t.Fatal("delivery did not finish after release")
	}
}

func TestPollHealthRecoveryFollowsPartialInFlightFailureDelivery(t *testing.T) {
	engine, s, _, _ := newPollHealthEngine(t)
	engine.cfg.Channels = NotificationChannels{Email: true, Discord: true}
	mailCount, closeSMTP := setupSMTPAndMailer(t, s, engine)
	defer closeSMTP()

	var discordAttempts atomic.Int32
	firstDiscordStarted := make(chan struct{})
	releaseFirstHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt := discordAttempts.Add(1)
		if attempt == 1 {
			close(firstDiscordStarted)
			<-releaseFirstHandler
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer func() {
		close(releaseFirstHandler)
		server.Close()
	}()
	engine.discord = &DiscordSender{webhookURL: server.URL, client: server.Client()}

	engine.RecordPollFailure("codex", "acct-1", "network", "one")
	engine.RecordPollFailure("codex", "acct-1", "network", "two")
	failureDeliveryDone := make(chan struct{})
	go func() {
		engine.RecordPollFailure("codex", "acct-1", "network", "three")
		close(failureDeliveryDone)
	}()
	select {
	case <-firstDiscordStarted:
	case <-time.After(time.Second):
		t.Fatal("failure delivery did not reach the blocked later channel")
	}
	if got := mailCount.Load(); got != 1 {
		t.Fatalf("first channel did not succeed before later channel blocked: %d", got)
	}

	recoveryRecorded := make(chan struct{})
	go func() {
		engine.RecordPollSuccess("codex", "acct-1")
		close(recoveryRecorded)
	}()
	select {
	case <-recoveryRecorded:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("recovery blocked behind in-flight failure delivery")
	}
	select {
	case <-failureDeliveryDone:
	case <-time.After(time.Second):
		t.Fatal("partial failure result did not complete and chain recovery")
	}

	if got := mailCount.Load(); got != 2 {
		t.Fatalf("external recovery was not sent after partial failure delivery: email count %d", got)
	}
	if got := discordAttempts.Load(); got != 2 {
		t.Fatalf("external recovery did not attempt all configured channels: Discord count %d", got)
	}
	state := pollState(t, s, "codex", "acct-1")
	if state.State != "healthy" || state.ExternalFailureDelivered {
		t.Fatalf("recovery did not leave clean healthy state: %+v", state)
	}
}

func TestPollHealthMonitorCancellationStopsBlockedDelivery(t *testing.T) {
	engine, s, _, _ := newPollHealthEngine(t)
	engine.cfg.NotifyPollFailure = false
	for range 3 {
		engine.RecordPollFailure("codex", "acct-1", "network", "provider unavailable")
	}
	engine.cfg.NotifyPollFailure = true
	engine.pollHealthTick = time.Millisecond

	started := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-releaseHandler
	}))
	defer func() {
		close(releaseHandler)
		server.Close()
	}()
	engine.discord = &DiscordSender{webhookURL: server.URL, client: server.Client()}

	ctx, cancel := context.WithCancel(context.Background())
	monitorDone := make(chan struct{})
	go func() {
		engine.RunPollHealthMonitor(ctx)
		close(monitorDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("monitor delivery did not start")
	}
	cancel()
	select {
	case <-monitorDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("monitor did not return promptly while delivery was blocked")
	}
	deadline := time.Now().Add(time.Second)
	for {
		engine.pollHealthMu.Lock()
		inFlight := len(engine.pollHealthAttempts)
		engine.pollHealthMu.Unlock()
		if inFlight == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled monitor left an in-flight attempt")
		}
		time.Sleep(time.Millisecond)
	}
	if got := pollState(t, s, "codex", "acct-1").LastExternalAttemptAt; got != nil {
		t.Fatalf("canceled delivery persisted a cooldown attempt: %v", got)
	}
}

func TestPollHealthCanceledEvaluationDoesNotMutateState(t *testing.T) {
	engine, s, now, _ := newPollHealthEngine(t)
	*now = now.Add(10 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	engine.evaluatePollHealth(ctx)

	state := pollState(t, s, "codex", "acct-1")
	if state.State != "healthy" || state.ConsecutiveFailures != 0 ||
		state.FirstFailureAt != nil || state.LastFailureAt != nil {
		t.Fatalf("canceled evaluation mutated poll state: %+v", state)
	}
	alerts, err := s.GetActiveSystemAlerts()
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 0 {
		t.Fatalf("canceled evaluation created alerts: %+v", alerts)
	}
}

func TestPollHealthCancellationBeforeMissedMutationPersistsNothing(t *testing.T) {
	engine, s, now, _ := newPollHealthEngine(t)
	*now = now.Add(10 * time.Minute)
	baseCtx, cancel := context.WithCancel(context.Background())
	ctx := context.WithValue(baseCtx, pollHealthMutationHookKey{}, func(stage pollHealthMutationStage) {
		if stage == pollHealthBeforeMissedMutation {
			cancel()
		}
	})

	engine.evaluatePollHealth(ctx)

	state := pollState(t, s, "codex", "acct-1")
	if state.State != "healthy" || state.ConsecutiveFailures != 0 ||
		state.FirstFailureAt != nil || state.LastFailureAt != nil {
		t.Fatalf("cancellation at missed-mutation boundary persisted state: %+v", state)
	}
	alerts, err := s.GetActiveSystemAlerts()
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 0 {
		t.Fatalf("cancellation at missed-mutation boundary created alerts: %+v", alerts)
	}
}

func TestPollHealthCancellationBeforeDeliveryClaimPersistsNoAttempt(t *testing.T) {
	engine, s, _, capture := newPollHealthEngine(t)
	engine.cfg.NotifyPollFailure = false
	for range 3 {
		engine.RecordPollFailure("codex", "acct-1", "network", "provider unavailable")
	}
	engine.cfg.NotifyPollFailure = true

	baseCtx, cancel := context.WithCancel(context.Background())
	ctx := context.WithValue(baseCtx, pollHealthMutationHookKey{}, func(stage pollHealthMutationStage) {
		if stage == pollHealthBeforeDeliveryClaim {
			cancel()
		}
	})
	engine.evaluatePollHealth(ctx)

	state := pollState(t, s, "codex", "acct-1")
	if state.LastExternalAttemptAt != nil || state.ExternalFailureDelivered {
		t.Fatalf("cancellation at delivery-claim boundary persisted an attempt: %+v", state)
	}
	if capture.count() != 0 {
		t.Fatalf("cancellation at delivery-claim boundary sent externally: %d", capture.count())
	}
}

func TestPollHealthMissedIntervalsWarnAndEscalate(t *testing.T) {
	engine, s, now, capture := newPollHealthEngine(t)

	*now = now.Add(time.Minute)
	engine.EvaluatePollHealth()
	state := pollState(t, s, "codex", "acct-1")
	if state.State != "stalled" || state.ConsecutiveFailures != 1 {
		t.Fatalf("first missed interval not recorded: %+v", state)
	}
	if capture.count() != 0 {
		t.Fatalf("first missed interval sent externally: %d", capture.count())
	}
	firstDeadline := *now
	if state.FirstFailureAt == nil || !state.FirstFailureAt.Equal(firstDeadline) {
		t.Fatalf("first missed deadline = %v, want %v", state.FirstFailureAt, firstDeadline)
	}
	if state.LastCompletedPollAt != nil {
		t.Fatalf("missed poll fabricated a completed outcome: %v", state.LastCompletedPollAt)
	}

	*now = now.Add(2 * time.Minute)
	engine.EvaluatePollHealth()
	state = pollState(t, s, "codex", "acct-1")
	if state.ConsecutiveFailures != 3 || capture.count() != 1 {
		t.Fatalf("missed escalation failed: state=%+v messages=%d", state, capture.count())
	}
	if state.LastFailureAt == nil || !state.LastFailureAt.Equal(*now) {
		t.Fatalf("latest missed deadline = %v, want %v", state.LastFailureAt, *now)
	}
	engine.EvaluatePollHealth()
	if got := pollState(t, s, "codex", "acct-1").ConsecutiveFailures; got != 3 {
		t.Fatalf("same evaluation time double-counted missed polls: %d", got)
	}
}

func TestPollHealthDelayedEvaluationPreservesTrueLastCompletedPoll(t *testing.T) {
	engine, s, now, capture := newPollHealthEngine(t)
	engine.RecordPollSuccess("codex", "acct-1")
	trueCompletedAt := *now

	*now = now.Add(3 * time.Minute)
	engine.EvaluatePollHealth()

	state := pollState(t, s, "codex", "acct-1")
	if state.ConsecutiveFailures != 3 || capture.count() != 1 {
		t.Fatalf("delayed catch-up failed: state=%+v messages=%d", state, capture.count())
	}
	wantFirst := trueCompletedAt.Add(time.Minute)
	wantLast := trueCompletedAt.Add(3 * time.Minute)
	if state.FirstFailureAt == nil || !state.FirstFailureAt.Equal(wantFirst) {
		t.Fatalf("first missed deadline = %v, want %v", state.FirstFailureAt, wantFirst)
	}
	if state.LastFailureAt == nil || !state.LastFailureAt.Equal(wantLast) {
		t.Fatalf("latest missed deadline = %v, want %v", state.LastFailureAt, wantLast)
	}
	if state.LastCompletedPollAt == nil || !state.LastCompletedPollAt.Equal(trueCompletedAt) {
		t.Fatalf("true completed poll changed: got %v want %v", state.LastCompletedPollAt, trueCompletedAt)
	}
	engine.EvaluatePollHealth()
	if got := pollState(t, s, "codex", "acct-1").ConsecutiveFailures; got != 3 {
		t.Fatalf("delayed evaluation double-counted missed polls: %d", got)
	}
}

func TestPollHealthRestartDoesNotDoubleCountMissedPolls(t *testing.T) {
	engine, s, now, _ := newPollHealthEngine(t)
	engine.RecordPollSuccess("codex", "acct-1")
	trueCompletedAt := *now
	*now = now.Add(3 * time.Minute)
	engine.EvaluatePollHealth()

	restarted := newTestEngine(t, s)
	restarted.now = func() time.Time { return *now }
	restarted.pollHealthGrace = 0
	restarted.cfg = engine.cfg
	restarted.discord = engine.discord
	restarted.RegisterPoller("codex", "acct-1", time.Minute)
	restarted.EvaluatePollHealth()
	if got := pollState(t, s, "codex", "acct-1").ConsecutiveFailures; got != 3 {
		t.Fatalf("restart double-counted old missed polls: %d", got)
	}

	*now = now.Add(time.Minute)
	restarted.EvaluatePollHealth()
	state := pollState(t, s, "codex", "acct-1")
	if state.ConsecutiveFailures != 4 {
		t.Fatalf("new post-restart missed poll not counted once: %+v", state)
	}
	if state.LastCompletedPollAt == nil || !state.LastCompletedPollAt.Equal(trueCompletedAt) {
		t.Fatalf("restart catch-up changed true completed outcome: got %v want %v",
			state.LastCompletedPollAt, trueCompletedAt)
	}
}

func TestPollHealthRestartPreservesMissedDeadlineCadenceWithGrace(t *testing.T) {
	engine, s, now, _ := newPollHealthEngine(t)
	engine.pollHealthGrace = 10 * time.Second
	*now = now.Add(time.Minute + 10*time.Second)
	engine.EvaluatePollHealth()

	restarted := newTestEngine(t, s)
	restarted.now = func() time.Time { return *now }
	restarted.pollHealthGrace = 10 * time.Second
	restarted.cfg = engine.cfg
	restarted.discord = engine.discord
	restarted.RegisterPoller("codex", "acct-1", time.Minute)

	*now = now.Add(time.Minute - time.Nanosecond)
	restarted.EvaluatePollHealth()
	if got := pollState(t, s, "codex", "acct-1").ConsecutiveFailures; got != 1 {
		t.Fatalf("post-restart missed poll counted before deadline: %d", got)
	}
	*now = now.Add(time.Nanosecond)
	restarted.EvaluatePollHealth()
	state := pollState(t, s, "codex", "acct-1")
	if state.ConsecutiveFailures != 2 || state.LastFailureAt == nil || !state.LastFailureAt.Equal(*now) {
		t.Fatalf("post-restart missed deadline cadence shifted: %+v", state)
	}
}

func TestPollHealthSanitizesAndTruncatesStoredError(t *testing.T) {
	engine, s, _, _ := newPollHealthEngine(t)
	secret := `Bearer bearer-secret token=plain-secret "access_token":"json-secret" ` +
		`client_secret='client-secret' Cookie: session-secret session_id=session-id ` +
		`auth_key=auth-secret Set-Cookie: cookie-secret password=pw-secret `
	engine.RecordPollFailure("codex", "acct-1", "AUTHORIZATION", secret+strings.Repeat("x", 1000))

	state := pollState(t, s, "codex", "acct-1")
	for _, leaked := range []string{
		"bearer-secret", "plain-secret", "json-secret", "client-secret",
		"session-secret", "session-id", "auth-secret", "cookie-secret", "pw-secret",
	} {
		if strings.Contains(state.LastErrorMessage, leaked) {
			t.Fatalf("stored error leaked %q: %q", leaked, state.LastErrorMessage)
		}
	}
	if len(state.LastErrorMessage) > 512 {
		t.Fatalf("stored error was not truncated: %d", len(state.LastErrorMessage))
	}
	if len(state.LastErrorCategory) > 64 {
		t.Fatalf("stored category was not truncated: %d", len(state.LastErrorCategory))
	}
	alerts, err := s.GetActiveSystemAlerts()
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"bearer-secret", "plain-secret", "json-secret", "client-secret"} {
		if len(alerts) != 1 || strings.Contains(alerts[0].Message, leaked) {
			t.Fatalf("in-app alert leaked %q: %+v", leaked, alerts)
		}
	}
}

func TestPollHealthSanitizerRedactsCommonSecretFormats(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		secret string
	}{
		{name: "plain token", input: "token=plain-token", secret: "plain-token"},
		{name: "quoted JSON token", input: `"token":"json-token"`, secret: "json-token"},
		{name: "client secret", input: "client_secret='client-value'", secret: "client-value"},
		{name: "authorization header", input: "Authorization: Basic auth-value", secret: "auth-value"},
		{name: "authorization assignment", input: "authorization=Basic assignment-value", secret: "assignment-value"},
		{name: "proxy authorization", input: "proxy_authorization = Bearer proxy-value", secret: "proxy-value"},
		{name: "cookie header", input: "Cookie: cookie-value", secret: "cookie-value"},
		{name: "session key", input: "session_id=session-value", secret: "session-value"},
		{name: "auth key", input: "auth_key=key-value", secret: "key-value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := sanitizePollHealthText(test.input, maxPollErrorMessageRunes)
			if strings.Contains(got, test.secret) {
				t.Fatalf("sanitizer leaked %q in %q", test.secret, got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("sanitizer did not mark redaction: %q", got)
			}
		})
	}
}

func TestPollHealthAssignmentCredentialsNeverReachStateOrAlert(t *testing.T) {
	engine, s, _, _ := newPollHealthEngine(t)
	message := "request failed authorization=Basic c2VjcmV0 proxy_authorization = Bearer proxy-secret"
	engine.RecordPollFailure("codex", "acct-1", "auth", message)

	state := pollState(t, s, "codex", "acct-1")
	alerts, err := s.GetActiveSystemAlerts()
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"c2VjcmV0", "proxy-secret"} {
		if strings.Contains(state.LastErrorMessage, secret) {
			t.Fatalf("persistent state leaked %q: %q", secret, state.LastErrorMessage)
		}
		if len(alerts) != 1 || strings.Contains(alerts[0].Message, secret) {
			t.Fatalf("in-app alert leaked %q: %+v", secret, alerts)
		}
	}
}

func TestPollHealthAttemptsEveryEnabledConfiguredChannel(t *testing.T) {
	engine, s, _, _ := newPollHealthEngine(t)
	engine.cfg.Channels = NotificationChannels{Email: true, Push: true, Discord: true}

	mailCount, closeSMTP := setupSMTPAndMailer(t, s, engine)
	defer closeSMTP()

	var pushCount atomic.Int32
	pushServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pushCount.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer pushServer.Close()
	engine.pushSender = makeTLSPushSender(t, pushServer)
	saveTestSubscription(t, s, pushServer)

	var discordStatus atomic.Int32
	discordStatus.Store(http.StatusNoContent)
	discord, discordCapture := newPollHealthDiscord(t, &discordStatus)
	engine.discord = discord

	for range 3 {
		engine.RecordPollFailure("codex", "acct-1", "network", "provider unavailable")
	}

	if got := mailCount.Load(); got != 1 {
		t.Fatalf("email attempts = %d, want 1", got)
	}
	if got := pushCount.Load(); got != 1 {
		t.Fatalf("push attempts = %d, want 1", got)
	}
	if got := discordCapture.count(); got != 1 {
		t.Fatalf("Discord attempts = %d, want 1", got)
	}
}

func TestPollHealthMonitorStopsWithContext(t *testing.T) {
	engine, _, _, _ := newPollHealthEngine(t)
	engine.pollHealthTick = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		engine.RunPollHealthMonitor(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poll health monitor did not stop after cancellation")
	}
}
