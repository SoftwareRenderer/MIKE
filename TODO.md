# TODO

## Goal
When a chat message send fails due to a network connection issue (fetch TypeError/AbortError), queue it in the offline action queue instead of losing it, keep it visible as an outgoing (queued) message, surface a visible non-persisted error, and let the existing ReviewBar "Resume" button appear.

## Tasks

### 1. Expose the transient-error helper
- [x] `frontend/src/lib/api/queue.ts`: export `isTransientError` (currently module-private)

### 2. Queue `sendTaskMessage` on transient network failure
- [x] `frontend/src/lib/api/tasks.ts`: in `sendTaskMessage`, wrap the raw HTTP call in try/catch; on `isTransientError` (network failure), enqueue a `sendTaskMessage` action via `queueWrite` and return the optimistic queued result (instead of throwing). Non-transient errors (server 4xx/5xx) keep throwing (existing behavior).

### 3. TaskChat: keep the outgoing message + show non-persisted error + Resume button
- [x] `frontend/src/lib/components/Chat/TaskChat.svelte`:
  - [x] `sendWithTracking` returns the send result object (`{ id, role, content, queued?, actionId?, thread_id? }`) or `null` on failure (was boolean)
  - [x] `handleSend`: on `null` keep existing permanent-failure behavior (remove message + `send_failed` error); on a queued result keep the optimistic user entry, mark it `queued: true`, and append a visible, non-persisted error entry (`code: 'send_queued'`)
  - [x] New `handleQueuedSend(content, entryId?)` helper: marks matching entry queued + appends the non-persisted error
  - [x] `handleRegenerate`: update the `!result` check to `result === null`; apply `handleQueuedSend` on queued results
  - [x] `handleEditFork` / `handleEditSend`: apply `handleQueuedSend` on queued results
  - [x] `showResumeButton` / `showRestartButton` / `handleStartTask`: use the last **non-error** entry so the appended error doesn't hide the Resume button
  - [x] `handleDismissError`: non-persisted error codes (`send_queued`, `ws_disconnected`) are removed locally only — no `deleteTaskMessage` API call (prevents 404 / queued delete of a non-existent message)
  - [x] `onQueueChanged` cleanup (import `getQueuedTaskIds`): when no actions remain queued for this task, drop `send_queued` error entries (message flushed successfully)

### 4. Queued indicator copy
- [x] `frontend/src/lib/components/Chat/ChatMessage.svelte`: update queued-indicator tooltip to "Queued — will send when the connection is restored" (was "after summarization completes")

### 5. Unit tests
- [x] New `frontend/src/lib/api/__tests__/send-queue.unit.ts` (mock IndexedDB + fetch, same pattern as `offline-cache.unit.ts`):
  - [x] `sendTaskMessage` queues a `sendTaskMessage` action (in IndexedDB queue) and returns a queued result on transient failure (TypeError)
  - [x] `sendTaskMessage` does NOT queue and rejects on non-transient failure (HTTP 500)
  - [x] Also covers AbortError (timeout) queueing

### 6. E2E test
- [x] New `frontend/tests/e2e/offline-send-queue.spec.ts`:
  - [x] Setup: create task via API, move to `review` (review bar + chat form visible)
  - [x] Block the `/send` endpoint with `page.route(...).abort('failed')`
  - [x] Send a message via the chat UI
  - [x] Assert: message visible as outgoing user message with `.queued-indicator`; `.error-card` visible with queued copy; ReviewBar "Resume" button visible
  - [x] Assert IndexedDB `offline-queue` contains the `sendTaskMessage` action
  - [x] Restore network, reload (flushes queue), assert message is persisted server-side, queue drained, error entry gone
  - [x] Red-phase check: test fails when the queue-on-failure logic is removed

### 7. Verification
- [x] `cd frontend && npm run check` (svelte-check clean — 0 errors, 0 warnings)
- [x] `cd frontend && npm test` (all 831 unit tests pass, incl. new file)
- [x] `cd frontend && npm run build`
- [x] Run new e2e spec: `npx playwright test offline-send-queue` (passes)
- [x] Regression e2e: `offline-cache`, `chat-core`, `ui-modals` pass (ui-modals "edit task prompt and save" is a pre-existing flaky race — optimistic UI update vs fire-and-forget `updateTask` PUT; fails intermittently on baseline too, unrelated to this change)

### 8. Post-review cleanup (redundancy / dead code)
- [x] `TaskChat.svelte`: `sendWithTracking` catch no longer uses `e` → `catch {`
- [x] `tasks.ts`: extracted shared `queueMessage()` helper — the offline branch and the transient-error catch branch were byte-identical
- [x] `TaskChat.svelte`: extracted `appendErrorEntry(code, message)` — dedupes the `send_failed`/`send_queued` error-entry construction
- [x] `queue.ts`: removed redundant inner `isTransientError(e)` re-check in `flushQueue`'s callback (`doFlush` only invokes it for transient errors already)
- [x] `TaskChat.svelte` `onQueueChanged`: gated BOTH the queued-indicator clearing and the `send_queued` notice removal on `!queuedIds.has(taskId)` — previously the loop cleared all queued flags on any queue change while the notice removal was gated, so indicator and notice could disagree (multi-message / multi-task flush scenarios)
- [x] Re-verified after cleanup: svelte-check clean, 831 unit tests pass, build succeeds, `offline-send-queue` e2e passes

## Discovered Tasks
- [x] `frontend/tests/e2e/ui-navigation.spec.ts` has a pre-existing syntax error in the committed code (`ensureTaskStarted   findProviderByRef` — missing comma in the import on line 2). It cannot be loaded by Playwright (`SyntaxError: Unexpected token`), which blocks the offline-queue e2e tests in that file (`queued actions survive flush failure and retry on reconnect`). The fix is a one-character edit (add the missing comma). NOT part of the original plan — found during regression testing. Fixed; the two offline-queue tests now pass. (The file's `board renders from cache when server is unreachable` test still fails on `page.goto` with `net::ERR_INTERNET_DISCONNECTED` — a service-worker offline-navigation issue unrelated to this change.)

## Notes
- Scope is frontend-only; no backend/Go changes.
- "Network connection issue" = `TypeError` (fetch failed) / `AbortError` (15s apiFetch timeout). Server errors (4xx/5xx) keep current behavior (message removed + `send_failed` error) — per requirement wording.
- Error entries are client-side only (never persisted to DB): `send_queued` (new) and `ws_disconnected` (existing). Persisted errors (from stream-handler) still delete via API on dismiss.
- "Resume" = the existing ReviewBar button (review status + last non-error entry is a user message). In-progress tasks keep their "Restart Task" affordance.
- Reuses the existing offline-queue machinery (`offline-queue` IndexedDB store, `sendTaskMessage` action type, flush-on-reconnect/page-load).
- Note: the environment's checkpoint mechanism reverted the four modified source files once mid-task; they were re-applied and re-verified.
