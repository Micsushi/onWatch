# UI Design

onWatch is a local-first quota and cost monitor with a dense operational
dashboard.

## Required Sources

- Design system: `design-system/onwatch/MASTER.md`
- Dashboard and cost decisions: `docs/`
- Implemented web UI: `internal/web/`

## Rules

- Preserve a compact, high-signal dashboard layout.
- Favor scan speed, exact labels, and consistent ranges for cost and quota data.
- Keep provider identity, refresh state, errors, and data age visible.
- Keep the last successful cards and graphs visible during refreshes. Show a
  persistent header activity indicator until replacement data finishes loading.
- Swap history graph datasets and axis scales atomically without transition
  animation. Cached and refreshed payloads may arrive close together, so range
  changes must not interpolate from the old graph or restart an animation.
- Preserve observed usage points and timestamps through client rendering.
  Summary values use observed response data, and collection gaps must not be
  filled with synthetic usage points, connecting lines, or shaded areas. Leave
  unobserved time blank in cumulative usage and cost graphs.
- When collection gaps split a dense usage series, remove area fill and point
  markers, and explain that blank spans mean no samples were collected.
- Per-period usage keeps unobserved buckets unknown, never assigns usage across
  a collection gap to a later bucket, and clamps partial edge buckets to the
  exact selected window without clipping. All-zero token and cost periods use
  the normal empty state.
- Do not repaint a visible history chart when startup or refresh paths produce
  an identical render state. Cards and freshness may update independently.
- Keep the sticky header height stable while refresh status appears. The active
  refresh message replaces the timestamp and truncates before it can wrap.
- Preset graph refreshes share one bounded, non-preemptive queue. A newly
  selected preset moves ahead of pending work but never interrupts the active
  query. Keep exact saved preset snapshots indefinitely as display fallbacks,
  and never dim or clear an existing graph while a preset refresh waits.
- Custom date graphs use a separate abortable request lane because there is no
  reliable preset fallback. Replacing a Custom request may cancel the older
  Custom request without interrupting the preset queue.
- Keep history controls inside the graph or table header they affect. Usage,
  token-cost, and cost-breakdown views each own an independent range. Presets
  update that view's Start and End dates; editing either date selects Custom
  only for that view.
- Disclose reduced historical precision when a response uses compacted hourly
  data. Treat the 30d preset as the exact-retention boundary and do not warn
  for it; warn for All or Custom selections that extend beyond 30 days.
- Do not rely on color alone for provider or warning state.
- Avoid vague or generic UI copy.
- Update this file when a durable product-wide UI rule changes.

If stable machine-readable tokens are introduced, store them in root
`DESIGN.md` and link them here.
