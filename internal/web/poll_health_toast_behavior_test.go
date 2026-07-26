package web

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestPollHealthToastQueueDisplaysWithoutGenericCollisions(t *testing.T) {
	t.Parallel()

	source := dashboardAppSource(t)
	dispatchSource := markedDashboardJavaScript(t, source, "// DASHBOARD TOAST DISPATCH", "// END DASHBOARD TOAST DISPATCH")
	queueSource := markedDashboardJavaScript(t, source, "// POLL HEALTH TOAST QUEUE", "// END POLL HEALTH TOAST QUEUE")

	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node is required for executable dashboard behavior tests: %v", err)
	}

	script := fmt.Sprintf(`
const POLL_HEALTH_SEEN_ALERTS_KEY = 'test-seen-alerts';
const POLL_HEALTH_SEEN_ALERT_LIMIT = 100;
const POLL_HEALTH_TOAST_QUEUE_LIMIT = 10;
const POLL_HEALTH_TOAST_DURATION_MS = 6500;
const DEFERRED_DASHBOARD_TOAST_QUEUE_LIMIT = 10;
const storage = new Map();
const writes = [];
const displayed = [];
const timers = [];
const localStorage = {
  getItem(key) { return storage.has(key) ? storage.get(key) : null; },
  setItem(key, value) { storage.set(key, value); },
};
const window = {
  setTimeout(fn, delay) {
    timers.push({ fn, delay });
    return timers.length;
  },
};
function readJSONStorage(target, key) {
  const value = target.getItem(key);
  return value ? JSON.parse(value) : null;
}
function writeJSONStorage(target, key, value) {
  target.setItem(key, JSON.stringify(value));
  writes.push([...value]);
}
function renderDashboardToast(message, type, timeoutMs) {
  displayed.push({ message, type, timeoutMs });
}
%s
%s
function assert(condition, message) {
  if (!condition) throw new Error(message);
}
const newestFirst = [
  { id: 2, type: 'poll_recovered', severity: 'info', message: 'recovered' },
  { id: 1, type: 'poll_failure', severity: 'warning', message: 'failed' },
];
showNewPollHealthAlertToasts(newestFirst);
assert(JSON.stringify(displayed.map(item => item.message)) === '["failed"]',
  'first display must be the oldest alert');
assert(JSON.stringify(writes) === '[]',
  'active alert must not persist before its protected display interval finishes');
assert(timers.length === 1 && timers[0].delay === POLL_HEALTH_TOAST_DURATION_MS,
  'queue must advance asynchronously after the toast duration');

showNewPollHealthAlertToasts(newestFirst);
assert(displayed.length === 1 && timers.length === 1,
  'refresh while queued must not enqueue or display duplicates');

timers.shift().fn();
assert(JSON.stringify(displayed.map(item => item.message)) === '["failed","recovered"]',
  'second alert must display after the first in chronological order');
assert(JSON.stringify(writes) === '[["1"]]',
  'first alert must persist only after its full display interval');

timers.shift().fn();
assert(JSON.stringify(writes) === '[["1"],["1","2"]]',
  'second alert must persist only after its full display interval');

showNewPollHealthAlertToasts([
  { id: 3, type: 'poll_failure', severity: 'error', message: 'protected poll failure' },
]);
showDashboardToast('generic refresh message', 'info', 2000);
assert(displayed[displayed.length - 1].message === 'protected poll failure',
  'generic toast must not overwrite an active poll-health toast');
assert(_deferredDashboardToastQueue.length === 1,
  'generic toast must wait in the deferred queue');
assert(JSON.stringify(writes) === '[["1"],["1","2"]]',
  'poll ID must remain unseen while its display interval is active');

timers.shift().fn();
assert(displayed[displayed.length - 1].message === 'generic refresh message',
  'deferred generic toast must display after poll-health work');
assert(JSON.stringify(writes) === '[["1"],["1","2"],["1","2","3"]]',
  'poll ID must persist after its uninterrupted display interval');

timers.shift().fn();
const largeBatch = Array.from({ length: 25 }, (_, index) => ({
  id: 100 + index,
  type: 'poll_failure',
  severity: 'warning',
  message: 'failure-' + index,
})).reverse();
showNewPollHealthAlertToasts(largeBatch);
assert(_pollHealthToastQueue.length + (_pollHealthToastActive ? 1 : 0) <= POLL_HEALTH_TOAST_QUEUE_LIMIT,
  'toast queue must remain bounded');
for (let index = 0; index < 25; index += 1) {
  showDashboardToast('generic-' + index, 'info', 1000);
}
assert(_deferredDashboardToastQueue.length <= DEFERRED_DASHBOARD_TOAST_QUEUE_LIMIT,
  'deferred generic queue must remain bounded');
`, dispatchSource, queueSource)

	if output, err := exec.Command(nodePath, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("poll-health toast behavior failed: %v\n%s", err, output)
	}
}

func markedDashboardJavaScript(t *testing.T, source, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	if start < 0 {
		t.Fatalf("%s not found", startMarker)
	}
	endOffset := strings.Index(source[start:], endMarker)
	if endOffset < 0 {
		t.Fatalf("%s not found", endMarker)
	}
	return source[start : start+endOffset+len(endMarker)]
}
