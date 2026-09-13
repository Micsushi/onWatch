package collector

import (
	"testing"
	"time"
)

func TestRetryDelayKeepsJitterWithinMaximum(t *testing.T) {
	for _, test := range []struct {
		attempt      int
		lower, upper time.Duration
	}{
		{0, 800 * time.Millisecond, 1200 * time.Millisecond},
		{1, 1600 * time.Millisecond, 2400 * time.Millisecond},
		{8, 204800 * time.Millisecond, 5 * time.Minute},
		{9, 4 * time.Minute, 5 * time.Minute},
		{100, 4 * time.Minute, 5 * time.Minute},
	} {
		seen := make(map[time.Duration]bool)
		for sample := 0; sample < 1000; sample++ {
			delay := retryDelay(test.attempt)
			if delay < test.lower || delay > test.upper {
				t.Fatalf("attempt %d: delay %s outside [%s, %s]", test.attempt, delay, test.lower, test.upper)
			}
			seen[delay] = true
		}
		if len(seen) < 2 {
			t.Fatalf("attempt %d: retry delay lost jitter", test.attempt)
		}
	}
}
