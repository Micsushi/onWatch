# First-Load Chart Render Deduplication Design

## Problem

Opening Codex and selecting a saved cost range can repaint the same graph
several times while startup data sources settle. Live instrumentation measured
eight platform-cost chart updates in about 2.2 seconds. Seven updates contained
the same 56 points, final values, and axis maximum.

The duplicate updates come from independent startup paths that correctly update
current provider data, API Integration current data, cached history, and
refreshed history. Each path renders the complete cost panel, and the panel
currently asks Chart.js to repaint even when the graph did not change.

Chart animation is already disabled. The remaining flicker is repeated canvas
painting, not interpolation.

## Desired Behavior

- Show the saved graph immediately when it exists.
- Continue updating cards, summaries, freshness, and refresh status as their
  data arrives.
- Repaint a graph only when its visible chart state changes.
- A changed range, graph mode, theme, dataset, scale, or empty state must still
  repaint immediately.
- A genuinely newer history response must replace the saved graph once.
- Apply the same protection to the main usage graph and the token-cost graph.
- Custom ranges retain their existing loading behavior.

## Design

Each history chart keeps the signature of the last visible render.

The token-cost signature includes:

- provider;
- selected range;
- graph mode;
- empty or populated state;
- cost and token point values;
- calculated axis maxima;
- theme-dependent chart colors.

The main usage signature includes:

- provider;
- selected range;
- graph mode;
- empty or populated state;
- visible dataset labels, values, and display properties;
- calculated axis maximum;
- theme-dependent chart colors.

Signatures are derived from the already-built chart data immediately before the
Chart.js update. The datasets are bounded by the dashboard's existing
downsampling, so serializing this state is small compared with Chart.js layout
and canvas painting.

If the new signature matches the last applied signature:

- skip assigning chart data and options;
- skip `chart.update`;
- leave the existing canvas untouched.

The surrounding panel continues rendering normally, so changing current totals
or freshness never waits for graph history.

If the signature differs:

1. assign the new chart data and options;
2. update with Chart.js `none` mode;
3. store the new signature only after the update is requested.

Destroying or recreating a chart clears its stored signature. Theme changes are
included in the signature, so recoloring is never suppressed. Empty-state
transitions use explicit signatures so populated-to-empty and empty-to-populated
changes each render once.

## Failure Handling

Signature generation must not be allowed to hide a chart update. If a value
cannot be normalized, the renderer treats the signature as changed and performs
the existing update.

Skipped duplicate paints do not change queue state, cache writes, fallback
timers, refresh status, or database queries.

## Verification

Automated regression tests will require:

- two identical token-cost render inputs produce one Chart.js update;
- a changed token-cost point, range, mode, theme, scale, or empty state produces
  an update;
- two identical usage render inputs produce one Chart.js update;
- changed usage data produces an update.

Live browser verification will reproduce a stale saved 30d first-open flow and
record Chart.js update calls. The accepted result is one initial saved graph
paint and at most one replacement paint when refreshed history is materially
different, with no identical consecutive signatures.
