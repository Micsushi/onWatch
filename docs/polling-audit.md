# Polling and collector audit

The September 2026 audit covered provider clients and polling agents, account
selection, local usage collection, poll-health notifications, collector queue
and ingestion, dashboard authentication and routing, backup and deployment
contracts, and the separate Quota Wake worker and its offline tests.

## Findings and repairs

| Finding | Impact | Repair |
| --- | --- | --- |
| Named Codex profiles used ID-token expiry | Unnecessary OAuth refresh and paused accounts despite a usable access token | Prefer access-token expiry |
| Copied Codex grants could be refreshed by multiple owners | Stale profiles and competing refresh attempts | Read-only default and explicit credential-home binding |
| Codex account header was misspelled and omitted by the manager | Requests lacked the expected account context | Send the correct account header |
| Gemini overwrote a refreshed token in the same poll | Avoidable authentication failure and second refresh | Retain the new token |
| Gemini and Antigravity omitted poll-health outcomes | Failures and recoveries were not consistently visible | Register pollers and report outcomes |
| Gemini auth pause had no timed recovery | Temporary failures could remain paused until credentials changed | Bounded authentication retry schedule |
| New session discovery cached paths for five minutes | Delayed Claude, Codex, and Gemini usage registration | Refresh discovery every 30 seconds |
| Usage parsing advanced state before output persisted | Failed output writes could lose usage until restart | Restore source state on failure and sync output |
| Collector retries were not durable | Restart could repeat failing requests immediately | Persist quota retry state with jitter |
| Collector ignored quarantine failures and unknown statuses | Queued observations could be discarded | Validate acknowledgements and persist rejections before ack |
| Partial queue writes poisoned subsequent records | Later observations became unreadable | Preserve incomplete suffixes and recover them on startup |
| Collector Codex ignored account credential selection | Multiple assignments could report the same local account | Resolve credential alias and validate account identity |
| Collector Gemini skipped project discovery | Quota requests could lack project context | Resolve tier/project before quotas |
| Antigravity web test depended on a real language server | Local and CI checks failed based on host state | Stub discovery in the handler test |
| Quota Wake fallback ignored response contents | A successful HTTP exchange counted as a successful wake without evidence | Require exact text, token usage, and no tool output |
| Central Codex mirroring treated external IDs as local row IDs | Distinct accounts collapsed into the default history | Resolve the persistent provider identity inside the ingest transaction |
| Ingestion bypassed quota trackers and alerts | A credential-free server stored quotas without evaluating live alerts | Process fresh, latest observations from a durable database cursor outside the HTTP handler |
| Collector registered after its first source scan | Large first scans delayed device visibility | Send the initial heartbeat before scanning |
| Imported-history export chose a broad provenance scan | Windows export spent minutes repeatedly scanning the same table | Cover the local-record lookup and provenance ordering with one index |

The central monitor suppresses historical and out-of-order observations, retains
its cursor across restarts, and retries processing failures. Claude, Codex,
Gemini, and Antigravity reuse their existing cycle trackers. Notification
delivery retains the existing channel cooldown and retry behavior; this is not
an exactly-once external delivery guarantee. Ingest-enabled dashboards create
trackers even when provider credentials are held only by collectors.

The Codex account header is verified against the
[official backend client](https://github.com/openai/codex/blob/main/codex-rs/backend-client/src/client.rs).

## Operational limits

Provider-side delays, rate limits, unavailable language servers, and expired
login grants remain external conditions. A quota poll cannot make the provider
publish usage sooner. Quota Wake's minimal probes start usage windows; they do
not extend an expired login grant. New-file discovery and quota polling are
separate intervals.

Baseline onWatch smoke checks reproduced the Antigravity handler-test failure.
Quota Wake unit, worker failure, worker scheduling, and fallback tests passed
before repair. Added regressions reproduced expiry, token overwrite, unsafe
acknowledgements, and partial-write failures before the fixes.

The Server2 cutover requires fresh observations from the intended devices,
verified private ingestion, dashboard authentication, and a current backup.
Old full daemons must remain available until those checks pass.
