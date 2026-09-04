# Reconnection Refresh — Mechanism

Read this before touching WS connect/reconnect handling in `App.svelte`, `TaskChat`, or the offline
write queue. The hard rules live in `AGENTS.md → Reconnection & Offline`.

When the WebSocket drops and reconnects, the current view (board, task view) updates automatically:

- `onConnected(true)` in `App.svelte` runs `silentRefresh()` if `initialLoadComplete` is true.
- Project re-subscription forces the server to re-send `task_diff_stats`, refreshing the diff panel.
- `TaskChat` subscribes to the `wsConnection` store and reloads history on reconnect. Turns completed
  while offline aren't replayed (buffer cleared at turn end), so DB reload is required.
- Browser `online`/`offline` events complement the WS backoff: `offline` queues writes immediately;
  `online` flushes the queue and calls `reconnectNow()` to skip backoff.
- Board refresh on `visibilitychange` handles phone-sleep cases.

**Tests:** e2e `reconnect-refresh.spec.ts`. Since `page.route` doesn't intercept WebSockets in
Playwright, the spec wraps the `WebSocket` constructor via `addInitScript` and uses `serverApi()` to
advance state.

## Behaviour in full (moved from AGENTS.md)
### Reconnection & Offline
`onConnected(true)` in `App.svelte` runs `silentRefresh()` once `initialLoadComplete`; `TaskChat` reloads history from the DB on reconnect because **turns completed while offline are never replayed** (buffer cleared at turn end). `offline` queues writes immediately; `online` flushes the queue and calls `reconnectNow()` to skip backoff; `visibilitychange` refreshes the board (phone-sleep). Project re-subscription forces a re-send of `task_diff_stats`. Mechanism + test approach: `docs/reconnection.md`.
