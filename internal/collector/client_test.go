package collector

import (
	"testing"
	"time"
)

func TestRetryDelayKeepsJitterWithinMaximum(t *testing.T) {
	for _, attempt := range []int{0, 1, 8, 9, 100} {
		base := time.Second << min(attempt, 9)
		if base > 5*time.Minute {
			base = 5 * time.Minute
		}
		lower, upper := base*8/10, base*12/10
		if upper > 5*time.Minute {
			upper = 5 * time.Minute
		}
		seen := make(map[time.Duration]bool)
		for sample := 0; sample < 1000; sample++ {
			delay := retryDelay(attempt)
			if delay < lower || delay > upper {
				t.Fatalf("attempt %d: delay %s outside [%s, %s]", attempt, delay, lower, upper)
			}
			seen[delay] = true
		}
		if len(seen) < 2 {
			t.Fatalf("attempt %d: retry delay lost jitter", attempt)
		}
	}
}
