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
- A window coming back refreshes the board, the open task's chat and its diff — phone-sleep included — on
  the same signal as the window's own `focus` and a bfcache `pageshow`. See *The tab that comes back*. The
  listener is on `window`, which is where the browser's own event bubbles to from `document` — a synthetic
  `new Event(...)` fired at `document` does not bubble and reaches nothing.

**Tests:** e2e `reconnect-refresh.spec.ts`; the merge above is held by
`src/lib/utils/__tests__/chat-history-merge.unit.ts`, which drives the real stream handler and the real
message builder. Since `page.route` doesn't intercept WebSockets in
Playwright, the spec wraps the `WebSocket` constructor via `addInitScript` and uses `serverApi()` to
advance state. `ws-stale-reconnect.spec.ts` drops a live socket the same way for the banner and the
wake-up fetch.

## What a reconnect brings back, and what the client must fetch

`?sync=1` replays the ring buffer of the **connection** that asks (`bufferRaw` → `clientState.ringBuffer`
in `backend/stream/app_stream.go`), and a reconnect is a new connection with an empty one — so a
reconnected app stream receives `project_counts` and whatever its own `subscribe_project` answers, and
nothing that happened while it was down. That is why `onConnected(true)` runs `silentRefresh()`, why the
wake-up path refetches the board over HTTP, and why `TaskChat` reloads history from the DB. The replay
that does survive a reconnect is the **per-task** one (`getTaskBuffer`, replayed on `subscribe_task`),
which is why a task's buffer is cleared the moment its turn ends.

## One frame per key: what a reconnect may say

A connection holds a subscription **per key** (`backend/stream/app_stream_subscription_test.go`), so one
`unsubscribe_*` leaves it, and the work a subscription starts — a project's initial-data pass, a task's
buffer replay — belongs to the key rather than to the frame that arrived. The client's side is `owed`
(`src/lib/api/stream.ts`): the newest intent for each key, recorded when `subscribeProject`/
`unsubscribeProject`/`subscribeTask`/`unsubscribeTask` run, never a log of frames. It is a projection of
the two subscription sets, which is what bounds it — an outage costs at most one frame per key the app
has opened however long it runs, where a message log replayed a frame per navigation and re-subscribed
every held key on top, so a project opened during an outage was subscribed twice and unsubscribed once.
That second hold never left, and the project's branch and filesystem watchers stayed armed for the life
of the socket.

Two rules the server's holding depends on, both held by the shape of `owed`: a key's `unsubscribe_*` is
never sent after a later `subscribe_*` for that key, because the newest intent replaces the older one; and
a subscribe the server never received is undone by **dropping** it, not by sending its cancellation — the
connection that would have carried it never held the key. `onopen` writes what is owed, then one subscribe
per key still held that the owed pass did not already cover.

`subscribe_project` is the one frame that waits: it goes out after the async `getKnownStats` provider
answers, so the skip-if-known handshake still declares what the client already holds. The **intent** is
recorded in `owed` at call time regardless, so an enter-and-leave during that wait costs nothing, and when
the provider answers its frame is written only if `owed` still holds it — otherwise the app left that
project, and a subscribe nothing will unsubscribe is exactly what the old ordering wrote.

**Tests:** `src/lib/api/__tests__/stream-subscription.unit.ts` (coalescing, the bound, the provider's
ordering) with `stream-reconnect.unit.ts` for the ladder around it; the server half is
`app_stream_subscription_test.go` and `project_subscription_watchers_test.go`.

## The boot offer and one stream per page

`/api/auth/session` decides whether the app or the offline mirror is on screen. A 2 s timer *offers*
the mirror (`hasLocalCache`) when a request has not answered and the mirror holds projects; the answer
that arrives afterwards retracts the offer (`hasLocalCache = false`) and the copy says the wait is slow,
not that the server is unreachable. Only one `/api/stream` is open at a time (`closeAppStream()` in
`App.svelte`): the field holding it is a single reference, so a second `createAppStream` leaves the
first connected, still firing handlers, and unreachable to close — the exact shape the boot race took
when the user clicked Offline Mode and the session then answered.

**Tests:** e2e `boot-slow-session.spec.ts` (the offer comes down by itself; one live stream afterwards).

## An unanswered attempt is replaced, not awaited

A `WebSocket` to a host that accepts nothing never fires `onclose`: the browser sits in `CONNECTING` on the
OS TCP timeout (minutes) while the page waits, and `readyState === CONNECTING` used to mean *ask again later*
— so the banner's Connect button, a window coming back and the browser's own `online` event all declined to
do anything, and on a first attempt nothing had even scheduled a retry. Silence decides instead, whatever
state the socket claims: `lastActivityAt` is stamped when an attempt **starts**, so an outstanding handshake
is measured by the same two rules as an OPEN peer that stopped talking — `reconnectNow()` replaces one
silent for `WAKE_FRAME_GAP_MS`, and the heartbeat, which now runs from the attempt rather than from `onopen`,
retires one silent for `STALE_THRESHOLD_MS` and hands over to the ladder. The threshold is not a new number:
`WAKE_FRAME_GAP_MS` is also the ladder ceiling, so a replaced attempt is never slower than the ladder's own
pace, and a fresh attempt is by definition not silent, which is what keeps it from being torn down.

This bounds one attempt's exclusivity, not the wait: nothing is failed, no offline flag is set, and the
ladder goes on trying for as long as the page is open. See *No client-side deadline replaced it* — that rule
is about answering a request, and replacing a handshake that no one answered is asking the question again.
The cost is a genuinely slow live handshake (> `WAKE_FRAME_GAP_MS`) being torn down by a Connect click or a
wake signal, both of which arrive with the person's own judgement that it is dead.

One consequence is that `msSinceFrame()` counts from the start of an in-flight attempt, so a `focus` or
`pageshow` landing during one does not fire the HTTP reconcile (*The tab that comes back*) — the attempt is
the thing that will bring the answer, and `visibilitychange` is never gated on silence. Exactly one socket is
live at a time regardless: `connect()` closes what it finds and `connectGen` stale-outs the handlers of the
one it replaced.

**Tests:** `src/lib/api/__tests__/stream-reconnect.unit.ts` (an attempt with no answer is replaced;
the heartbeat retires a first attempt that never completes; a fresh attempt is not piled on; the clock
belongs to the attempt).

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

## The tab that comes back

`createTabReturn` (`$lib/composables/use-tab-return.ts`) is the one listener set for "this window has come
back", and `needsReturnReconcile` (`$lib/utils/tab-return.ts`) is the one rule. Three signals answer for one
gesture — `visibilitychange`→visible, the window's own `focus`, and `pageshow` from the back/forward cache —
and the rule collapses them to a single call, never twice inside `RETURN_DEDUPE_MS`.

| view | what says it could be behind | what it refetches |
|---|---|---|
| board (`App.svelte`) | always — a stale list looks current whatever the socket did | `refreshBoard()`, plus `checkStale()` / `reconnectNow()` for the socket |
| chat (`TaskChat.svelte`) | in flight, a card still pending, or a call it owes a card for | the task row, then the messages |
| diff (`CardDetail.svelte`) | in flight or streaming | `loadDiff()` |

`visibilitychange` reconciles whenever the view says it could be behind; `focus` and `pageshow` only after
the stream has been silent for `WAKE_FRAME_GAP_MS`, read from `AppStream.msSinceFrame()` (which restarts its
clock at the last close, so a dead socket counts as silence). A window that merely lost focus keeps
receiving frames, so silence is the only evidence the machine slept or the socket went zombie without the
tab ever going hidden — and with two windows open on one app, `document.hidden` never becomes true at all.

**The chat's gate is `StreamState.unresolvedToolIds`, not the task's status.** `isLoading` and
`task.status !== 'in_progress'` were the gate, and both fail in the direction that matters: a stopped task
stays `in_progress` (see *A tool call reads as running only while its task is in flight*), and both are
false once the turn has ended, which is exactly when the missed frames have already taken the card with
them — `dropUnresolvedToolCalls` removes the entry whose call was never answered. `unresolvedToolIds` is
the evidence that survives the card: a tool call this screen saw announced and holds no answered card for,
settled when an answer lands on a card (an answer with **no** card to land on is itself the evidence, which
is how a lost `tool_call` frame is caught), and cleared by any rebuild from the rows.

`reconcileAfterAbsence` then decides work-vs-leftover from the row it just read — `getTask().execution_state`
through `taskIsInFlight` — and passes that into `loadHistory`, never the board copy. The merge drops live
pending cards when told the task is not in flight, and the board can carry an `idle` from a
duplicate-message answer (`backend/exec/handler_http.go`) while the call is still running.

A turn's first frames are not part of this story: `suppressEvents` is a board-status rule and lags the turn
it gates, so `isLoading` joins it — the `status: running` frame is not gated by that flag and so opens the
gate for the turn it announces. `scheduleScroll` likewise does nothing while the tab is hidden (rAF is
paused there; the follow tracker re-pins on return).

**Tests:** e2e `tab-return-chat.spec.ts` swallows `tool_result` task events at the socket (a listener added
in the wrapped constructor runs before the app's `onmessage`, so `stopImmediatePropagation` is where the
frame can be removed) and asserts they really were swallowed; the turn ends with the card gone, and one
`visibilitychange` must bring it back holding the stored output. `tab-return.unit.ts` holds the gate rule
itself.

## The board on screen belongs to one project

`App.svelte` holds one task list, and it is the board of the project named in the URL. Three things can
hand it a task that belongs to another project, and each is answered where it arrives:

- **A board response that lost the race.** `refreshBoard`, `silentRefresh`, `restoreTaskFromUrl` and
  `selectProject` capture the project id before their awaits and write through `applyBoard(board,
  projectId)`, which drops the write when that project is no longer on screen.
- **A board frame from a project the client left.** `task_updated` and friends go only to that
  project's subscribers, but a frame already in flight outlives `unsubscribe_project`, and the
  per-task replay buffer (`getTaskBuffer`) answers a `subscribe_task` on a *new* connection. So the
  client checks what the frame names:
  `mergeTaskUpdateIntoBoard` (`src/lib/utils/task-utils.ts`) appends an unknown task only when its
  `project_id` is the project on screen. `commit_status` and `task_deleted` name no project, so those
  are matched against the tasks the board already holds.
- **A queued write of another project.** The offline queue is global; `applyQueuedActionsToBoard(tasks,
  projectId)` replays onto one board only the actions that name it.

While a switched-to project's live board is in flight the screen holds that project's own cached copy
(`showCachedBoard`, from `board:` + the project id), never the previous project's list — which is why
the list is emptied before the switch is painted. The cache banner stays off unless the socket is down,
so a slow but live switch does not claim to be stale.

## The age line measures data, not the socket

While there is no stream, the banner adds "Data from {age}". The clock it counts from is the moment
data **landed** — `refreshBoard`, `silentRefresh` and `selectProject` write it, and a board read from
the mirror keeps the mirror's own `cachedAt`. `null` (nothing has landed since this client opened)
answers an empty string, and the line is left out: never "just now".

Two things used to make the line unable to leave "just now", and neither is a clock any more: it was
re-zeroed on every `onclose` (the retry gap is at most 30 s against a 60 s floor), and a board served
from the cache stamped itself as arriving now. `dataAgeText` (`$lib/utils/format-time.ts`) holds the
rule and `timeAgo` holds the rungs.

The countdown and the age are not in an announced region: they change every second. The banner is
plain markup and `App.svelte`'s `sr-only` region carries the transitions only (down, back) — one
sentence per change instead of ~29 a minute.

**Tests:** `src/lib/utils/__tests__/format-time.unit.ts` (unknown ≠ just now); e2e
`ws-stale-reconnect.spec.ts` (a drop announces one sentence with no digits in it, and the way back is
announced).

**Tests:** `src/lib/utils/__tests__/task-utils.unit.ts` (a frame of another project never appends) and
`src/lib/api/__tests__/queue-board-scope.unit.ts` (a queued action of another project never replays).

## The mirror banner carries its own way back

`servingFromCache` and `wsConnected` are both true whenever a read answered from the mirror while the
socket stayed up — `selectProject`'s fallback after a failed board fetch, and any board carrying
`isFromCache`. The "Reconnecting…" banner is off in that state, so the cache banner's own Connect button
is the only route back: `retryLiveNow` skips the socket's backoff and runs `silentRefresh()`, which puts
live data on screen or leaves the banner exactly where it was. The button is hidden while the socket is
down, because the reconnecting banner already offers it. No spec covers the button itself.

## Behaviour in full (moved from AGENTS.md)
### Reconnection & Offline
`onConnected(true)` in `App.svelte` runs `silentRefresh()` once `initialLoadComplete`; `TaskChat` reloads history from the DB on reconnect because **turns completed while offline are never replayed** (buffer cleared at turn end). `offline` queues writes immediately, except a value that carries a secret — an unmasked provider key, or a clone URL with credentials in it — which is refused because the queue is persisted; `online` flushes the queue and calls `reconnectNow()` to skip backoff; `visibilitychange` refreshes the board (phone-sleep), and the open task's chat and diff answer the same signal together with the window's own `focus` and a bfcache `pageshow` — gated on the screen being able to be behind, never on the task's status. Project re-subscription forces a re-send of `task_diff_stats`. Mechanism + test approach: `docs/reconnection.md`.

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

## A refused queued write is not a delivered one

A queued action leaves the queue for two reasons: the server applied it, or it refused it outright
(404, 403 — a task deleted while the tab was shut, a project that no longer exists). `getQueuedTaskIds()`
answers "no" for both, so a UI that clears a "queued" marker on that answer reports a refusal as a
delivery. `doFlush` therefore records what it dropped (`getDroppedTaskIds()` / `getDroppedActions()` in
`api/queue.ts`), the record belongs to one flush, and a fresh write about a task retires its verdict.

On top of that: `App.svelte` toasts the count from `onQueueFlushed`, and `TaskChat` rewrites the
queued notice under its own code (`send_refused`) rather than deleting it — a notice still wearing
`send_queued` would be cleared by the *next* flush's reading.

**Tests:** `api/__tests__/queue-refused.unit.ts` (refused ≠ flushed, reported once, retired by a new
write).
