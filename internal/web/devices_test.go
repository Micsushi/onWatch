package web

import (
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestCentralDeviceFreshnessBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	current := now.Add(-3 * time.Minute)
	delayed := now.Add(-15 * time.Minute)
	stale := now.Add(-15*time.Minute - time.Nanosecond)
	for name, test := range map[string]struct {
		heartbeat *time.Time
		want      string
	}{
		"never": {nil, "never"}, "current": {&current, "current"}, "delayed": {&delayed, "delayed"}, "stale": {&stale, "stale"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := centralDeviceState(store.Device{LastHeartbeatAt: test.heartbeat}, now); got != test.want {
				t.Fatalf("got %q want %q", got, test.want)
			}
		})
	}
}
