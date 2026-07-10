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
- Do not rely on color alone for provider or warning state.
- Avoid vague or generic UI copy.
- Update this file when a durable product-wide UI rule changes.

If stable machine-readable tokens are introduced, store them in root
`DESIGN.md` and link them here.
