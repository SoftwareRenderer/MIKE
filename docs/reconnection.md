# Reconnection Refresh — Mechanism

Read this before touching WS connect/reconnect handling in `App.svelte`, `TaskChat`, or the offline
write queue. The hard rules live in `AGENTS.md → Reconnection & Offline`.

When the WebSocket drops and reconnects, the current view (board, task view) updates automatically:

- `onConnected(true)` in `App.svelte` runs `silentRefresh()` if `initialLoadComplete` is true.
- Project re-subscription forces the server to re-send `task_diff_stats`, refreshing the diff panel.
- `TaskChat` subscribes to the `wsConnection` store and reloads history on reconnect. Turns completed
  while offline aren't replayed (buffer cleared at turn end), so DB reload is required.
- Browser `online`/`offline` events complement the WS backoff: `offline` queues writes immediately
  (the exceptions are a config carrying a real provider API key and a clone URL carrying credentials —
  both refused, never persisted);
  `online` flushes the queue and calls `reconnectNow()` to skip backoff.
- Board refresh on `visibilitychange` handles phone-sleep cases.

**Tests:** e2e `reconnect-refresh.spec.ts`; the merge above is held by
`src/lib/utils/__tests__/chat-history-merge.unit.ts`, which drives the real stream handler and the real
message builder. Since `page.route` doesn't intercept WebSockets in
Playwright, the spec wraps the `WebSocket` constructor via `addInitScript` and uses `serverApi()` to
advance state.

## The reload meets the replay buffer

`loadHistory` runs on reconnect, on task switch and after a stop, and it joins two incomplete views of
one conversation: the DB rows, which lag the running turn (`finalizeTurn` writes the assistant row when
an LLM call ends and each `tool_call` row just before its tool starts, so a multi-call turn reaches the
DB one tool at a time), and the frames the server's per-task buffer kept. `mergeLoadedHistory`
(`src/lib/utils/chat-entries.ts`) owns the rule and states it at its definition; the fact it is built on
is that a `tool_call` event is broadcast once, so an entry discarded before its row exists loses that
card until the page is reloaded — discarding on an approximation is what used to show a task with its
reasoning and none of its tool calls.

Being in progress is what separates a pending `tool_call` row that is work from one that is a leftover:
a stopped task will never write the missing result, so that card is hidden while the message around it —
its text, its thinking, the cards that did finish — stays.

## Behaviour in full (moved from AGENTS.md)
### Reconnection & Offline
`onConnected(true)` in `App.svelte` runs `silentRefresh()` once `initialLoadComplete`; `TaskChat` reloads history from the DB on reconnect because **turns completed while offline are never replayed** (buffer cleared at turn end). `offline` queues writes immediately, except a value that carries a secret — an unmasked provider key, or a clone URL with credentials in it — which is refused because the queue is persisted; `online` flushes the queue and calls `reconnectNow()` to skip backoff; `visibilitychange` refreshes the board (phone-sleep). Project re-subscription forces a re-send of `task_diff_stats`. Mechanism + test approach: `docs/reconnection.md`.

### How a request ends: cancelled, or unanswered

`apiFetch` (`src/lib/api/helpers.ts`) is the one place that decides what a failed fetch means, because all
69 call sites read it.

| outcome | thrown | offline flag | what the user sees |
|---|---|---|---|
| caller aborted (unmount, navigate away, Stop/Cancel) | `RequestAborted` | untouched | nothing — they asked for this |
| connection failed | `ConnectionFailure` | **set** | "MIKE did not answer — check the server is still running", and the queue takes over |

Before this, both rows were one row: any `AbortError` counted as offline. Since `apiFetch` attached its own
15 s `AbortController` to every call that had no caller signal, that meant a big task diff, a merge on
commit-and-done, or a clone replayed from the queue **failed in the browser at 15 s while the server was
still doing the work**, and then every later write went to IndexedDB because of it. Cancelling a commit
dialog had the same effect on purpose-free terms.

**No client-side deadline replaced it.** The plan's rule is that the queue gets bounded and the work does
not, and the case the timeout existed for — a server that accepts a connection and never answers — is
already surfaced honestly: the WebSocket drops, and `setOfflineStatus(!connected)` in `App.svelte` sets the
same flag the HTTP layer would have. Stalls on the server are reported where they are known
(`subproc.Watch` for podman, the lock slow-wait reports), not guessed at from the client.

Both classes keep the DOM `name` (`AbortError`, `TypeError`) rather than inventing one, because a check that
misses a cancellation fails silently in one of two directions: a cancellation starts toasting, or a queueable
write starts failing. **The name is the test; `instanceof DOMException` is not** — these are `Error`s that
carry the name, which is why `isAborted(e)` is a function rather than a class check. One call site
(`diff-cache`'s fetch of a single file's diff) tested the instance, so the moment the throw became an `Error`
a cancelled read stopped being silent: a check written against the throwing type, not the contract.

`isTransientError` explicitly excludes a cancellation, so a send the user stopped is not replayed from
storage after they left.

The server answers the same pair, in `httputil.Fail`, which is the only place a status is chosen:
`context.Canceled` → **499** logged at debug (no fault to report); an `errs.Operation` fault → **500** whose body
is MIKE's own sentence for the operation (`"git log failed"`) with the cause logged; anything else → **500**
`"internal error"` at error, cause logged. The middle class exists because 30 handlers had no way to say "this
did not complete and it is MIKE's problem" without echoing the cause into the browser: they printed git's
stderr and an absolute worktree path to the panel and left the server with no trace. `httputil.Fault` is that
call — `Fault(w, "git log", err)` — and it is `Fail` with an `errs.Operation`, so a handler names what it was
doing instead of choosing a code.

Neither answer carries a deadline for the next request, and nothing in the product bounds how long a client
may wait for this server — a subprocess or a protocol round-trip may carry its own timeout, a wait on MIKE
does not.

Tests: `src/lib/api/__tests__/fetch-failure.unit.ts` (including "no implicit abort after 60 s of fake timers"
and "a cancellation does not call itself an outage"), `src/lib/api/__tests__/send-queue.unit.ts`. The server side is held by `backend/errs/errs_test.go` (an operation must not inherit its cause's
classification) and `backend/httputil/httputil_test.go` (the sentence answers, the cause is logged, a host path
never reaches the body).

### A tool call reads as running only while its task is in flight

An unanswered `tool_call` is in-progress work in exactly the states a turn can still send a frame from, so
`taskIsInFlight` (`$lib/utils/task-utils.ts`) names `running` and `pulling_image` and nothing else. Its
server-side mirror is `terminalTurnStates` (`backend/stream/app_stream.go`), which drops a task's replay
buffer on the states that end a turn: a `tool_call` replayed to a later subscriber is a card that reads as
running and nothing will ever answer it.

**The task's status is never the answer to "is it still running".** Stopping an in-progress task keeps
`status = in_progress` and only moves `execution_state` to `paused` (`applyStopTransition`), so a check
written as `taskStatus !== 'in_progress'` cannot fire for the stopped task. `TaskChat` reacts instead to
`execution_state` **leaving** flight — not on mount, because a task opened while it runs has live cards, and
not on the `status` frame, because a duplicate message is answered with `state: "idle"` while the turn is
still running (`backend/exec/handler_http.go`).

What disappears depends on where the card came from. A live entry whose call was never persisted goes whole:
the turn was cancelled before `finalizeTurn` (`backend/exec/loop.go`), which writes both the assistant row
and the `tool_call` row, so a rebuild from the DB shows neither. A persisted row keeps its message and loses
only the card (`withoutStalePendingCards` in `$lib/utils/chat-entries.ts`) — the turn's finished text and
thinking are on screen whatever its tool did.

Known gap: an executor that outlives `executorExitGrace` can still stream a `tool_call` frame after the
transition, and that frame re-adds a pending card until its own `tool_result` lands.

Tests: e2e `stop-tool-status.spec.ts` (a stop the chat did not start, the chat's own Stop, and the live card
that must survive); `chat-history-merge.unit.ts` (both values of the running flag); `stream-state.unit.svelte.ts`
(`dropUnresolvedToolCalls`); `task-utils.unit.ts` (the state partition, including `status: in_progress` with
`execution_state: paused`); `backend/stream/app_stream_test.go` (which status frames clear the buffer, and
that a new `ExecutionState` cannot join the vocabulary undecided).

### A lock wait repeats, so a lost frame cannot strand the line

`lock_wait` is the one task event whose *repetition* is the contract. While a click is queued behind a lock,
`keylock.Table.LockWatched` re-sends it every `stream.LockWaitNoticeEvery` with a bigger `waited_seconds`, then
sends one frame with `acquired: true`. A state set by a single frame and cleared by a single frame would leave the
spinner running forever if the clearing frame were lost — a reconnect, a tab backgrounded at the wrong moment —
and the user would have to reload to be told the truth. Repeating costs one small frame every three seconds and
makes the worst case one repeat of staleness instead of an indefinite lie. Nothing on the server tracks whether a
client saw the frames: there is no state to get out of step.

Tests: `backend/stream/lock_wait_test.go` (both frames), `frontend/src/lib/services/__tests__/stream-handler.unit.ts`
(the acquisition clears it, an unknown domain still names a wait).

## What refuses the queue rather than joining it

The queue is persisted to IndexedDB, so a queued value outlives the tab and the session. Two requests are
therefore **refused**, not stripped — refusing keeps a request from quietly becoming something else:

- a `PUT /api/config` carrying a provider key that is not the server's masked placeholder
  (`carriesPlaintextAPIKey` in `api/config.ts`);
- a clone URL carrying credentials (`gitUrlCarriesCredentials` in `utils/git-url.ts`). The scp-style
  `git@host:org/repo` form carries no secret and still queues.

Both failure classes keep the DOM `name` (`AbortError`, `TypeError`) because a check that misses one
silently turns a cancellation into an error toast, or a queueable write into a failure — and **the name is
the test, never `instanceof DOMException`**: these are `Error`s that carry the name, so the call is
`isAborted(e)`. Tests: `api/__tests__/fetch-failure.unit.ts`, `api/__tests__/send-queue.unit.ts`.
