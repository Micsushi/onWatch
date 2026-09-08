package api

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

func TestAgyProcessHelper(t *testing.T) {
	if os.Getenv("ONWATCH_AGY_PROCESS_HELPER") != "1" {
		return
	}
	time.Sleep(time.Minute)
}

func TestAgyTeardownReapsManagedProcess(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := pty.New()
	if err != nil {
		t.Fatal(err)
	}
	cmd := p.Command(executable, "-test.run=^TestAgyProcessHelper$")
	cmd.Env = append(os.Environ(), "ONWATCH_AGY_PROCESS_HELPER=1")
	if err := cmd.Start(); err != nil {
		_ = p.Close()
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, p) }()
	r := NewAntigravityCLIRunner(nil)
	defer r.Stop()
	r.sess = &agySession{pty: p, cmd: cmd}
	r.teardownLocked()
	if cmd.ProcessState == nil {
		// Reap the test child even when exercising the broken implementation.
		_ = cmd.Wait()
		t.Fatal("managed process was killed but never reaped")
	}
	if r.sess != nil {
		t.Fatal("managed session retained after teardown")
	}
}

func TestAgyCanceledFetchDoesNotResolveOrLaunch(t *testing.T) {
	t.Setenv("ANTIGRAVITY_CLI_PATH", "missing-agy-test-binary")
	r := NewAntigravityCLIRunner(nil)
	defer r.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Fetch(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled fetch should stop before resolving a binary, got %v", err)
	}
}

func TestAgyStoppedRunnerRejectsFetch(t *testing.T) {
	t.Setenv("ANTIGRAVITY_CLI_PATH", "missing-agy-test-binary")
	r := NewAntigravityCLIRunner(nil)
	r.Stop()
	if _, err := r.Fetch(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped runner should reject fetch, got %v", err)
	}
}

func TestAgySummaryPreservesWindows(t *testing.T) {
	b, e := os.ReadFile("testdata/agy_quota_summary.json")
	if e != nil {
		t.Fatal(e)
	}
	groups, e := parseAgyQuotaSummary(b)
	if e != nil {
		t.Fatal(e)
	}
	if len(groups) != 2 || len(groups[0].Buckets) != 2 || groups[0].Buckets[0].Window != "weekly" {
		t.Fatalf("lost summary: %+v", groups)
	}
	for _, b := range []string{`{}`, `{"response":{"groups":[]}}`, `{"response":{"groups":[{"buckets":[{"bucketId":"unknown"}]}]}}`} {
		if _, e := parseAgyQuotaSummary([]byte(b)); e == nil {
			t.Fatal("accepted unmeasured quota")
		}
	}
	nested := `{"response":{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-5h","remaining":{"remainingFraction":0.25}}]}]}}`
	groups, e = parseAgyQuotaSummary([]byte(nested))
	if e != nil || groups[0].Buckets[0].UsagePercent != 75 {
		t.Fatalf("nested: %+v %v", groups, e)
	}
}
func TestAgyManagedLive(t *testing.T) {
	if os.Getenv("ONWATCH_AGY_LIVE") != "1" {
		t.Skip("opt-in local quota probe")
	}
	// TestMain isolates home to protect credentials during ordinary tests.
	// Live quota reads require a deliberately selected, already signed-in home.
	liveHome := os.Getenv("ONWATCH_AGY_LIVE_HOME")
	if !filepath.IsAbs(liveHome) {
		t.Fatal("ONWATCH_AGY_LIVE_HOME must name an absolute, signed-in home")
	}
	t.Setenv("HOME", liveHome)
	t.Setenv("USERPROFILE", liveHome)
	r := NewAntigravityCLIRunner(nil)
	defer r.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s, e := r.Fetch(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if len(s.SummaryGroups) == 0 {
		t.Fatal("no measured quota groups")
	}
	pid := r.sess.cmd.Process.Pid
	if _, e := r.Fetch(ctx); e != nil {
		t.Fatal(e)
	}
	if r.sess.cmd.Process.Pid != pid {
		t.Fatal("did not reuse owned session")
	}
	t.Logf("measured %d quota groups; warm session reused", len(s.SummaryGroups))
}
