package api

import (
	"context"
	"os"
	"testing"
	"time"
)

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
