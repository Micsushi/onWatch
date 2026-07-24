# Dashboard History Performance Design

## Problem

The dashboard warms every provider, history range, and insight range on load and repeats that work every 30 seconds. Cost panels also prefetch every range every 15 seconds. In the combined view, the main provider history waits for the slower API Integrations history before rendering.

With a large local database this creates dozens of competing SQLite requests and delays the graph the user is actually looking at.

## Design

- Fetch only the active provider and selected range.
- Keep the existing session cache so a previously viewed graph can render immediately while stale data refreshes.
- Remove whole-dashboard and all-range background warmups.
- Fetch one selected cost-history payload and share it between the chart and model breakdown.
- Render combined provider history as soon as it arrives, then load API Integrations history as optional secondary data and re-render when ready.

This request-diet change is intentionally first. Stored rollups remain a later option if direct endpoint timing is still too slow after request contention is removed.

## Verification

- Static regression tests prevent reintroducing all-range warmups.
- A regression test verifies combined history renders before secondary API Integrations history is requested.
- Smoke, race, and live endpoint/page-load checks verify behavior.
