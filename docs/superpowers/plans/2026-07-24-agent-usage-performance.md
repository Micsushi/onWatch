# Agent Usage Performance Implementation Plan

> REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development.

Goal: Remove periodic multi-core agent-usage collection spikes while preserving local usage history.

Architecture: Honor the configured schedule, retain state for the append-only Codex parser, and cache recursive source discovery briefly. Collector-owned state resets on truncation or replacement and existing persisted fingerprints remain the final dedupe boundary.

Tech Stack: Go 1.25, standard library `bufio`, `io`, `os`, existing collector and parser tests.

## Stage OW-S1 - Lightweight background collection

### Feature OW-S1-F1 - Incremental local agent-usage collection

#### OW-S1-F1-T1 - Honor configured collector interval

- [ ] Completion: acceptance and verification passed
- Parent: Project onWatch / Stage OW-S1 / Feature OW-S1-F1
- Outcome: A 120-second configured interval is not reduced to 15 seconds.
- Files:
  - Modify `internal/agent/agent_usage_collector_agent.go`
  - Modify `internal/agent/agent_usage_collector_agent_test.go`
- Scope:
  - Add a same-package test constructing an agent with `120*time.Second`.
  - Assert `ag.interval == 120*time.Second`.
  - Keep the non-positive fallback at 15 seconds.
  - Remove the maximum-interval clamp and unused constant.
- Out of scope: Provider-agent schedules.
- Acceptance:
  - Test fails against current 15-second clamp.
  - Positive configured intervals are preserved.
  - Zero interval still uses 15 seconds.
- Verification: `bash ./app.sh --test`
- Blocked by: none
- Blocks: OW-S1-F1-T4
- Context: `NewAgentUsageCollectorAgent`

#### OW-S1-F1-T2 - Tail appended Codex JSONL

- [ ] Completion: acceptance and verification passed
- Parent: Project onWatch / Stage OW-S1 / Feature OW-S1-F1
- Outcome: Changed Codex sessions process only complete bytes appended since the prior scan.
- Files:
  - Modify `internal/agentusage/parsers.go`
  - Modify `internal/agentusage/collector.go`
  - Modify `internal/agentusage/parsers_test.go`
  - Modify `internal/agentusage/collector_test.go`
- Scope:
  - Introduce unexported Codex parse state carrying offset, turn context, fast-mode context, and partial line.
  - Refactor full-file parsing through the stateful parser so public behavior stays compatible.
  - Store state per Codex path in `Collector`.
  - Commit parser state only after a successful scan.
  - Reset when size shrinks or the file is replaced.
- Out of scope: Incremental Claude, Gemini, Cursor, or Antigravity parsing.
- Acceptance:
  - First scan emits existing events.
  - Second scan after append emits only the new event.
  - An appended token event without a new `turn_context` retains prior model/mode/effort.
  - Incomplete trailing JSON is held, not emitted or treated as a permanent error.
  - Completing that line on a later append emits it once.
  - Truncation resets state and parses replacement content.
- Verification: `bash ./app.sh --test`
- Blocked by: none
- Blocks: OW-S1-F1-T4
- Context: `ParseCodexUsageFile`, `Collector.collectSource`, collector `seen`

#### OW-S1-F1-T3 - Cache recursive source discovery

- [ ] Completion: acceptance and verification passed
- Parent: Project onWatch / Stage OW-S1 / Feature OW-S1-F1
- Outcome: Normal collection scans reuse known paths instead of recursively walking every source tree.
- Files:
  - Modify `internal/agentusage/collector.go`
  - Modify `internal/agentusage/collector_test.go`
- Scope:
  - Cache expanded paths per source with a five-minute refresh deadline.
  - Add a test clock or explicit refresh timestamp that tests can control without sleeping.
  - Continue statting cached files for size/modification changes.
- Out of scope: `fsnotify` or another dependency.
- Acceptance:
  - Repeated collections inside the cache window reuse discovered paths.
  - A new file becomes visible after forced cache expiry.
  - Deleted cached files do not fail the whole collection.
- Verification: `bash ./app.sh --test`
- Blocked by: none
- Blocks: OW-S1-F1-T4
- Context: `expandSourcePaths`, `collectSource`

#### OW-S1-F1-T4 - Verify and profile installed daemon

- [ ] Completion: acceptance and verification passed
- Parent: Project onWatch / Stage OW-S1 / Feature OW-S1-F1
- Outcome: Tests pass and the installed process demonstrates lower CPU.
- Scope:
  - Run repository-prescribed test/build commands.
  - Rebuild and install the Windows binary using existing project lifecycle tooling.
  - Restart the installed daemon.
  - Confirm executable path and PID.
  - Capture at least 35 seconds of per-second CPU usage covering two former 15-second windows.
- Out of scope: Commit, push, release, or unrelated dirty files.
- Acceptance:
  - Full project verification passes.
  - Installed binary comes from this checkout.
  - No repeated 7-8 core burst occurs in the sample.
- Verification:
  - `bash ./app.sh --test`
  - repository build/install command discovered from current project scripts
  - live per-process CPU timeline
- Blocked by: OW-S1-F1-T1, OW-S1-F1-T2, OW-S1-F1-T3
- Blocks: none
- Context: Preserve all pre-existing uncommitted work; do not commit.

Status on 2026-07-24:

- `./app.sh --smoke` passed.
- `./app.sh --test` could not start because Go race detection requires CGO and no Windows C compiler is installed.
- The installed repo build was restarted and verified at `C:\Users\sushi\.onwatch\bin\onwatch.exe`.
- A clean 35-second background sample averaged 0.000 CPU cores, peaked at 0.016, and had no samples over one core.
