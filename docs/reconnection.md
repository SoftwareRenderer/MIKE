# Reconnection & Offline Mechanism

This document describes how the application recovers from WebSocket disconnects and handles offline state. Invariants are in `AGENTS.md → Reconnection & Offline`.

## Reconnection Refresh

When the WebSocket reconnects, the application automatically refreshes the current view:
- **Board**: Runs `silentRefresh()` to update the project list, the board on screen and the config.
- **Task View**: Reloads chat history from the database and refreshes the diff panel.
- **Terminal**: Opens a new connection if the previous one stopped answering.

A read inside that refresh may still fall back to the mirror, and one such read is enough to keep the
"Viewing cached data" banner and the age line where they were: the socket coming back is not an answer about
the data. `isFromCache` is what says which kind of answer landed.

### The Replay Buffer
Since a reconnection is a new connection, the server doesn't know what the client missed. To avoid full reloads, the server maintains a **per-task replay buffer** (`taskBufferFrames`).
- **Purpose**: Replays the most recent frames of a task's turn to the client upon subscription.
- **Limit**: Replays are capped at half the connection queue's budget to avoid blocking live frames.
- **Consistency**: A task's frames are delivered exactly once through a locking mechanism (`asm.mu` and `taskBufMu`).

## View Reconciliation (The Tab that Comes Back)

The app reconciles state when a tab regains focus, wakes from sleep, or is restored from cache.

### Reconciliation Triggers
- `visibilitychange` $\to$ visible.
- Window `focus`.
- `pageshow` (bfcache).
- Clock jump (detects page suspension/sleep).

### Reconciliation Logic
Each view determines if it is "behind" by asking the server for a sync digest (`GET /api/.../sync`):
- **Board**: Always refreshes.
- **Chat**: Compares the current `revision` with the server. If the server has moved, it refetches the newest message window.
- **Diff**: Refreshes the manifest and re-evaluates if the base commit has moved.

## Execution State Sync

To ensure the UI correctly reflects whether a task is running, paused, or stopped, the app uses a hierarchy of truth:
1. **The Task Row**: The most trusted source of `execution_state`.
2. **The Board's Copy**: Used as a fallback.

`TaskChat` reads the task row at mount to decide if it should show a "running" indicator or a "paused" bar, regardless of what the board thinks.

## Offline Mode & Mirroring

### The Offline Mirror
The app maintains a local mirror of the board (`board:<projectId>`) in browser storage.
- **Usage**: When the socket is down, the app displays the mirrored board.
- **Freshness**: The "Data from {age}" banner indicates the age of the mirrored data.
- **Recovery**: Clicking "Connect" in the cache banner skips the WebSocket backoff and forces a `silentRefresh()`.
- **The offline screen answers from the mirror, under the loading screen.** Booting past the offer
  ("Offline Mode") reads projects, the board and the config out of IndexedDB, with **no request in front of
  them**. A request that neither answers nor fails would otherwise sit between the click and the board.
  `getCachedProjects()` and `getCachedConfig()` are those reads, and `loadAppData('mirror')` is the path that
  uses them. The mirror stream's own reconnect is what brings live data back. `sessionBoot.isLoadingAuth`
  goes back up for the read: the offline screen is on screen only while that flag is down, and the app shell
  under it holds neither project nor config.

### The Offline Queue
Writes performed while offline are stored in the `offline-queue`.
- **Projection**: Pending messages are derived from the queue and rendered as "pending" rows in the chat.
- **Flushing**: The queue is flushed immediately upon reconnection.
- **Persistence**: Queue entries are stored locally; those containing secrets (e.g., API keys) are refused and never persisted.

### Signing out empties both halves
Signing out leaves this browser holding nothing, because the stores hold this user's messages, diffs, drafts and queued writes. `signOut()` (`frontend/src/lib/services/session-boot.svelte.ts`) runs `clearQueue()` **then** `clearAll()`, in that order: a queue left in memory is written back into the emptied database by the next enqueue.

## Slow Clients

The server does not time out slow clients. Instead:
- **Bounded Queues**: Each connection has a bounded `sendQueue`. If the client stops reading, the oldest frames are dropped.
- **Release**: Once the queue is full, the server closes the connection with `StatusTryAgainLater`.
- **Recovery**: The client detects the close and initiates the standard reconnection flow, which triggers a resync.

## Verification

`make test-frontend` runs the unit files below; `make test-e2e SPECS=reconnect-refresh` runs the specs.

- **E2E Tests**: `reconnect-refresh.spec.ts`, `away-and-back.spec.ts`, `sync-return-freshness.spec.ts`, `focus-return-review.spec.ts`, `offline-mode-cached-board.spec.ts`.
- **Unit Tests**: `connection-restored.unit.svelte.ts`, `task-events-delivery.unit.svelte.ts`, `stream-subscription.unit.ts`, `chat-history-merge.unit.ts`, `session-boot.unit.svelte.ts`.
- **Backend Tests**: `app_stream_test.go`, `app_stream_subscribe_race_test.go`, `handler_task_sync_test.go`.
