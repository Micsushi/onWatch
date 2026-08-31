# Central Hosting Owner Test Backlog

Record the exact live onWatch SHA, Ansible SHA, image ID, and test date before testing. Reopen any failed item after a new deployment and retest it plus its dependent checks.

Each check starts `Not run`. Use `Passed`, `Failed`, or `Capability unavailable` with notes.

## Browser and graphs

- State: Not run. Prerequisite: authenticated live dashboard. Steps: open `onwatch.mshi.ca`, log out, log in, refresh. Expected: Access and built-in login both work; logout ends the session.
- State: Not run. Prerequisite: at least seven days of quota history. Steps: check cumulative and per-period at 1h, 6h, 24h, 7d, 30d, all, and custom. Expected: no clipping, false bridges, invented zeros, or hidden resets.
- State: Not run. Prerequisite: cost events. Steps: repeat all ranges and modes on cost graphs. Expected: totals match tables and range boundaries.
- State: Not run. Prerequisite: desktop and phone. Steps: inspect at desktop and 390px. Expected: device status and graphs fit without horizontal clipping.

## Devices

- State: Not run. Prerequisite: macOS device token. Steps: install collector, sleep, wake, disconnect network, reconnect. Expected: status moves current to delayed or stale, queued events remain, and drain once.
- State: Not run. Prerequisite: Windows device. Steps: install task in a path containing spaces and inspect token ACL. Expected: collector restarts at login, token is user-only, uninstall preserves spool.
- State: Not run. Prerequisite: enrolled test device. Steps: rotate token, try old token, then new token. Expected: old rejected, replacement accepted, token shown only once.
- State: Not run. Prerequisite: enrolled test device. Steps: revoke it. Expected: new writes stop, old history and device row remain.

## Data and recovery

- State: Not run. Prerequisite: portable archive. Steps: dry-run, apply, apply again. Expected: dry-run does not write; second apply does not duplicate totals.
- State: Not run. Prerequisite: live backup. Steps: create backup and complete isolated restore drill. Expected: integrity, devices, receipts, revocation, ownership, settings, and representative totals match.
- State: Not run. Prerequisite: rollback window. Steps: simulate central failure and follow rollback commands. Expected: old runtime returns without double polling; queued events later reconcile once.

## Settings and alerts

- State: Not run. Steps: change a reversible dashboard setting from one device and inspect another. Expected: central value is shared and undo remains available where the setting supports it.
- State: Not run. Steps: trigger a safe test notification and stale collector condition. Expected: one clear alert, no repeated storm, no credential in logs or metrics.
