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

## The Editor Buffer Across a Browser Being Away

An unsaved buffer belongs to the browser, not the server: the editor writes it to the `editor-drafts`
store as the reader types (throttled, and flushed on `beforeunload`, because a reload never destroys the
component), and reopens a file from it. Two rules keep that buffer honest about the file.

### A draft carries the bytes it diverged from

An `editor-drafts` record is `{ text, base, ts }`, and
`base` is what the file held when `text` was written — the draft's merge partner, kept because a merge
with no base can only pick a side. Reopening a file whose bytes moved is then a three-way merge
(`adoptDraft` → `reconcileExternalChange`, `$lib/utils/editor-merge`). It is the same question an
`fs_event` answers while the buffer is open, so a change is not merged one way while reading and another
way while returning. What each answer does:

- File unchanged since the draft: paint the draft.
- Unsaved edits and a change elsewhere: merge, **silently** — a reader who was asked nothing has nothing to read.
- Both sides changed one region: paint both, between `<<<<<<<unsaved`, `=======` and `>>>>>>>on-disk`.
  That marker text is a token rather than a catalog key because it lands inside the person's file, where
  the browser's language has no business. `editor-conflict.ts` tints the lines and the banner counts the
  blocks; resolving means deleting lines, so the buffer's text is the only record and both read it fresh.
- A draft older than the base (the bare string an older build wrote): the buffer stands alone.

Saving a buffer that still holds markers is allowed and never assumed: `handleSave` asks first, because
writing them is a thing a person can want — the file may hold the version they would rather not lose —
and never what "Save" means by default.

### Closing an editor is not discarding it

Closing a file's tab keeps its buffer: the draft is what the next open reads, so closing asks nothing and
loses nothing. Reopening brings the text back still marked unsaved. Deleting the text is how a person throws
an edit away — a buffer equal to its file leaves no record behind.

### A save the connection lost waits with the editor, not in the queue

A body replayed from IndexedDB
would overwrite whatever the file got since, so `offline-queue` never carries file content. The editor
holding the buffer holds the wait (`saveRetryDelayMs`) until the write lands.

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

- **E2E Tests**: `reconnect-refresh.spec.ts`, `away-and-back.spec.ts`, `sync-return-freshness.spec.ts`, `focus-return-review.spec.ts`, `offline-mode-cached-board.spec.ts`, `editor-draft-merge.spec.ts`, `editor-external-change.spec.ts`.
- **Unit Tests**: `connection-restored.unit.svelte.ts`, `task-events-delivery.unit.svelte.ts`, `stream-subscription.unit.ts`, `chat-history-merge.unit.ts`, `session-boot.unit.svelte.ts`, and for the editor buffer `editor-merge.unit.ts`, `editor-conflict.unit.ts`, `editor-draft-merge-on-open.unit.svelte.ts`, `editor-buffer-persistence.unit.svelte.ts`, `storage-editor-draft.unit.ts`.
- **Backend Tests**: `app_stream_test.go`, `app_stream_subscribe_race_test.go`, `handler_task_sync_test.go`.
