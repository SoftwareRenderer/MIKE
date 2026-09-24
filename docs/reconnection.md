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

**Tests:** e2e `reconnect-refresh.spec.ts`; the wake reading is held by `stream-unpainted.unit.ts`; the
merge above is held by `src/lib/utils/__tests__/chat-history-merge.unit.ts`, which drives the real stream
handler and the real message builder. Since `page.route` doesn't intercept WebSockets in
Playwright, the spec wraps the `WebSocket` constructor via `addInitScript` and uses `serverApi()` to
advance state. `ws-stale-reconnect.spec.ts` drops a live socket the same way for the banner and the
wake-up fetch.

## What a reconnect brings back, and what the client must fetch

A reconnect is a **new connection**, and nothing is held for a connection that has gone: the app stream
answers it with `project_counts` and whatever its own `subscribe_project` answers, and nothing that
happened while it was down. That is why `onConnected(true)` runs `silentRefresh()`, why the wake-up path
refetches the board over HTTP, and why `TaskChat` reloads history from the DB. The one replay that
survives a reconnect is the **per-task** one (`taskBufferFrames`, replayed on `subscribe_task`): it is
keyed by what the client asks for again, not by the socket it lost. That is also why a task's buffer is
cleared the moment its turn ends.

That replay holds **one frame per tool call**, because two frames for one call replay as two cards. The
live path sends a call's whole args and then only its growth, so the frames of one call are joined where
the replay is read (`foldToolCallFrames`). The frame handed over carries the whole input, not the last
delta: a card opened mid-call would otherwise start mid-JSON. Joining at the read costs one pass per subscribe
instead of one per frame, and it runs on the frames copied out of the buffer rather than inside the hub
lock. A full buffer of one call's deltas measured 26 ms of JSON there, and 0.9 ms for the same pass with
nothing to join. A delta whose start has already been trimmed stays a delta — there is nothing to fold it
into, and dropping it would hide the call.

**Tests:** `backend/stream/app_stream_test.go` (one frame per call, args from the start, a lone delta
kept as it arrived).

A replay is never written on the goroutine that asks for it: it is queued into the connection's own sender
(`sendQueue.pushReplay`) and comes out of it in order. It goes behind any replay already queued, so two
replays keep the order they were asked for, and ahead of every live frame, so a replay is not interrupted by what
the task said meanwhile. A replay gets **half the queue's budget**, not the queue's bound. A frame the queue
discards is a hole in a live stream; a frame a replay leaves behind is content the subscriber reads from the
database seconds later. So `pushReplay` trims its own batch to that budget, oldest first, and logs the count
naming the task — it is not a missed frame, and see *The slow client is released, not timed out*. A buffer
that could fill the whole queue would let the act of subscribing force the resync it exists to avoid. No
replay path writes the socket itself: the read loop and the initial-data worker both hand frames over.

A task's frame reaches a connection **exactly once**, which is a locking rule rather than a client one. The
hub hands the frame to the task's subscribers and adds it to that task's replay buffer in one step, and
`subscribe_task` reads the buffer in the same step that makes the connection a subscriber (`asm.mu`, taken
before `taskBufMu`). Split into two steps, a subscribe landing between them got the frame twice or never,
and a hole in a replay is something the client cannot see. **Tests:**
`backend/stream/app_stream_subscribe_race_test.go`.

## Who holds a task key, and for how long

`TaskChat` takes the task key when it mounts and releases it when it is destroyed, and it is the only
place that does either. The key lives exactly as long as the screen that reads it, so every mount is a
genuine new take — which is what earns a replay.

A key held across closing the detail is a dead end. Frames pour into a connection with no reader, and
while the key is held the client's per-key dedupe swallows the reopen's `subscribeTask`, so **no
`subscribe_task` frame goes out and the replay is never asked for**. That chat has only the rows, and a
tool call announced while its chat was closed has no card among them: the `tool_result` that lands later
has nothing to resolve, and nothing asks again.

Two consequences the client depends on. `subscribeTask`'s dedupe is about frames, not chances: a key is
deduped only while it is held, so the release on destroy is what makes the next mount's subscribe real
(`stream-subscription.unit.ts`). The replay also arrives while the mount is still loading its rows. So the
gate that drops frames for a task this screen does not believe is running must already be open: it opens
on the task row (below), never on the board's copy.

**Tests:** e2e `away-and-back.spec.ts` holds the rows shut while the chat is closed and reopened, so the
card it draws can only have come from the replay. Run it against a stale `frontend/dist` and it proves
nothing.

## The row decides, not the board's copy

`mergeLoadedHistory` takes a running flag. Given the truth it is right in both directions: `false` for a
task that is running rebuilds a pending `tool_call` row with its card hidden — the message stays, the work
vanishes; `true` for a stopped task leaves a leftover card spinning forever. The flag's *input* is the
thin half: `loadHistory` takes it from the `taskExecutionState` prop, the board's copy, and a board copy
is whatever arrived before this chat opened. Read a board from the mirror, or miss one `task_updated`, and
the rebuild hides a tool call that is running.

So the task's own row answers until the board's copy moves under it; `trustedExecutionState`
(`$lib/utils/execution-state.ts`) states the rule and says why a superseded row is not a second opinion.
`TaskChat` reads that row as the first thing its mount does, which costs nothing because the mount already
read it for the model and session cost. Every caller that *makes* a transition passes the row it got back:
the stop's `paused`, `commitAndDone`'s and `moveTask`'s updated task. The same value answers the frame
gate (`isLoading` → `suppressEvents`), which is why a replayed `tool_call` now reaches the screen for a
task the board still lists under Review.

**Tests:** `src/lib/utils/__tests__/execution-state.unit.ts` (the rule) and
`chat-history-merge.unit.ts` (the merge given the truth, both directions). E2E `away-and-back.spec.ts`
swallows a task's frames so its board copy stays stale while its tool call runs; the row read is then the
only thing that can draw the card.

## A discarded card releases its id

The `status: running` frame announces a run, and it discards any entry that is nothing but an unanswered
tool call — that call belongs to the run the frame ends, not the one it opens. What the frame supersedes is
what this screen saw announced, never what it read. A pending call carrying `messageId` is a row the server
wrote; a frame about the run says nothing about that row. The `tool_call` event is broadcast once (*The
reload meets the replay buffer*), so the replay carrying this frame may have lost the announcement that
would redraw the card. Delete the row's copy then, and the call has no card for the rest of the run: its
`tool_result` resolves nothing, and nothing asks again. That is why the machine that watched the run shows
the running card, and the one that opens the task does not.

The claim goes with the
entry. `processedToolIds` is what stops a second announcement of one id opening a second card. An id still
held when its card is discarded **swallows the announcement that follows** — the replayed one, or the
event `taskEventContext` delivers the instant a chat subscribes. Every frame of that call is then refused
as a duplicate and the card is never drawn: the `tool_result` resolves nothing, because the id was never
rendered and `unresolvedToolIds` never learns either. So `processedToolIds` lets go of exactly the ids it
discarded, and `unresolvedToolIds` keeps them — an unanswered call is still evidence that a card is owed.

**Tests:** `src/lib/services/__tests__/stream-handler.unit.ts`. E2E `away-and-back.spec.ts` holds the whole
stream back until the rows are read, then loses the call's announcement; the rows are the only evidence
the screen holds.

## The slow client is released, not timed out

Every frame for a connection goes through that connection's own queue (`sendQueue`) and one sender
goroutine writes it, so a browser that stopped reading costs nobody else in the server. The queue is
bounded by frame count and bytes; over the bound the **oldest** frames go, which leaves what the client
receives a contiguous tail with a hole behind it — and a chat holding a hole never becomes right by
waiting.

**A replay never fills the queue.** It arrives with half the budget (*What a reconnect brings back*)
because it is a head start and not a reservation: what it cannot queue, the subscriber fetches from the
database in the same breath it asked. A truncated replay is a log line naming the task and the count, never
a resync.

So once such a queue has drained, the server ends the connection (`StatusTryAgainLater`, one
`component=stream` warning naming how many frames were missed) and the client's existing path does the
rest: `onclose` → `onConnected(false)` → reconnect → `onConnected(true)` →
`silentRefresh()` and `TaskChat.loadHistory()`, plus the per-task replay its `subscribe_task` answers.
That is why there is no "you missed frames" frame to add: the resync the app has is a connection, and
the only other choice — holding the connection open on a gap it cannot report — is a view that lies
until someone reloads. Nothing here puts a deadline on a wait for this server (AGENTS.md →
Reconnection & Offline); a slow client is degraded, not timed out.

**Tests:** `backend/stream/app_stream_sender_test.go` (a stalled subscriber does not hold the turn, the
queue's two bounds drop the oldest, the missed-frames connection is ended). A task whose buffer holds more
than the replay budget keeps its connection and gets the newest frames.

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

**The buffer holds exactly what is not a row yet.** The window read ends where durability ends, and the
replay starts there. The two views of one conversation are complementary rather than overlapping: the read
answers the rows, the replay answers what has no row.

The executor is what says where that line is, by calling `AppStreamManager.DurableThrough` after a
conversation write lands — the assistant row of an LLM call, and each `tool_call` row's result. Not at the
`tool_result` frame: the loop sends that frame *before* its row is written, so trimming there would drop
content no row holds. What a trim keeps is one table in `backend/stream/app_stream.go`: every frame of a
call with no result yet, and the newest value of each event that is a current value rather than content
(`status`, `stats`, `prompt_progress`, `queue`, `lock_wait`). What it drops is the text that is a row now —
streamed tokens and thinking, a finished call's arguments and output, the user and summary rows. So a client
opening a task mid-turn is owed seconds of stream rather than the turn, and the row read beside its
subscribe brings the rest.

One exception holds the whole rule up: **a client with no rows is owed the buffer as it stands.** The trim
trades replay content for rows the client can read, so it cannot run while any subscriber holds the task
with `no_rows` on its `subscribe_task` frame. For that connection the frames are the transcript. `TaskChat`
sets the claim when a window read failed outright on a task still running, and clears it with the first read
that answered. While it stands the chat says the running turn is all it has
(`noticeForWindowRead` → `'owing'`). A second `subscribe_task` for a key the connection already holds is how
that change is declared: it moves the claim and earns no replay. A replay the client already has is the
doubled text the merge cannot undo.

`loadHistory` runs on reconnect, on task switch and after a stop, and it still merges the two views by row
id: `mergeLoadedHistory` (`src/lib/utils/chat-entries.ts`) owns the rule and states it at its definition.
The fact it is built on is that a `tool_call` event is broadcast once, so an entry discarded before its row
exists loses that card until the page is reloaded.

Being in progress is what separates a pending `tool_call` row that is work from one that is a leftover:
a stopped task will never write the missing result, so that card is hidden while the message around it —
its text, its thinking, the cards that did finish — stays. Where the running flag comes from is its own rule
(*The row decides, not the board's copy*); this section is only about what the merge does with it.

**Tests:** `backend/stream/app_stream_test.go` for the partition and the claim, and
`backend/exec/durable_replay_test.go` for the seam — a real turn, a subscriber arriving after the result
row, and no finished iteration in what it is sent. `DurableThrough` keeps a running call and the newest
state; a client with `no_rows` is owed what a trim would drop. Removing either executor call site turns one
of them red.

## A read that did not answer says so

`getTaskMessages` answers from this device's mirror when the network call fails (`api/tasks.ts`).
`loadHistory` ignores everything its read throws except a 404, so the transcript can be the last window
this browser saved, or nothing at all — indistinguishable from a task with no conversation.
`noticeForWindowRead` (`$lib/utils/chat-window.ts`) is the one decision that tells them apart: `'cached'`
when the window carried `isFromCache`, `'owing'` when the read failed on a running task this chat holds the
key for, `'failed'` for anything else. `ChatEntries` renders the sentence above the list with **Retry beside
it** — the way to ask again without leaving the view. The `'owing'` sentence is also what declares the claim
described in *The reload meets the replay buffer*, which is the difference between a chat that fills in as
the turn continues and one that stays empty. `mayBeBehind()` is false for an idle task with an empty
transcript, so a return from a hidden window (*The tab that comes back*) is the other thing that refetches
by itself. A live read clears the notice and the claim, so neither outlives its answer.

**A mirror copy is not a reading of the server** — it does not redraw a transcript that has rows.
`windowIsReading` (`$lib/utils/chat-window.ts`) is that gate. The stored window paints a screen holding no
rows — the open that never reached the server — and nothing else. It is this browser's own last answered
read, so every turn and card that arrived since is missing from it: redrawing from one at a return is how a
transcript goes backwards. The notice and the `no_rows` claim still move on what the read was, so the reader
is told and Retry still asks again.

It is not a chat entry: a read that lands rebuilds the transcript, so a synthetic entry would be appended
again by each rebuild. What the queue holds is rebuilt too — see *A pending outgoing message is
the queue's projection* — so a read that failed does not hide a message this browser wrote.

**Tests:** e2e `away-and-back.spec.ts` — a read aborted from the first open with no cached copy,
retried once the server answers again, and a reload onto the stored copy. `chat-window.unit.ts` holds the
notice decision itself, including that a mirror read never reports `'owing'`, and `windowIsReading`,
including that a mirror copy never replaces rows the screen holds.
`chat-entries-history-notice.unit.svelte.ts` renders which sentence lands for which notice, and the Retry
beside it.

## The tab that comes back

`createTabReturn` (`$lib/composables/use-tab-return.ts`) is the one listener set for "this window has come
back", and `needsReturnReconcile` (`$lib/utils/tab-return.ts`) is the one rule. Three signals answer for one
gesture — `visibilitychange`→visible, the window's own `focus`, and `pageshow` from the back/forward cache —
and the rule collapses them to a single call, never twice inside `RETURN_DEDUPE_MS`.

| view | what says it could be behind | what it refetches |
|---|---|---|
| board (`App.svelte`) | always — a stale list looks current whatever the socket did | `reconnectNow()`, then `refreshBoard()`, retried once after a second |
| chat (`TaskChat.svelte`) | after a window that was hidden: nothing, because `hiddenIsEvidence` is set. Otherwise: in flight, a card still pending, a turn still open on screen, or a call it owes a card for | the task row, then the messages |
| diff (`CardDetail.svelte`) | in flight or streaming | `loadDiff()` |

**A window that was hidden is its own evidence, for a view that cannot see its own gap.**
`visibilitychange`→visible says this screen ran unread. What the gate reads — a pending card, an open
assistant id, `unresolvedToolIds` — is what a turn missed whole takes with it. No card was drawn, no id was
opened, nothing was left unresolved. Ask such a screen whether it is behind and it answers no, which is the
shape of a chat left on a stale transcript while its task answered twice. So `ReturnSignal` carries
`hiddenIsEvidence`, the chat sets it, and a `visible` return reconciles without the screen's own answer. The
diff does not set it: its content is mirrored by `task_diff_stats` on the project channel, the same frames
that move the board's copy it gates on. And `loadDiff()` resets the open file, the checkpoint overlay and the
from/to comparison — reloading it on every tab switch is a loss the chat does not pay.

**A frame that arrived to an unfocused window is the evidence for a window that never hid.** A `focus` or
`pageshow` return needs the view's own answer *and* one of two readings of the stream. The first is
`AppStream.msSinceUnpainted()`: the newest frame that arrived while `document.hasFocus()` was false, from
which `createTabReturn` keeps only what is newer than that view's own last reconcile. The second is the
stream silent for `WAKE_FRAME_GAP_MS` on `AppStream.msSinceFrame()` — the *liveness* clock, which restarts
at every close, so a dead socket counts as silence.

Silence alone cannot carry the unfocused case, because it is a measurement a healthy stream never fails to
pass: `httputil.KeepaliveFrame` arrives every 27 s and `msSinceFrame()` is stamped by every inbound frame,
so on a live connection that clock never reaches the 30 s gap and the branch could only ever fire for a
zombie socket — the state the reconnect path already serves. And a window that never hid has no third
signal: Android's background window, a split-screen pane, and a second browser window on the same app are
all `visible` throughout, so `visibilitychange` never fires and `document.hidden` never becomes true. What
content arriving to an unfocused window proves is unread content, which is what the rule needed.

Two rules keep that reading honest. The keepalive is not content (`isKeepaliveFrame`, `$lib/api/events.ts`)
and never marks anything unread — counting it would make an idle app look like a page that missed a turn,
and a returning tab missed nothing. And the comparison is against each view's own last reconcile, not a
cleared counter: three watchers share one stream, and a reading one of them consumed would leave the other
two with nothing to ask about. Silence stays in the rule because it answers the case content cannot — a
socket that received nothing because it was gone.

`msSinceFrame()` deliberately stays a liveness clock. Had the gate been switched to a content clock, the
board — whose `mayBeBehind()` is constant `true` — would refetch on every window switch of an idle app.

**The chat's gate is `StreamState.unresolvedToolIds`, not the task's status — for the signals that never hid
the page.** `isLoading` and
`task.status !== 'in_progress'` were the gate, and both fail in the direction that matters: a stopped task
stays `in_progress` (see *A tool call reads as running only while its task is in flight*), and both are
false once the turn has ended, which is exactly when the missed frames have already taken the card with
them — `dropUnresolvedToolCalls` removes the entry whose call was never answered. `unresolvedToolIds` is
the evidence that survives the card: a tool call this screen saw announced and holds no answered card for,
settled when an answer lands on a card (an answer with **no** card to land on is itself the evidence, which
is how a lost `tool_call` frame is caught). A rebuild from the rows narrows it to the calls still waiting
(`pendingToolIds`) rather than clearing it. A call the rebuild left answered is settled, and one it left
pending is still owed; a call it left no card for is settled too, because the card was hidden for a task
that left flight and no answer is coming for it.

`reconcileAfterAbsence` then decides work-vs-leftover from the row it just read — `getTask().execution_state`
through `taskIsInFlight` — and passes that into `loadHistory`, never the board copy, and records the reading
so the next rebuild starts from it too (*The row decides, not the board's copy*). A row read that did not
answer keeps the reading of the last row this screen read — `knownExecutionState`, not `taskExecutionState`.
A window that has been unread is the window whose board copy missed its frames, and that reading gates
`dropStaleStreamingState`, which deletes the running turn's entries. The rebuild narrows `unresolvedToolIds`
to the calls still waiting **only when the rows came from the server**. A mirror copy settles the id of a
card that was lost; with the evidence gone `mayBeBehind()` answers no, and the next return asks for nothing.
The merge drops live
pending cards when told the task is not in flight, and the board can carry an `idle` from a
duplicate-message answer (`backend/exec/handler_http.go`) while the call is still running.

A turn's first frames are not part of this story: `suppressEvents` is a rule about the task this screen
believes is running — the column plus `isLoading`, whose `execution_state` comes from the row (*The row
decides, not the board's copy*) — and it lags the turn it gates. The `status: running` frame is not gated by
that flag and so opens the gate for the turn it announces. `scheduleScroll` likewise does nothing while the tab is hidden (rAF is
paused there; the follow tracker re-pins on return).

**Tests:** e2e `tab-return-chat.spec.ts` swallows `tool_result` task events at the socket (a listener added
in the wrapped constructor runs before the app's `onmessage`, so `stopImmediatePropagation` is where the
frame can be removed) and asserts they really were swallowed; the turn ends with the card gone, and one
`visibilitychange` must bring it back holding the stored output. `tab-return.unit.ts` holds the gate rule
itself, including that a screen which cannot see its own gap reconciles on the hidden signal alone, that one
which was never hidden reconciles on unread content, and that content older than its own reconcile is not
evidence. `stream-unpainted.unit.ts` holds the reading: the keepalive never marks content unread, neither
does a focused window's traffic, and the newest unread frame is the one counted. `chat-window.unit.ts` holds
the half the reconcile reads — that only a row read that reached the server redraws a transcript.

## The board on screen belongs to one project

`App.svelte` holds one task list, and it is the board of the project named in the URL. Three things can
hand it a task that belongs to another project, and each is answered where it arrives:

- **A board response that lost the race.** `refreshBoard`, `silentRefresh`, `restoreTaskFromUrl` and
  `selectProject` capture the project id before their awaits and write through `applyBoard(board,
  projectId)`, which drops the write when that project is no longer on screen.
- **A board frame from a project the client left.** `task_updated` and friends go only to that
  project's subscribers, but a frame already in flight outlives `unsubscribe_project`, and the
  per-task replay buffer (`taskBufferFrames`) answers a `subscribe_task` on a *new* connection. So the
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

## The mirror follows the server, and the queue carries intent

`board:<projectId>` holds what this browser received and nothing else: a board response it fetched, or the
board its own frames left it holding. Nothing is fetched in order to populate it — no background sync, and no
read whose only purpose is to fill the store. A board read out of the mirror is never written back, because
`cachedAt` is what the age line counts from.

Pending intent is not in the mirror either. `offline-queue` is the record of a write the server has not
answered, and `applyBoard(…, 'mirror')` replays it onto every mirror landing — the one place that replays it,
because `applyQueuedActionsToBoard` is not idempotent and a second pass opens a second card. Keeping guessed
state out of the mirror is what keeps two sentences checkable: *Viewing cached data* means the server answered
what is on screen, and a write the server refused cannot settle in as a permanent card.

**Tests:** `src/lib/services/__tests__/board-mirror.unit.ts` (one write per coalescing window; an empty flush
rewrites nothing), `src/lib/api/__tests__/queue-mirror-landing.unit.ts` (one row per pending write, and the
replay is not idempotent), e2e `mirror-follows-frames.spec.ts` (a card moved by a frame is in its new column
after a mirror reload).

## The age line measures data, not the socket

While there is no stream, the banner adds "Data from {age}". The clock it counts from is the moment
data **landed** — a live board landing or a `task_updated` / `task_deleted` frame writes it, and a board read
from the mirror keeps the mirror's own `cachedAt`. `null` (nothing has landed since this client opened)
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

## A pending outgoing message is the queue's projection

A message queued while offline has no row: nothing reached `finalizeTurn`, and the mirrored window holds rows
the server answered. So the transcript derives its pending rows from `offline-queue` on every rebuild
(`pendingSendEntries`, `$lib/utils/pending-sends.ts`) — the clock, the one notice beside them, and the
ReviewBar's *Resume*, which reads the last non-error entry. Deriving rather than remembering keeps the
mirror's claim true and survives a reload. A row is named for the queue action, so a rebuild draws the same row
instead of a second one; an entry this session already drew is skipped by the action id it carries
(`ChatEntry.actionId`).

A refusal has already left the queue, so its verdict is stored with the text
(`queued-failed:<projectId>:<taskId>`) and returns as the `send_refused` row; a fresh send for that task
retires it. A refused message is never simply gone.

When a flush ends, `TaskChat` re-reads the window. The read that filled the transcript went out before the
flush applied anything, and without that re-read an applied send goes from visible to nothing until some
unrelated reload.

One use of the same clock stays session-local: the message the executor will take after a summarization is a
row that exists, and nothing on it says it waited, so a reload cannot badge it.

**Tests:** `src/lib/utils/__tests__/pending-sends.unit.ts` holds the clock for what waits, none for what was
refused, the order, and the skip. `src/lib/api/__tests__/queue-refused-send.unit.ts` holds the verdict
outliving the queue entry, and a fresh send retiring it. E2E `offline-send-queue.spec.ts` shows the pending
message after a reload while still offline, and the applied row reaching the screen.

## The behaviour, in full

`onConnected(true)` in `App.svelte` runs `silentRefresh()` once `initialLoadComplete`; `TaskChat` reloads history from the DB on reconnect because **turns completed while offline are never replayed** (buffer cleared at turn end). `offline` queues writes immediately, except a value that carries a secret — an unmasked provider key, or a clone URL with credentials in it — which is refused because the queue is persisted; `online` flushes the queue and calls `reconnectNow()` to skip backoff; `visibilitychange` refreshes the board (phone-sleep), and the open task's chat and diff answer the same signal together with the window's own `focus` and a bfcache `pageshow` — the chat whatever it believes, because a missed turn leaves no local trace, the diff only while the task looks able to move under it, never on the task's status. Project re-subscription forces a re-send of `task_diff_stats`.

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

Tests: `backend/stream/lock_wait_test.go` (both frames), `frontend/src/lib/components/__tests__/queue-indicator.unit.svelte.ts`,
`frontend/src/lib/services/__tests__/stream-handler.unit.ts`
(the acquisition clears it, an unknown domain still names a wait).

### The same frame carries a wait for the model, which is not a lock

A second producer sends `lock_wait` with `domain: "model"`: a task's provider that has sent nothing for a while
(`streamSilenceWatcher`, `backend/exec/loop.go`). It repeats and withdraws on the same `LockWaitNoticeEvery` cadence
because a frame that must survive a lost frame has the same shape whatever it waits on, and it adds one field,
`phase` — `first_token` while no token has arrived, `mid_response` after one has, which is the difference between
"still processing the prompt" and "generating without streaming" and therefore between two different sentences in
`QueueIndicator`.

The two producers differ in one thing that is not on the wire: **a lock wait ends the wait, a model wait does not
end the call.** The client-side notice measures silence and says what it measures; only a per-provider
`stream_idle_seconds` ceiling ends a stream, and its default of 0 means silence is waited out however long it
lasts. A provider has been measured holding 16,267 generated tokens behind 76-89 seconds of silence and then
finishing normally, so silence is never reported as a lost connection and the notice is the whole response to it.

Tests: `backend/exec/quiet_stream_test.go` (the frame pair, and that a resumed stream is not retried).

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
## Checking this

`cd frontend && npm test` covers `taskIsInFlight`, the offline queue, the mirror's coalescing and the pending
rows derived from the queue. `cd frontend && npx playwright test reconnect-refresh ws-stale-reconnect` drives a
lost connection; `mirror-follows-frames offline-send-queue` drive the mirror and the queue across a reload.
