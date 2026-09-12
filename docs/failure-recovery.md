# Collector and dashboard failure recovery

A heartbeat proves that the collector reached the server. It does not prove
that usage collection or assigned quota polling succeeded. Compare the latest
accepted event and quota observation before trusting subscription estimates.

## Old source records

Live ingestion accepts observations up to 90 days old. An older archived
session previously stopped the source reader, blocking later usage and quota
polls. Source output grew while the upload queue appeared empty.

Rejected source records now go to `source-quarantine.jsonl` in the spool. Each
entry retains the source filename, offset, reason and exact bytes in base64.
The file is synchronized before advancing the source cursor. Failed preservation
leaves the cursor in place. These private records are not uploaded.

Valid records continue through deduplicating ingestion. Each pass processes at
most the configured upload batch size, with a minimum budget of 500 records,
then yields to uploads and heartbeats. Quota polls run despite usage errors.

Before installing a repair, retain a consistent backup of the spool, state,
source output and destination database. Preserve quarantine for the existing
history-import workflow with duplicate reconciliation. An empty upload queue
does not prove the source backlog or quarantine has been reconciled.

## Wake and comparison checks

- Antigravity wake commands exclude concurrent execution. Duplicate manual
  requests receive a conflict response. Printed connection errors remain
  failures even with a zero exit code. Discovery failures replace stale success
  status; failed results do not activate the success cooldown after restart.
- Entirely unpriced usage has no value multiple. Partial pricing is a lower
  bound, not zero spending or cash savings.
- Period totals retain historical plan activity. Calibration matches explicit
  event plans to meter plans; legacy account binding remains unverified.
- Hourly records overlapping interval boundaries suppress exact calibration.
- Quota observations over 30 minutes behind the selected period end are marked
  delayed. Refresh preserves drafts, unchanged charts and the latest response.
- Plan saves cannot overlap. Rejected requests do not silently save a plan.
- Subscription and theme scripts use content hashes. JavaScript revalidates
  even when a rebuild keeps the same app version string.

API-equivalent totals value recorded tokens at the stated price basis. They do
not establish invoice savings, work quality, another plan's entitlement or a
provider policy change. Restore collection coverage before comparing plans.

## Graph and interaction checks

- Cumulative cost samples retain their observed timestamps. Empty buckets do
  not create new observations. Period totals without timed samples stay in the
  summary and do not become a fabricated graph point.
- Historical API integration graphs accumulate the selected period. Current
  lifetime totals cannot establish a baseline for an older date range.
- Tokens-per-call aggregation weights each sample by its request count.
- Sonnet and Opus quota calibration uses only the corresponding models.
  Extra-spend and unknown scoped meters do not manufacture allowance estimates.
  Codex rollout meters retain historical plans even without a poll snapshot.
- Unknown model prices and partially priced multiples are explicit in the UI.
  The default allowance is selected by observation time, not quota-name order.
- Antigravity detail charts match both the group and reset window. History
  storage errors return a failed response so the dashboard retains its graph.
- Quota popups show loading, empty and failed states. Closed popups ignore late
  responses. Hidden charts and dialogs leave the layout and keyboard focus
  returns to the originating control after closing.
- Saving protects the form against overlapping edits, supports a zero-dollar
  fee and undo/redo, and restores keyboard focus. Refresh failures retain drafts.


## Repository audit repairs

Collector batches split before replayed event IDs, so the server can apply its
receipt deduplication. Acknowledged spool segments are reclaimed even when the
queue is empty. Newly created segment names are unique, preventing an old saved
cursor from skipping a replacement file after interruption. Append capacity
checks use file sizes instead of repeatedly decoding the pending queue. Duplicate
quota metric names are rejected per event before database insertion.

Portable exports read account metadata, history, settings and provenance through
one source database transaction. Backup checksums stream from the SQLite snapshot
without allocating the entire database. Password changes commit the new login
hash, transformed notification secrets and session revocation together. Failed
transformation or persistence leaves the previous credentials intact. Existing
raw and prefixed SMTP ciphertext is decrypted before rotation, and notification
sender configuration is refreshed using the new key.

Form login and API Basic Auth share failed-attempt limits. Concurrent password
checks are bounded. Direct-peer addresses identify clients; arbitrary forwarded
headers cannot select a different limiter identity. A proxy's clients therefore
share its peer limit unless an external authenticated gateway handles access.

Menubar pages expose only an empty shell before authentication. Data and settings
require a dashboard session or a scoped local companion credential. The companion
passes its credential in request headers; the initial popover URL uses a fragment
that is removed from browser history and retained only in that tab's session
storage. A password change invalidates the old companion credential; restart the
companion after changing the password. Loopback transport alone no longer grants
access through a same-host reverse proxy.

Self-update requires the release's SHA256SUMS manifest and rejects mismatched or
oversized downloads. Releases predating this manifest cannot self-update through
this path; install a verified new release through the normal installation flow.
A synchronized replacement is atomically renamed where supported. Windows uses
a unique backup with rollback if an executing image prevents replacement. Failed
swaps preserve the installed binary or identify the retained recovery backup.
Checksums detect corruption; they are not an independent release signature.

PID files have a companion instance identity recording the executable and process
creation time. Stop operations refuse an unrelated, reused or unverifiable PID
and preserve the PID file if a process remains alive. Legacy running instances
without identity metadata must be stopped through their service manager or
manually during the first upgrade. New startup refuses to overwrite a live
unverified instance. Native Windows checks use process handles and supported
termination rather than POSIX signals; scheduled collector removal ends and
checks the scheduled action before deleting the schedule.

Builds require Go 1.26.8 or newer. CI includes native Windows checks and a Go
vulnerability scan. Platform cross-compilation is not a substitute for native
service, tray and upgrade acceptance tests.
