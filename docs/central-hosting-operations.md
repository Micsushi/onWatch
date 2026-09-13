# Central Hosting Operations

This runbook moves onWatch from independent full daemons to one canonical Server2 service. Devices run collector-only mode. The server owns SQLite, imports, exports, settings, alerts, and graphs.

## Security boundary

- The dashboard listens on container port 9211, published only to loopback port 9213. Tailscale Serve HTTPS 9444 carries dashboard traffic to the existing Server1 Authelia gateway at `onwatch.mshi.ca`. The optional direct Cloudflare Tunnel route requires its own access policy.
- Authelia and onWatch login both remain enabled. `ONWATCH_TRUST_PROXY_AUTH` stays false because other containers share the Docker network.
- Ingest listens on container port 9212 and is published only to `127.0.0.1:9212`. Tailscale Serve exposes HTTPS 9443 to tailnet peers.
- Every ingest request still requires a device ID and bearer token. Only the SHA-256 token digest is stored.
- Provider credentials remain on the one device assigned to poll that account. They are never uploaded.

## Server2 deployment

Prerequisites: Docker, the external `homelab` network, the existing Server1 Traefik/Authelia gateway, Tailscale, and a checked-out exact release SHA.

1. Create `/srv/onwatch/data`, `/srv/onwatch/backups`, `/srv/onwatch/config`, and `/srv/onwatch/release`. Set data and backup ownership to UID/GID 65532.
2. Copy `deploy/server2/onwatch.env.example` to `/srv/onwatch/config/onwatch.env`. Replace every placeholder. Set mode 0600.
3. Set `ONWATCH_RELEASE_SHA` and `ONWATCH_IMAGE_TAG` to the exact full Git SHA. Do not use `latest`.
4. Validate and start:

   ```sh
   docker network inspect homelab
   docker compose --env-file /srv/onwatch/config/deploy.env -f deploy/server2/docker-compose.yml config
   docker compose --env-file /srv/onwatch/config/deploy.env -f deploy/server2/docker-compose.yml up -d --build
   docker inspect --format '{{.State.Health.Status}}' onwatch
   docker inspect --format '{{index .Config.Labels "com.mshi.onwatch.revision"}}' onwatch
   ```

Expected within 90 seconds: health is `healthy` and the revision equals the pushed main SHA. Reversal: run the same Compose command from the prior release checkout with its prior SHA.

5. Use the existing Authelia gateway procedure below to forward `onwatch.mshi.ca` to Server2. Keep the dashboard host port bound to loopback.
6. Preflight Tailscale Serve before mutation:

   ```sh
   tailscale serve status --json > /tmp/onwatch-tailscale-before.json
   tailscale serve --bg --https=9443 http://127.0.0.1:9212
   tailscale serve status
   ```

If 9443 already belongs to another service, stop. Do not reset or replace unrelated Serve routes. Reversal: `tailscale serve --https=9443 off`.

## Device enrollment

On Server2:

```sh
docker exec onwatch /app/onwatch device create --name "MacBook" --platform darwin
```

Record the device ID and one-time token in the password manager. On the device, write only the token to `~/.onwatch/collector.token` and set mode 0600.

```sh
mkdir -p ~/.onwatch
chmod 700 ~/.onwatch
printf '%s\n' 'ONE_TIME_TOKEN' > ~/.onwatch/collector.token
chmod 600 ~/.onwatch/collector.token
onwatch collector install \
  --server https://server2-tailnet-name:9443 \
  --device-id dev_0123456789abcdef0123456789abcdef \
  --token-file ~/.onwatch/collector.token
onwatch collector status --json
```

`collector install` uses the distinct `dev.onllm.onwatch.collector` LaunchAgent on macOS and `onWatch Collector` Scheduled Task on Windows. It does not replace the full daemon. Windows installation restricts the token file ACL to the current user. `collector uninstall` preserves queued events unless `--purge-spool` is explicitly supplied.

Linux uses the [native user service](../../deploy/linux/README.md). Install it
under the user who owns the usage logs, including on Server2 when that host also
runs coding tools. The central container itself needs no provider credentials.

## Account poll ownership

Assign exactly one owner per provider account:

```sh
docker exec onwatch /app/onwatch device assign --provider codex --account PROVIDER_EXTERNAL_ID --owner device --device-id DEVICE_ID --credential-alias ONWATCH_CODEX_WORK_HOME --poll-interval 60s
docker exec onwatch /app/onwatch device owners --json
```

Use `--owner server` when Server2 has the credential. To transfer ownership, first stop the old poller, then unassign, then assign the new owner. There is no automatic failover. `device unassign` preserves history and removes the assignment from the device heartbeat configuration.

For Codex, use the credential's external account identity, preferably the
account/user composite used by the existing imported profile. Set the named
credential alias in the device's `~/.onwatch/.env` to that account's `CODEX_HOME`
directory. The collector reads the owner's current access token and verifies the
identity on each poll. It does not rotate a shared refresh grant. Numeric local
database IDs remain readable for legacy history, but are not credential identities.

The server evaluates fresh collector quotas every 15 seconds from a persistent
cursor. Offline history and superseded samples do not generate live alerts.

Rotation prints a replacement token once and invalidates the prior token:

```sh
docker exec onwatch /app/onwatch device rotate --device-id DEVICE_ID
```

Revocation is immediate and does not delete history:

```sh
docker exec onwatch /app/onwatch device revoke --device-id DEVICE_ID
```

## Migration and transfer

Portable archives are additive history transfer, not continuous sync. Export each existing database once, retain a raw recoverable backup, and dry-run every archive against central before applying it.

```sh
onwatch data export --out device-history.onwatch.zip
docker cp device-history.onwatch.zip onwatch:/tmp/device-history.onwatch.zip
docker exec onwatch /app/onwatch data import --dry-run /tmp/device-history.onwatch.zip
docker exec onwatch /app/onwatch data import /tmp/device-history.onwatch.zip
docker exec onwatch /app/onwatch data import /tmp/device-history.onwatch.zip
```

The second apply must report only skipped records or safe metadata updates. Central settings win. Archives exclude enrollment and provider credentials.

History imports preserve event fingerprints so subsequent collector replay does not count the same usage twice. Imports also recognize proven legacy fingerprint aliases, preserve their receipts and origin mappings, and reject conflicting account, token, or observation identities. Repeated subscription meter readings keep the existing central observation.

An empty collector queue does not prove complete historical coverage: normal initial scans skip unchanged agent logs older than six hours. For migration, run `ONWATCH_AGENT_USAGE_INITIAL_BACKFILL=1 onwatch agent-usage --once --out PRIVATE_STAGING_DIRECTORY` on each device and reconcile that output with its existing database exports. Keep staging separate from the live spool. Events older than the live ingest age limit belong in historical transfer, not live replay.

Raw records and hourly summaries are alternate representations of the same usage. If an import reports overlap, retain both source backups and compare origin mappings, exact fingerprints, request counts, and every token total before selecting a representation. Do not bypass the overlap check or sum both representations. Keep a recoverable preimage and rehearse any reconciliation on an isolated central backup first.

## Backup, retention, and restore

Create an online SQLite snapshot. Never copy the live database, WAL, and SHM files separately.

```sh
docker exec onwatch /app/onwatch backup --out /backups/onwatch-$(date -u +%Y%m%dT%H%M%SZ).db
```

The command verifies SQLite integrity, writes a SHA-256 metadata sidecar, and refuses the live database path. Server2 keeps 14 daily and 8 weekly generations. At least the newest verified generation is copied to an operator-selected off-host target. Server1 is not required. Off-host copy failure must not prune local backups.

Restore rehearsal uses a new isolated directory and port. It never overwrites `/srv/onwatch/data`:

```sh
mkdir -p /srv/onwatch/restore-drill
cp /srv/onwatch/backups/SELECTED.db /srv/onwatch/restore-drill/onwatch.db
ONWATCH_DB_PATH=/srv/onwatch/restore-drill/onwatch.db ONWATCH_PORT=19211 ONWATCH_INGEST_PORT=19212 ./onwatch --debug
./onwatch healthcheck --url http://127.0.0.1:19211/healthz
```

Verify history, settings, devices, receipts, revocations, ownership, export, and one new fixture event. Stop the isolated process after proof.

## Graph and freshness behavior

All graph timestamps use observation `captured_at` in UTC. Missing observations render as gaps after the larger of three expected intervals or one chart bucket. Resets remain visible as drops. The graph does not smooth, bridge, or invent zero usage. Delayed and stale collectors are shown next to the graph.

Codex and Antigravity cards show the age of their last successful quota snapshot
and mark it stale after 30 minutes. The last measured percentage remains visible;
it is not reset to zero or presented as a newly measured value. Central Codex
ingestion replaces legacy generated `central:` display names with short account
labels on the next accepted snapshot, preserving external IDs, history, and
custom names.

Check cumulative and per-period modes at 1h, 6h, 24h, 7d, 30d, all, and custom ranges for both quota usage and cost. Cost events may arrive from multiple devices. Quota series accept only the configured owner.

## Production cutover

Complete `docs/central-cutover-manifest.example` privately before starting.

1. Target: every old device and any older server deployment. Run portable export and raw backup. Timeout: 20 minutes each. Reversal: none needed, read-only.
2. Target: Server2. Run backup and dry-run imports. Expected: no unresolved account conflict. Timeout: 30 minutes. Reversal: discard the proposed import.
3. Target: Server2. Deploy the exact pushed main SHA and record container image ID. Expected: both health routes pass within 90 seconds. Reversal: deploy prior SHA.
4. Target: devices. Enroll collectors while old full daemons still run usage logging. Do not assign provider quota owners yet. Expected: heartbeat current and queue drains within 5 minutes. Reversal: collector uninstall, spool preserved.
5. Target: each provider account. Stop the old quota poller, clear old ownership, assign the new owner, then confirm one fresh snapshot. Timeout: two poll intervals. Reversal: unassign new owner and restart old poller.
6. Target: Server1 gateway. Forward `onwatch.mshi.ca` to Server2 dashboard. Expected: unauthenticated requests redirect to Authelia, authenticated login works. Reversal: stop the gateway and restart the retained old app.
7. Target: old full daemons. Stop only after central history, settings, imports, collector health, every graph mode, backup, and alerts pass. Retain old databases and launch configuration for 30 days. Reversal: restart the old daemon and pause the corresponding central poll owner.

Rollback triggers are loss of authenticated access, database integrity failure, unexplained duplicate quota polling, ingest unavailability over two poll intervals, graph corruption, or inability to create a verified backup. Decide within 30 minutes of route switch. Queued usage events remain on devices and drain once central returns.

## Tier 3 publication checklist

- Record clean Tier 2 results for `scripts/test-central-hosting.sh`, `scripts/test-central-hosting-security.sh`, Compose, and Ansible.
- Integrate only the reviewed graph, launchd, reset-intelligence, and central-hosting scope.
- Use the configured human Git identity. No force push or history rewrite.
- Record onWatch main SHA, `origin` URL, CI run URL/result, Ansible main SHA, image tag, image ID/digest, and build time.
- Deploy the exact pushed onWatch SHA. Re-run Ansible and require idempotence.
- Verify Authelia, built-in login, secure cookies, Tailscale HTTPS ingest, invalid token rejection, valid token acceptance, restart persistence, backup, restore selection, device health, and every graph range/mode.
- Stop and roll back on any security, persistence, required gate, or data-loss failure.

## Monitoring and troubleshooting

- Dashboard readiness: `GET /healthz` on container port 9211.
- Ingest readiness: `GET /healthz` through local 9212 and Tailscale 9443.
- Ingest metrics require `ONWATCH_INGEST_METRICS_TOKEN`; dashboard metrics use a different token.
- `current` means heartbeat age at most 3 minutes, `delayed` at most 15 minutes, and `stale` over 15 minutes.
- A current heartbeat and empty queue do not prove quota polling works. Compare
  each account's latest snapshot time with the provider's current reading. The
  collector's saved `quota_polls` reports failures and the next retry time.
- For Codex HTTP 401 responses, check the assigned credential home against the
  account's active login. A copied login can be revoked before its token's stated
  expiry. Point collection at the credential owner's current home; do not rotate
  a refresh token shared with another application.
- On Windows, verify CLI availability from the Scheduled Task context. A packaged
  application's private AppData files can appear at a normal path inside that
  application while being absent for the collector. Install the verified CLI in
  a shared user location such as `%USERPROFILE%\.local\bin`, or configure
  `ANTIGRAVITY_CLI_PATH` to a path the task can actually read. Also configure the
  explicit Antigravity account binding described in [its setup guide](ANTIGRAVITY_SETUP.md).
- HTTP 401 or 403 pauses uploads for 15 minutes. Rotate or correct the token file. Do not delete the spool.
- Gemini can return `UNSUPPORTED_CLIENT` from `loadCodeAssist` without a project.
  Preserve that provider reason and skip the subsequent quota request, which can
  otherwise report a misleading subscription error. A refreshed login does not
  fix an unsupported client; use the provider's supported collection path.
- A full spool stops new collection and preserves every unacknowledged event. Restore ingest before resuming.
- Import/export does not repair live sync. Use the collector for ongoing data.

## Existing Authelia gateway

Where public DNS already points to Server1, use `deploy/server1-gateway` to replace only the obsolete onWatch app with an nginx forwarding container. It preserves existing Traefik TLS and Authelia labels. Other services and the Traefik process need no restart. The gateway verifies Server2's Tailscale certificate and strips trusted-user headers; onWatch retains its own login.

Before starting the gateway, enable the unused Server2 route with `tailscale serve --bg --https=9444 http://127.0.0.1:9213`. Preserve existing 9443 ingest. Check HTTPS health through the tailnet. Stop the old Server1 onWatch container, then start the gateway. Roll back by stopping the gateway and starting the retained old container. Do not delete its database. No dashboard port is published on an external host interface.
