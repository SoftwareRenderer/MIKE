# Reconnection Refresh — Mechanism

Read this before touching WS connect/reconnect handling in `App.svelte`,
`frontend/src/lib/services/reconnect-banner.svelte.ts`, `TaskChat`, or the offline write queue. The hard
rules live in `AGENTS.md → Reconnection & Offline`.

When the WebSocket drops and reconnects, the current view (board, task view) updates automatically:

- `onConnected(true)` in `reconnect-banner.svelte.ts` runs the app's `silentRefresh()` if
  `initialLoadComplete` is true.
- Project re-subscription forces the server to re-send `task_diff_stats`, refreshing the diff panel.
- `TaskChat` registers `useConnectionRestored` — the false→true edge of the connection store — and reloads
  history on reconnect; the value read at mount is not that edge. Turns completed while offline aren't
  replayed (buffer cleared at turn end), so DB reload is required.
- The task-event reader (`useTaskEvents`) arms itself only after that history read, and takes each frame
  exactly once: a handler whose writes the reader can read back re-delivers its own frame and restarts the
  turn.
- Browser `online`/`offline` events complement the WS backoff: `offline` queues writes immediately
  (the exceptions are a config carrying a real provider API key and a clone URL carrying credentials —
  both refused, never persisted);
  `online` flushes the queue and calls `reconnectNow()` to skip backoff.
- A window coming back refreshes the board, the open task's chat and its diff — phone-sleep included — on
  the same signal as the window's own `focus`, a bfcache `pageshow`, and a page that finds its own clock has
  jumped. The chat and the diff ask one cheap digest of the server first, because neither can prove its own
  currency; see *The tab that comes back*. The
  listener is on `window`, which is where the browser's own event bubbles to from `document` — a synthetic
  `new Event(...)` fired at `document` does not bubble and reaches nothing.

**Tests:** e2e `reconnect-refresh.spec.ts`; the edge rule and the arming rule are held by
`src/lib/composables/__tests__/connection-restored.unit.svelte.ts` and
`src/lib/composables/__tests__/task-events-delivery.unit.svelte.ts`. The wake reading is held by
`stream-unpainted.unit.ts`; the merge above is held by `src/lib/utils/__tests__/chat-history-merge.unit.ts`,
which drives the real stream handler and the real message builder. Since `page.route` doesn't intercept
WebSockets in
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
`TaskChat` asks that row at the top of its mount, beside the thread list and the newest window. It applies
the row before taking the task key: the key is what earns the replay, and the gate must be open before it
arrives. Reading the row costs nothing extra because the mount already read it for the model and session
cost. Every caller that *makes* a transition passes the row it got back:
the stop's `paused`, `commitAndDone`'s and `moveTask`'s updated task. The same value answers the frame
gate (`isLoading` → `suppressEvents`), which is why a replayed `tool_call` now reaches the screen for a
task the board still lists under Review. The two panels that *decide* from the state — the awaiting/queue
line in `ChatEntries` and `ReviewBar`'s paused bar — are handed that same value, not the prop, because both
are the question `isTaskPaused` and `isLoading` answer.

**Tests:** `src/lib/utils/__tests__/execution-state.unit.ts` (the rule) and
`chat-history-merge.unit.ts` (the merge given the truth, both directions). E2E `away-and-back.spec.ts`
swallows a task's frames so its board copy stays stale while its tool call runs; the row read is then the
only thing that can draw the card.

## A discarded card releases its id

The `status: running` frame announces a run, and it discards any entry that is nothing but an unanswered
tool call — that call belongs to the run the frame ends, not the one it opens. What the frame supersedes is
what this screen saw announced, never what it read. A pending call carrying `messageId` is a row the server
wrote; a frame about the run says nothing about that row. `tool_progress` names the row while the call is
still running, so the evidence is on screen before any answer — `docs/api-contract.md` → *What an
interrupting message may delete*. The `tool_call` event is broadcast once (*The
reload meets the replay buffer*), so the replay carrying this frame may have lost the announcement that
would redraw the card. Delete the row's copy then, and the call has no card for the rest of the run: its
`tool_result` resolves nothing, and nothing asks again. That is why the machine that watched the run shows
the running card, and the one that opens the task does not.

The claim goes with the
entry. `processedToolIds` is what stops a second announcement of one id opening a second card. An id still
held when its card is discarded **swallows the announcement that follows** — the replayed one, or the
event `taskEventStore` delivers the instant a chat arms its reader. Every frame of that call is then refused
as a duplicate and the card is never drawn: the `tool_result` resolves nothing, because the id was never
rendered and `unresolvedToolIds` never learns either. So `processedToolIds` lets go of exactly the ids it
discarded, and `unresolvedToolIds` keeps them — an unanswered call is still evidence that a card is owed.

**Tests:** `src/lib/services/__tests__/stream-handler.unit.ts`. E2E
`away-and-back.spec.ts` holds the whole
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

`/api/auth/session` decides whether the app or the offline mirror is on screen, and
`frontend/src/lib/services/session-boot.svelte.ts` holds that decision together with its offer. A 2 s timer
*offers* the mirror (`hasLocalCache`) when a request has not answered and the mirror holds projects; the
answer that arrives afterwards retracts the offer (`hasLocalCache = false`) and the copy says the wait is
slow, not that the server is unreachable. Both boot paths close the stream they found before opening the
next, because only one `/api/stream` is open at a time (`closeAppStream()` in `App.svelte`): the field
holding it is a single reference, so a second `createAppStream` leaves the first connected, still firing
handlers, and unreachable to close — the exact shape the boot race took when the user clicked Offline Mode
and the session then answered.

**Tests:** e2e `boot-slow-session.spec.ts` (the offer comes down by itself; one live stream afterwards) and
`src/lib/services/__tests__/session-boot.unit.svelte.ts` (the offer waits 2 s and needs a mirror; the login
click and the offline click each open one stream).

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

**Text is the second key, because a finished answer has no id the client knows.** A text answer's row id is
`assistant_message_id`, and it reaches the client on `complete` — which the executor sends at turn end and
not per call. So a live entry for a call whose row the read just brought has nothing to match, and `isRowCopy`
compares its content against every assistant row the read brought — equal to one, or a prefix of one. A match
means the row's own entry stands. Only the newest entry on screen escapes that test, and only while it
streams: that one is the turn in flight, and two calls in one conversation do open with the same words.

The merge replaces the entry list, so anything holding a reference to an entry it dropped is left pointing at
an object off screen. `currentAssistantId` is the one that writes: it names where the next `token` goes.
`settleStreamingRef` (`src/lib/services/stream-state.svelte.ts`) clears the ref unless an entry on screen still
streams under that id, and `TaskChat` calls it after every merge beside `reconcileActiveToolCalls`. A ref kept
on a dropped entry takes the next turn's text into a copy nobody sees.

Being in progress is what separates a pending `tool_call` row that is work from one that is a leftover:
a stopped task will never write the missing result, so that card is hidden while the message around it —
its text, its thinking, the cards that did finish — stays. Where the running flag comes from is its own rule
(*The row decides, not the board's copy*); this section is only about what the merge does with it.

**Tests:** `backend/stream/app_stream_test.go` for the partition and the claim, and
`backend/exec/durable_replay_test.go` for the seam — a real turn, a subscriber arriving after the result
row, and no finished iteration in what it is sent. `DurableThrough` keeps a running call and the newest
state; a client with `no_rows` is owed what a trim would drop. Removing either executor call site turns one
of them red. `src/lib/utils/__tests__/chat-history-merge.unit.ts` holds the copy rule (a copy below something
newer on screen goes; the newest streaming entry stays; an announced call outranks the text), and
`src/lib/services/__tests__/stream-state.unit.svelte.ts` holds the ref. E2e `return-after-review.spec.ts`
drives the whole path: a frozen tab, a run that ends in Review, and a transcript holding nothing the server
holds no row for.

## A read that did not answer says so

`getTaskMessages` answers from this device's mirror when the network call fails (`api/tasks.ts`).
`loadHistory` ignores everything its read throws except a 404, so the transcript can be the last window
this browser saved, or nothing at all — indistinguishable from a task with no conversation.
`noticeForWindowRead` (`$lib/utils/chat-window.ts`) is the one decision that tells them apart: `'cached'`
when the window carried `isFromCache`, `'owing'` when the read failed on a running task this chat holds the
key for, `'failed'` for anything else. `ChatEntries` renders the sentence above the list with **Retry beside
it** — the way to ask again without leaving the view. The `'owing'` sentence is also what declares the claim
described in *The reload meets the replay buffer*, which is the difference between a chat that fills in as
the turn continues and one that stays empty. The screen's own state is never the reason to ask again: a
return from a window that was not being read revalidates whatever the transcript holds (*The tab that comes
back*). A live read clears the notice and the claim, so neither outlives its answer, and a mirror copy
carries no `revision`, so a fallback cannot answer for the server on the next return.

**A copy this browser holds of its own is not a reading of the server, and it paints a screen that holds
nothing.** `windowIsReading` (`$lib/utils/chat-window.ts`) is that gate, and it has three doors.

- **a read that did not answer** paints the stored window onto a screen holding no rows, and nothing else —
  the open that never reached the server. The stored window is this browser's last answered read, so it is
  missing every turn and card that arrived since. Redrawing from one at a return is how a transcript goes
  backwards.
- **a read still out** paints that stored window onto that same empty screen, and only a window ending on a
  Review result may be drawn. `endsOnReviewResult` (`$lib/utils/chat-entries.ts`) says which ones that is.
  Nothing in such a conversation can still move, so showing it a round trip early loses nothing. Any other
  newest row is a send nothing answered, or a call with no result: those stay in the cache until the read
  lands. `readCachedWindow` (`$lib/api/tasks.ts`) is the read with no network attempt behind it. Whether the
  task is running is the row's `execution_state` or the digest's, never the shape of a drawn window.
- **neither** paints over rows the screen already holds. Only an answered read moves that boundary, because
  only an answered read tells the server this screen can be handed rows.

Both paints came from the cache, so both keep the `no_rows` claim (*The reload meets the replay buffer*). An
answered read drops it. A mirror copy carries no `revision`, so a fallback cannot answer for the server on
the next return.

What the reader is **told** is the other half, and the two paints do not say the same thing. A fault gets the
band with Retry. A read still out gets no band at all, because the ordinary good case must not wear the
vocabulary of a fault. `noticeForWindowRead` answers `'pending'`, and `ChatEntries` draws it as a spinner
floating over the transcript. It is absolute and out of the scroll region's flow, and only its opacity
animates, so no row moves when it appears or goes. It floats at the opposite corner from the jump-to-latest
button. A pending indicator may become a fault notice; it must never silently become nothing.

It is not a chat entry: a read that lands rebuilds the transcript, so a synthetic entry would be appended
again by each rebuild. What the queue holds is rebuilt too — see *A pending outgoing message is
the queue's projection* — so a read that failed does not hide a message this browser wrote.

**Tests:** e2e `away-and-back.spec.ts` — a read aborted from the first open with no cached copy,
retried once the server answers again, and a reload onto the stored copy. E2e
`provisional-review-paint.spec.ts` — the review card on screen while the newest-window read is held, the
`no_rows` claim on the wire beside it, and the answered window replacing the cached rows without drawing one
twice. `chat-window.unit.ts` holds the notice decision itself: a mirror read never reports `'owing'`, a
pending paint reports no fault, and `replayBufferOwed` keeps its claim. `chat-entries.unit.ts` holds
`endsOnReviewResult`, including the row that arrives after the card. `windowIsReading` holds that neither
cached paint replaces rows the screen holds. `chat-entries-history-notice.unit.svelte.ts` renders which
sentence lands for which notice, the Retry beside it, and the spinner in place of a band.

## The tab that comes back

`createTabReturn` (`$lib/composables/use-tab-return.ts`) is the one listener set for "this window has come
back", and `needsReturnReconcile` (`$lib/utils/tab-return.ts`) is the one rule. Four signals answer for one
gesture — `visibilitychange`→visible, the window's own `focus`, `pageshow` from the back/forward cache, and
`resume` from a page that stopped running — and the rule collapses them to a single call, never twice inside
`RETURN_DEDUPE_MS`.

| view | what says it could be behind | what it refetches |
|---|---|---|
| board (`App.svelte`) | always — a stale list looks current whatever the socket did | `reconnectNow()`, then `refreshBoard()`, retried once after a second |
| chat (`TaskChat.svelte`) | nothing on the screen: the sync digest answers | the task row when the row moved, then the newest message window |
| diff (`CardDetail.svelte`) | nothing on the screen: the sync digest answers | `refreshManifest()`, or `loadDiff()` when the base moved |
| terminal (`TerminalPanel.svelte`) | its own socket: `isTerminalStale` | a new terminal connection, when the old one stopped answering |

**A view that has stopped being read cannot report that it is behind, so it asks the server.** One request
answers for the task view: `GET /api/projects/{id}/tasks/{id}/sync` (`TaskHandler.HandleSync`) returns the
task-row fields, the conversation as `{epoch, revision, messages, thread}`, and the diff as
`{known, hash, staged}`. `$lib/utils/task-sync.ts` compares that against what each view *applied* and says
which part to bring back; `resyncPlan` is pure, so the rule is reachable from a test. Two facts make this the
shape rather than a poll:

- **A response-body ETag is a claim about the server, and a stale screen matches it perfectly.** The messages
  window's `If-None-Match` answers "the server is where this browser last downloaded it". Three screens fit
  that answer while sitting behind: one where a read threw after the baseline moved, one whose frames
  `suppressEvents` refused, and one that discarded a call card (*A discarded card releases its id*). Such a
  screen is answered `304` forever — a transcript left behind with nothing to ask about it. So
  `appliedRevision` (`$lib/utils/chat-window.ts`) moves the baseline only with a read that was applied,
  `createStreamHandler` reports a discarded card through `onDiverged`, and a transcript that knows it
  diverged reads without a baseline. Retry asks unconditionally for the same reason.
- **The revision is a count, not a version.** `AppStreamManager.ConversationRevision` moves at
  `DurableThrough` — the seam the executor already calls where a row lands — and where rows are removed. It
  restarts with the process, which is why the digest carries `epoch` too. Every reading the client cannot
  match (a restarted server, a re-created task, a digest it could not fetch) counts as behind: that direction
  costs a re-read, where the other one leaves a screen that lies.

The digest carries `Cache-Control: no-cache` and the middleware's ETag, so an unchanged view costs no body, and
`fetchTaskSync` (`$lib/services/task-sync.ts`) shares one in-flight call between the views answering the same
gesture. An unknown diff in the digest (`known: false`) is an unknown and not a clean answer: the panel is
left alone, because nothing was computed to compare against.

**A returning view keeps what the reader was doing.** `DiffCache.refreshManifest` reconciles the manifest —
new files, changed files, staged flags — and keeps the open file, the checkpoint overlay and the from/to
comparison. A full reload closes all three, which is why the return path asks for the cheaper one. The chat pins
only while the reader follows: a window that joins the rows held leaves them where they were, and one that
cannot be joined (a thread switch, a truncation) is a different conversation and opens at its end. The rows it
replaces take their entries with them, because a merge can only append what a read does not name, and an answer
appended below the newest rows is read as the newest content. `entriesWithoutRows`
(`$lib/utils/chat-entries.ts`) is what survives such a read: the run at the **end** of the transcript that no
stored row holds. Named: the turn streaming now, a call with no result yet, a send the server has not accepted,
and a notice this browser wrote that no row will ever carry. The bound is the end of that run, not its start. A
card left awaiting its result by a dropped socket opens a run reaching back over every turn finished after it,
and the merge appends that lot under the newest answers — which reads as turns out of order. A screen's first
read is such a replacement too, since it joins nothing because nothing was held. The answers above the new
window come back by paging, as they do whenever the window is too short for the conversation. A changed base is the exception that resets —
`diffHeadRef`/`diffBaseMoved` (`$lib/utils/diff-base.ts`) is the panel's own rule for what its diff is computed
against, and two bases cannot share one view. Not kept: an untracked file's preview, which is fetched on demand
and comes back fetched rather than pinned.

**Tests:** `sync-return-freshness.spec.ts` — a change made while the window was elsewhere arrives on return,
and the open file survives. `focus-return-review.spec.ts` — the transcript matches the stored rows after a run
missed whole, and the *next* return is answered by the ETag. `return-transcript-order.spec.ts` — a conversation
too long for one window comes back as the stored rows, in order, with nothing older beneath them.
`task-sync.unit.ts`, `entriesWithoutRows` in `chat-history-merge.unit.ts`, and
`chat-order-after-replace.unit.ts` — the tail a dropped socket leaves does not draw under the answers the read
brought, and the turn streaming now still does. `diff-base.unit.ts` and the
`appliedRevision` cases in `chat-window.unit.ts`. Go: `handler_task_sync_test.go`, and the
`ConversationRevision` tests in `app_stream_test.go`.

**A page that stopped running reports itself off its own clock.** `watchSuspension`
(`$lib/utils/page-suspension.ts`) keeps one 5 s interval for the whole page. A tick later than its pace by
`SUSPENDED_GAP_MS` is a page that did not run, because a running page fires within milliseconds of what it
asked for. Chrome's `resume` event feeds the same source as a courtesy, not as the mechanism: a phone
behind another app and a page thawed by iOS report nothing. Neither stream reading carries this case, and the
burst is why. Frames queued behind the freeze reach JavaScript after the window has focus, so
`msSinceUnpainted` stays unstamped, and the same burst stamps the liveness clock — which is also why no
reconnect banner appears. `resume` is a return like any other for the two views that ask the server, and
nothing for the board: the board's list is mirrored by the frames that move it, and a thawed page receives
the burst it missed. What no clock claims either: a page that kept running, lost frames and raised no event
at either edge has no evidence of its own. That is the case the digest exists for — those views ask the server
instead of reading themselves.

**A frame that arrived to an unfocused window is the evidence for a window that never hid.** For a view that
answers for itself, a `focus` or
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
cleared counter: the watchers share one stream, and a reading one of them consumed would leave the others
with nothing to ask about. Silence stays in the rule because it answers the case content cannot — a
socket that received nothing because it was gone.

Only the board still decides on this evidence, and it decides to refresh. The chat and the diff ask the
digest, which speaks about the server and the rows rather than about what this window happened to stamp.

`msSinceFrame()` deliberately stays a liveness clock. Had the gate been switched to a content clock, the
board — whose `mayBeBehind()` is constant `true` — would refetch on every window switch of an idle app.

**The newest window's ETag is the conversation's revision, and a stored `tool_result` is why it has to be.**
`getTaskMessages` reports the newest window's strong ETag as its `revision`, and `TaskChat` keeps the one its
last newest-window read answered with. A `tool_result` lands in the `tool_call` row that already exists, so no
row's `created_at` moves: comparing newest timestamps says the conversation is where it was while the finished
card and the answer beside it are both new. The window's own bytes move when either lands, because
`CompressMiddleware` hashes the body it is about to send.

**A return that changes nothing draws nothing.** `newestWindowUnchanged` is that one decision. A conditional
answer carries no body, so an unchanged revision must never reach `applyWindowRead`: merging it is a transcript
emptied to nothing. The reader's scroll position, text selection and follow state survive, because no entry
list is replaced. A changed revision goes through the ordinary join (*The read answers the newest answers; the
screen may be holding more than that*),
and re-pins only while the reader was following. The revision is dropped wherever the transcript it described
is dropped — a thread switch, a destroyed screen, a reconnect whose catch-up must not be bounded by a header
from before the outage — and a mirror copy never carries one (*A read that did not answer says so*). A read
that comes back to a screen which switched thread, went away, or was overtaken by a newer read is dropped
rather than painted; `isStaleHistoryRead` is that gate.

`reconcileAfterAbsence` then decides work-vs-leftover from the row it just read — `getTask().execution_state`
through `taskIsInFlight` — and passes that into `loadHistory`, never the board copy, and records the reading
so the next rebuild starts from it too (*The row decides, not the board's copy*). A row read that did not
answer keeps the reading of the last row this screen read — `knownExecutionState`, not `taskExecutionState`.
A window that has been unread is the window whose board copy missed its frames, and that reading gates
`dropStaleStreamingState`, which deletes the running turn's entries. The rebuild narrows `unresolvedToolIds`
to the calls still waiting **only when the rows came from the server**. A mirror copy settles the id of a
card that was lost; the next return still asks the server, because what this screen holds does not decide
whether to ask.
The merge drops live
pending cards when told the task is not in flight, and the board can carry an `idle` from a
duplicate-message answer (`backend/exec/handler_http.go`) while the call is still running.

A turn's first frames are not part of this story: `suppressEvents` is a rule about the task this screen
believes is running — the column plus `isLoading`, whose `execution_state` comes from the row (*The row
decides, not the board's copy*) — and it lags the turn it gates. The `status: running` frame is not gated by
that flag and so opens the gate for the turn it announces. `scheduleScroll` likewise does nothing while the tab is hidden (rAF is
paused there; the follow tracker re-pins on return).

**The read answers the newest answers; the screen may be holding more than that**. A reader who paged up
holds rows above the window the wake-up read returns, and the transcript draws a read ahead of every entry
the read does not name. A read that replaces the rows held therefore moves the newest answers above the older
pages and leaves an old row at the bottom, where the jump-to-latest button then lands. `applyWindowRead` joins
the window onto the pages held (`docs/api-contract.md → The conversation is a window, not the thread`), so the
reconcile lands the newest content below the reader instead of above them. The reader's own row needs nothing: scroll anchoring
holds it, which is exactly why a replaced page is invisible — nothing on screen looks wrong, only what is
missing is.

**Tests:** e2e `tab-return-chat.spec.ts` swallows `tool_result` task events at the socket (a listener added
in the wrapped constructor runs before the app's `onmessage`, so `stopImmediatePropagation` is where the
frame can be removed) and asserts they really were swallowed; the turn ends with the card gone, and one
`visibilitychange` must bring it back holding the stored output. `tab-return.unit.ts` holds the rule itself: a
view that revalidates reconciles on every visible signal, the other two views answer a `resume` with nothing,
and an unfocused return needs its own evidence. The ETag rule is held in both directions by the e2e
`focus-return-review.spec.ts`: a mounted chat whose only return signal is the window's own `focus`, after an
absence in which the keepalive was the one frame the page ran. The changed window is painted, by a read that
carried `If-None-Match`; the next return over an unmoved conversation sends the header, paints nothing, and
leaves the entries and their order alone — a conditional answer has no body to draw. `offline-cache.unit.ts`
holds the header's two directions (the newest window's revision is reported and re-sent; a mirror copy is
never offered as one) and the direct `304` whose body does not exist, and `chat-window.unit.ts` the two
decisions the reconcile reads — `newestWindowUnchanged` and `isStaleHistoryRead`.
`page-suspension.unit.ts` holds the clock reading: an
on-time tick says nothing, one late tick says it once, and one interval serves every watcher. The e2e
`away-window-chat.spec.ts` separates the four ways one missed turn is returned to, and counts the reads each
cause produces. A frozen page with no event is the return only the clock answers, and a `threads` read beside
the row would name a remount instead. `stream-unpainted.unit.ts` holds the reading: the keepalive never marks content unread, neither
does a focused window's traffic, and the newest unread frame is the one counted. `chat-window.unit.ts` holds
the half the reconcile reads — that only a row read that reached the server redraws a transcript. E2E
`tab-return-scroll.spec.ts` holds the join over a real transcript: a reader who paged up, left the tab and came
back keeps the newest message at the bottom of the list and their own row under the viewport.

## The board on screen belongs to one project

`appStore.board` is the app's one task list, and it is the board of the project named in the URL: the
board, the task view and every panel read that one value (`appStore.tasks` is its own list, not a copy of
it). Three things can hand it a task that belongs to another project, and each is answered where it arrives:

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
plain markup in `App.svelte`, whose `sr-only` region carries the transitions only (down, back) — one
sentence per change instead of ~29 a minute. Both clocks are `reconnect-banner.svelte.ts`'s, and both stop
when the page is destroyed.

**Tests:** `src/lib/utils/__tests__/format-time.unit.ts` (unknown ≠ just now); e2e
`ws-stale-reconnect.spec.ts` (a drop announces one sentence with no digits in it, and the way back is
announced); `src/lib/services/__tests__/reconnect-banner.unit.svelte.ts` (the age keeps counting while
disconnected and stops with the socket; the countdown follows the scheduled attempt).

**Tests:** `src/lib/utils/__tests__/task-utils.unit.ts` (a frame of another project never appends) and
`src/lib/api/__tests__/queue-board-scope.unit.ts` (a queued action of another project never replays).

## The mirror banner carries its own way back

`servingFromCache` and `wsConnected` are both true whenever a read answered from the mirror while the
socket stayed up — `selectProject`'s fallback after a failed board fetch, and any board carrying
`isFromCache`. The "Reconnecting…" banner is off in that state, so the cache banner's own Connect button
is the only route back: `reconnectBanner.retryLive()` skips the socket's backoff and runs `silentRefresh()`,
which puts live data on screen or leaves the banner exactly where it was. The button is hidden while the
socket is down, because the reconnecting banner already offers it. The button itself is held by
`src/lib/services/__tests__/reconnect-banner.unit.svelte.ts`; no spec covers it.

## A pending outgoing message is the queue's projection

A message queued while offline has no row: nothing reached `finalizeTurn`, and the mirrored window holds rows
the server answered. So the transcript derives its pending rows from `offline-queue` on every rebuild
(`pendingSendEntries`, `$lib/utils/pending-sends.ts`) — the clock and the one notice beside them. Deriving
rather than remembering keeps the mirror's claim true and survives a reload.

The ReviewBar's *Resume* beside those rows answers two questions, and `offersResume`
(`src/lib/utils/review-bar.ts`) asks both: is the last non-error entry a question, and is nobody already on
their way to answer it. A send on the wire holds the button off, and so does a "New Thread" click whose fork
has landed but whose send has not gone out. A send the queue is holding does not: nothing is in flight
there, and Resume is the recovery. A row is named for the queue action, so a rebuild draws the same row
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
refused, the order, and the skip. `src/lib/utils/__tests__/review-bar.unit.ts` holds the four answers
`offersResume` gives: on the wire, in handoff, queued, and none of the above.
`src/lib/api/__tests__/queue-refused-send.unit.ts` holds the verdict outliving the queue entry, and a fresh
send retiring it. E2E `offline-send-queue.spec.ts` shows the pending
message after a reload while still offline, and the applied row reaching the screen.

## The behaviour, in full

`onConnected(true)` in `reconnect-banner.svelte.ts` runs the app's `silentRefresh()` once
`initialLoadComplete`; `TaskChat` reloads history from the DB on reconnect because **turns completed while offline are never replayed** (buffer cleared at turn end). `offline` queues writes immediately, except a value that carries a secret — an unmasked provider key, or a clone URL with credentials in it — which is refused because the queue is persisted; `online` flushes the queue and calls `reconnectNow()` to skip backoff; `visibilitychange` refreshes the board (phone-sleep), and the open task's chat and diff answer the same signal together with the window's own `focus` and a bfcache `pageshow` — the chat whatever it believes, because a missed turn leaves no local trace, the diff only while the task looks able to move under it, never on the task's status. Project re-subscription forces a re-send of `task_diff_stats`.

The queue's own shape is stated where it is held: one record per browser profile, changed by
read-modify-write and replayed by the newest window alone (*One window replays the queue, and one record
holds it*). The three writes that cannot land twice carry the identity they sent first, so a replay is
answered rather than executed again (*One identity per write, so a replay cannot land twice*). Every other
queued write is at-least-once, deliberately.

### How a request ends: cancelled, or unanswered

`apiFetch` (`src/lib/api/helpers.ts`) is the one place that decides what a failed fetch means. Every request
that can be queued, or must report an outage, goes through it, and `npm run check:fetch` fails on a `fetch(`
outside `src/lib/api/`; the one file it exempts is reasoned in that script's own list. What still opens its
own response inside that directory is reasoned at its definition: the CSRF bootstrap, and the four progress
streams that cost the caller a line rather than the HTTP reason (`streamCommitPreview` in `api/tasks.ts`).

| outcome | thrown | the HTTP reason | what the user sees |
|---|---|---|---|
| caller aborted (unmount, navigate away, Stop/Cancel) | `RequestAborted` | untouched | nothing — they asked for this |
| connection failed | `ConnectionFailure` | **up** | "MIKE did not answer — check the server is still running", and the queue takes over |
| answered, whatever the status | the `Response` | **down** | the queue goes out |

Before this, both rows were one row: any `AbortError` counted as offline. Since `apiFetch` attached its own
15 s `AbortController` to every call that had no caller signal, that meant a big task diff, a merge on
commit-and-done, or a clone replayed from the queue **failed in the browser at 15 s while the server was
still doing the work**, and then every later write went to IndexedDB because of it. Cancelling a commit
dialog had the same effect on purpose-free terms.

**Two reasons, one flag, and each reason answers to its own evidence.** `getOfflineStatus()` is the union of
two reasons. One is the socket's: the edge in `reconnect-banner.svelte.ts`, the browser's own
`online`/`offline` report, and the boot probe. The other is the HTTP layer's, in the table above, and
`apiFetch` is its only writer. An answered request says nothing about a socket that is gone, and a socket
that is up says nothing about a server that stopped answering HTTP, so neither writes the other's reason;
the queue goes out when neither is left. A single unanswered request therefore cannot hold the queue once
something answers again — which matters because the banner reads the socket, and a queued message beside a
healthy banner is a report nobody can act on. A write that believes itself offline spends no request, so
**the reads are the probe**: `getBoard`, `getTaskMessages` and `getConfig` go to the network whatever the
queue believes, and the first one that answers releases it. No timer polls for that evidence.

**A replay paces itself, not the app.** A queued action whose own send failed has said something narrower
than "the server is down": this request did not land. Retrying it on the next answered read is a loop,
because every replay end provokes a read and every read answers — measured, one held action drew a send
attempt per read until it was bounded. So the queue waits `QUEUE_RETRY_BASE_DELAY_MS` after its own failure,
doubles that to `RECONNECT_MAX_DELAY_MS`, and resets it when a replay drains the queue. The next answered
read after the pause tries again; nothing waits on a timer the queue owns.

**No client-side deadline replaced it.** The plan's rule is that the queue gets bounded, not the work. The
case the timeout existed for — a server that accepts a connection and never answers — is already surfaced
honestly: the WebSocket drops, and `setOfflineStatus(!connected)` in `reconnect-banner.svelte.ts` puts up
the socket's reason, which holds the queue whether or not HTTP still works. Stalls on the server are reported where they are known (`subproc.Watch` for podman, the lock
slow-wait reports), not guessed at from the client.

Both classes keep the DOM `name` (`AbortError`, `TypeError`) rather than inventing one, because a check that
misses a cancellation fails silently in one of two directions: a cancellation starts toasting, or a queueable
write starts failing. **The name is the test; `instanceof DOMException` is not** — these are `Error`s that
carry the name, which is why `isAborted(e)` is a function rather than a class check. One call site
(`diff-cache`'s fetch of a single file's diff) tested the instance, so the moment the throw became an `Error`
a cancelled read stopped being silent: a check written against the throwing type, not the contract.

`isTransientError` explicitly excludes a cancellation, so a send the user stopped is not replayed from
storage after they left. The same split holds at the one other call site that saw a failed write:
`CreateTaskModal` queues the card and announces it, and writes no reason at all, because the request that
failed already reported its own.

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
and "a cancellation does not call itself an outage"), `src/lib/api/__tests__/send-queue.unit.ts`, and
`src/lib/api/__tests__/offline-reasons.unit.ts` for the two reasons — a queued write stays queued until an
answer releases it, an answered read does not release the socket's reason, and a replay that failed on the
network is retried at the pace and not per read. The server side is held by
`backend/errs/errs_test.go` (an operation must not inherit its cause's
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

## A write nobody answered is kept by the write itself

**A write that got no answer is kept by the write itself.** The rule has one home, `writeOrQueue` in
`api/queue.ts`: ask the queue, then send, and if nobody answered, enqueue the same action and hand back the
optimistic row. The failure raises the HTTP reason as it happens, so the queue is already open by the time the
catch runs and the replay takes the action on its own. A refusal of any status is an answer and reaches the
caller, because a write the server refused must not come back out of IndexedDB behind the person's back. A
cancellation is not a failure at all, and `isTransientError` is what says so.

Before that, every write asked `getOfflineStatus()` before setting out. So the edit made between the connection
dying and the app noticing went out live, failed, and was lost. The queue existed one moment later — too late
for the write that raised it.

A create is the exception, and the id is why. The card on screen and the action in the queue must name one task.
So `createTask` keeps its own branch, and `CreateTaskModal` — which drew the card and knows its id — sorts a
queueable failure from a refusal it must not queue.

Tests: `api/__tests__/write-or-queue.unit.ts` (answered, unanswered, refused, cancelled, already-offline);
e2e `one-task-list.spec.ts` drives the edit that is queued by a request nobody answered and lands once.

## One window replays the queue, and one record holds it

`offline-queue` is one record per browser profile, not per window, so every window open on it holds the same
queued writes. Two rules keep that shared record from becoming two of every write.

**A change is stated against the record, never as an array.** `updateQueue` (`$lib/storage.ts`) reads the
record, applies the change and writes it back inside one `readwrite` transaction, and the caller says what to
do to what the record holds *now*. Dumping one window's in-memory array over it erases what another window
queued in the meantime. An applied or refused action leaves by its `id`, never by position: between the send
and that write the record may have grown, and the head is not necessarily this action any more. With no
IndexedDB there is no record to read, so the caller's own array stands in and the session keeps its queue for
as long as it lives.

**The newest window owns the replay.** A `queue-owner` record beside the actions names the window that sends
them, mutated by the same read-modify-write. Boot takes the replay from whoever holds it (`seizeQueue`), every
flush asks the record first (`claimQueue`), and the owner hands it back on a `pagehide` that is not the
back-forward cache (`releaseQueue`). The rules are pure in `$lib/utils/queue-ownership.ts`: `shouldReplay`
answers the cheap question — work waiting, no reason holding it, its retry pace spent — on every reachability
flip, and `holdsReplay` answers the separate one of whether the record names this window. A record that does
not exist is answered as ours: with nowhere to store a claim, this window's queue is the only queue.

**A window that does not send still answers from the record.** It queues its own writes, and
`getQueuedSends`, `getQueuedTaskIds` and `getDroppedTaskIds` describe what the record holds, so its pending
rows and queued badges match the owner's. `BroadcastChannel('mike-queue')` carries `changed`, `claimed` and
`released`, and a reader re-reads on any of them. A burst of notices costs one re-read rather than one per
notice: a notice landing during a read asks for a second pass, so the last notice is never answered by a read
that began before it. `adoptQueue()` is that re-read for a window the browser
froze and never saw the notices, called from the tab-return watcher in `App.svelte`.

Two windows booting together both write the owner record, and the read-back leaves one of them the reader; until
the notice lands, at most one action can go out twice. That overlap is what the write identity below is for.

**Tests:** `src/lib/utils/__tests__/queue-ownership.unit.ts` holds the two rules.
`src/lib/api/__tests__/queue-two-windows.unit.ts` covers one send for a write two windows hold, a reader that
still names the pending send, the handover when the owner closes, a reader that answers a burst of notices with
few re-reads, and a window with nowhere to store its claim.
`src/lib/api/__tests__/queue-record-merge.unit.ts` holds the read-modify-write: an action queued in another
window survives, and a record that grew mid-send is replayed in order rather than overwritten.

## One identity per write, so a replay cannot land twice

The queue replays what it holds, and replaying a write whose answer was lost reaches the server with the first
attempt already applied. Three writes do not survive that: a send starts a turn, a create opens a card, a fork
rewinds a worktree. They carry an identity the client minted.

**The token is minted before the write sets out.** `writeOrQueue` mints one `clientToken` and hands the same
string to the live call and to the queued action, so the attempt and every replay of it are one write as far as
the server can tell. A refusal is answered and thrown, so the next attempt is a different write and mints a
fresh one. `createTask` and `forkTaskThread` keep their own branch and pass the id they already fixed for the
optimistic row. Only these three put `client_token` on the wire (`api/raw/tasks.ts`, `api/raw/threads.ts`);
the rest carry it in the record and their route never reads it.

**A claim records what the write produced.** `client_claims` (`backend/store/claims.go`) maps a token to the row
it answered — the message, the task or the thread. Each guarded handler asks before it changes anything. That is
what lets a replayed edit-and-send escape the refusal its own first arrival caused: the row it names to truncate
stopped being the thread's last user row the moment the send landed. What a repeat is answered with, the scope
the answer is confined to (a token is text from the request), and what a deleted answer row costs belong to the
server's contract, in `docs/api-contract.md` → *A replayed write carries the identity it sent first*.

**A claim is not forever.** The row ages out after `claimRetention` (30 days), pruned by `ClaimWrite` — the only
writer — through `idx_client_claims_created_at`, so no sweep or boot step is owed; a deleted task drops its
claims through the foreign key. A queue older than the horizon is then executed as a fresh write: the guard is
gone, nothing else is.

One thing the client leans on: a claim answers a replay, never a race — two arrivals carrying one token that
land together both find nothing claimed. The queue replays only after its own attempt ended, and from one
window, so the race is not the case that bites here.

**Every other queued write is at-least-once, by decision.** A move, a rename, a status change and a
`PUT /api/config` land the same value again and that is all; a clone stays last-writer-wins or refused. One
consequence is written down rather than fixed: a replayed `deleteTask` whose first arrival succeeded comes back
refused, and the refusal is reported as a refusal, not a delivery.

**Tests:** `src/lib/api/__tests__/write-identity.unit.ts` holds the replay sending the token it already sent, a
refusal and a next write each minting their own, and a create holding its fixed id in the record it leaves
behind. `backend/store/claims_test.go` holds one token claimed once, another token as its own write, the task's
deletion dropping its claim, a claim answering only the task or project it was written in, and a claim past the
horizon stopping its answer. The prune's index is the one read `backend/store/schema_test.go` walks.
`backend/server/client_claim_test.go` holds a replayed send, create and fork leaving one row each, two
identities carrying the same words still running twice, and one task's identity answering nothing for another.

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

**Displaying one strips the class name off.** The same `name` that lets the queue recognise a lost
connection is what `String(e)` prints in front of the sentence, so a person reads
`TypeError: Connection issue...` where the fact was the rest of the line. `errorText` in
`$lib/utils/error-text.ts` is the one way these failures reach the screen: the `message`, or the caller's
own sentence when the failure carried none. Its result is never handed to `t()` — a message is a wire value
or another pack's sentence, which is the rule `en.ts` states beside the catalog's fallback texts. Test:
`utils/__tests__/error-text.unit.ts`.

## A file save the connection lost waits with the editor, not in the queue

A file write never joins `offline-queue`, and that is a decision rather than a gap. The queue is replayed
from IndexedDB, possibly after a reload, and a file body is a snapshot of a buffer someone may have edited
since. A replay would write older bytes over newer ones and report success while doing it.

So the editor holds the wait, and `FileEditor.handleSave` sorts its own failure with `isTransientError`:

- **no answer** — nothing is said and nothing on screen changes: the buffer, its draft and the tab's
  unsaved marker stay, and the Save button stays enabled because it is gated on `hasChanges`. The editor
  runs the save again after `saveRetryDelayMs(attempt)` — 2 s, doubling to 15 s. The socket's false→true
  edge cuts that wait short and runs the save at once, and only an edge that **follows** the arming: the
  `$effect` watches `connectionStore.connected`, not the timer, so a blip that never dropped the socket
  runs its own delay.
- **answered and refused** — never retried: a later attempt asks the same question and gets the same
  answer. This is the one save failure said out loud, once, as a toast carrying the server's sentence, a
  permanent fault being the one thing silence cannot describe.

**The retry runs the save; it never replays a captured body.** Reading `content` when the attempt fires is
what makes the newest edit win. Two existing guards keep the wait to one write per rung: one timer per
editor, and `SaveController` turning any request made during a write into one coalesced follow-up. The wait
ends when a write lands, when the buffer stops differing from the file — `reconcileExternalChange` reconciled
it — and when the editor is switched or unmounted.

**The two throttled writes an edit arms carry the file they were armed for.** An edit arms a draft save at
200 ms and an undo-chain save at 500 ms. The diff editor hands one view instance from file to file, so a wait
can outlive its file. Both are kept as a closure over the path and the document read **at the edit**, never
at the timer. `destroyView()` delivers an outstanding wait to the file it was armed for rather than dropping
it. Dropping loses the last keystrokes before the switch; letting it fire writes the arriving file with the
leaving file's bytes — including the empty buffer a reload starts from.

**The draft is the buffer, and the undo chain rides along only when it agrees.** A reopened file paints the
draft, because that is both what the reader left and what a save writes. The persisted chain is kept only when
`replayDeltas(base, deltas)` (`$lib/utils/editor-history.ts`) reaches that same text; the draft is written
sooner than the chain, so a chain landing anywhere else is the older reading, and painting it would show one
document and save another.

**A save failure never writes `error`.** That state renders the *read*-failure pane, and the template's
`{:else if error}` replaces `.editor-wrapper` with its text, so a save that reports itself that way deletes
the document the user was editing along with the reason. Say it in the toast, or say nothing.

**Tests:** `src/lib/components/__tests__/editor-save-failure.unit.svelte.ts` holds the surviving document,
the silence during a blip, the attempt gaps of 2/4/8/15 s, and one write per rung.
`src/lib/utils/__tests__/save-state.unit.ts` holds the gap sequence itself.
`src/lib/components/__tests__/editor-buffer-persistence.unit.svelte.ts` holds the two throttled writes: the
file that arrives gets neither the leaving file's draft nor its history, and a chain that disagrees with the
draft paints and saves the draft. `src/lib/utils/__tests__/editor-history.unit.ts` holds the replay and its
refusal. E2E
`editor-save-retry.spec.ts` aborts the write in flight, checks the buffer stays on screen unannounced, and
lets the retry land it against the real server — once.

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

## Signing out empties both halves

Sign-out runs `clearQueue()` and then `clearAll()`. Both calls, in that order.

- `clearAll()` erases the record, and a change is written *against* the record rather than as one window's
  whole array, so an erased queue stays erased through the next enqueue.
- `clearQueue()` is still required, and for a different reason: a window **sends what it holds**. The flush
  walks the in-memory array, record or not, so a signed-out window that still names actions in memory would
  put them on the wire once something answers again.
- `gc()` is not the substitute: it prunes only entries whose task is gone, and it has one caller,
  `Board.svelte`, which runs it after the board loads.
- Test: `storage-clear-all.unit.ts` — "clears memory too, because an emptied record does not stop this window
  sending what it holds", which is the case `clearQueue()` answers. That is why the order is the rule rather
  than a style choice.

## Checking this

`cd frontend && npm test` covers `taskIsInFlight`, the offline queue, the mirror's coalescing and the pending
rows derived from the queue, and `stores/__tests__/app-store.unit.ts` the board helpers that are the only way
the task list moves. `go test ./backend/store/ ./backend/server/` covers the write identity: one claim
answering once, and the three guarded routes leaving one row for a replay. `cd frontend && npx playwright test reconnect-refresh ws-stale-reconnect` drives a lost
connection; `mirror-follows-frames offline-send-queue` drive the mirror and the queue across a reload;
`return-after-review` drives the return onto a run that finished while the tab held nothing. `one-task-list`
drives a board move and a queued card edit, which is where a second task list would show up as a card that
disagrees with its row.
