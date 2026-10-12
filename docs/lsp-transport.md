# LSP Transport — Mechanism

Read this before modifying `backend/lsp/handler.go`, `backend/lsp/extension_manager.go`, `frontend/src/lib/utils/lsp-connection-manager.ts`, or the browser's wait logic in `frontend/src/lib/utils/lsp-hover-actions.ts`. Hard rules are in `AGENTS.md → Extensions` and `AGENTS.md → Frontend`.

## Overview

This module manages the communication path for editor messages: from the browser's `/api/lsp` JSON-RPC socket through ID remapping and workspace queues, to the language server and back. It also defines how long the browser waits for an answer.

It does **not** handle:
- Server discovery or installation (`docs/extension-lifecycle.md`).
- Language routing (`docs/languages.md`).
- The underlying bridge protocol (`docs/sandbox-bridge.md`).

## Core Invariants

1. **Non-blocking Reads**: The socket's read loop only enqueues messages; it never waits on a language server. Saturated queues answer requests immediately.
2. **Exactly-once Answers**: Every forwarded request must be answered exactly once. Failures use `answerError`, and successful answers are removed from the mapping.
3. **Notification Priority**: Notifications are written inline, while requests are handled in goroutines. This ensures `didChange` notifications are never overtaken by slower hover responses.
4. **No Message Loss**: No component between the language server and the router discards messages.
5. **Queued Writes**: Browser socket writes are always queued. If a queue is saturated, the socket is closed with `StatusTryAgainLater` rather than blocking.
6. **ID Isolation**: Browser request IDs are remapped before being sent to the server to prevent ID collisions.
7. **Channel Routing**: Every response must include a channel hint (`_ch`). `dispatchMessage` uses this hint to route the response to the correct listener.
8. **Count-based Bounds**: Bounds in `backend/lsp` are defined as counts, not durations. Contexts handle the timing.
9. **Hover Lifecycle**: Hover placeholders are replaced by the server's name, and eventually by the answer or a timeout. `remember` drops failed requests so the next hover triggers a fresh request.

## Implementation

**Server-side**:
- **Goroutines**: One goroutine per browser (`ClientSession.serve`) and one ordered writer per workspace (`serveWorkspaceWrites`) meet in `writeJob`.
- **Request Tracking**: `nextBackendID` numbers requests; `track` records the return path. If `DefaultMaxPendingRequests` is exceeded, the oldest request is evicted and an error is sent.
- **Initialization**: `workspaceState.decideInitialize` caches the `initialize` result per workspace and signals `$/lspReady` to the browser.
- **Routing**: `routeServerMessage` distinguishes between answers (routed by ID to one session) and notifications (fanned out to all sessions using that workspace).

**Browser-side**:
- **Multiplexing**: `lsp-connection-manager.ts` multiplexes all editor tabs over a single socket and manages channel bookkeeping.
- **Wait Logic**: `lsp-hover-actions.ts` manages the user-facing wait clocks (`armWaitClocks` and `waits`). Caches are per-view and keyed by document offsets, meaning any edit clears the cache.

## Verification

- `go test ./backend/lsp/`: Verifies ordering, exactly-once answering, cancel IDs, and bounds.
- `frontend/src/lib/utils/__tests__/lsp-hover-actions.unit.ts`: Verifies hover clocks and memoization.
- `frontend/tests/e2e/editor-hover-definition.spec.ts`: Verifies the end-to-end path with a real browser.
- `make check-race`: Verifies writer goroutines.
