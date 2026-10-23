# HTTP and WebSocket contract

The rules are in `AGENTS.md → API`. This is who owns an endpoint, the shape of the frames, the route
table's exemptions, and the error vocabulary.

## Which package owns an endpoint

**Each domain package is responsible for serving its own endpoints.** `backend/api` holds the handlers for domain resources —
`TaskHandler`, `ProjectHandler`, `BoardHandler`, `GitHandler`. A new handler for a task, project, board
or git route goes there, constructed in `NewServer` and registered on the `*Server`'s mux. `backend/server`
holds the transport: the listener, TLS, session/CSRF middleware, the shutdown stage table, and the detached
base context. It therefore holds the endpoints that are *about the listener* — the `(*Server)` methods in
`handler_*.go` (`handleFS…`, `handleThread…`, `handleSessions…`), which are methods on `Server` because they
access `s.broadcastTaskDiff`, `s.lockWorktree`, `s.fsWriteTarget` and use the `s.gitTask` lock.
`handleTaskSend` is one of them for the same reason: the truncation it may perform releases the checkpoints
the discarded turns pinned and the forks keyed on their rows, which is `handler_message.go`'s responsibility.

Dependencies flow from `server → api` and never in reverse; `backend/api` imports no `backend/server`, which is why a
handler that needs the hub or a lock is *injected* with a function value instead. This one-way dependency is what
prevents certain refactors that would otherwise introduce import cycles. Folding every handler into one
package would require moving `Server` with them or creating a `server ↔ api` import cycle. Splitting
`backend/api` by domain would require a shared-helper package to solve a naming problem, because
`resolveTask`, `requireWorktreeMgr` and `resolveWorktree` (`http.go`) are shared between `task.go` and
`git.go`. The mixed `Handle*`/`handle*` naming inside `backend/api` is a reflection of this boundary: a handler is exported because `backend/server`'s route table names it; it is unexported because only
the package calls it.

## Who may call a route at all

Two credentials provide access to this server. A session cookie provides access to all endpoints protected by the authentication middleware. A bearer
token provides access to the patterns in `tokenScope` (`backend/server/token_auth.go`) and nothing else. The grant is
checked against the pattern the mux reports for the request, so a route renamed without editing that
map loses its access rather than keeping one under the old name. `docs/api.md` is the client-facing
version of that list and of what each route does; `TestTokenScopeIsTheApprovedSet` holds the code and
that list together. Nothing else in this file changes for a token caller except CSRF, which has no
cookie to protect.

The five `/api/updates/*` routes are excluded from `tokenScope` because bearer tokens are for headless clients, and no headless client should be replacing this server's binary. They are
session + CSRF routes, all five declare their verb, and `GET /api/updates/status` answers from what the last
check learned rather than making a request of its own — the row that route serves is docs/auto-update.md.
`POST /api/updates/prepare` and `POST /api/updates/apply` are the pair that changes anything, split so the
fetch can run on a detached context while the swap stays behind a click; the route that ends the process is
`POST /api/updates/restart`.

## Frames

- Inbound WebSocket frames use the `stream.AppStreamMessage` structure (`Data` is `json.RawMessage`). **Outbound** frames are
  sent as flat maps built where they're broadcast (no shared envelope struct in Go) — a `task_event` carries
  `task_id`/`event` beside `data`, so the client reads `msg.task_id`, **not** `msg.data.task_id`.
- `subscribe_task` carries `no_rows`: this connection holds no conversation rows of its own, so the task's
  replay buffer is kept whole for it. `docs/reconnection.md → The Replay Buffer` owns that
  rule and what clears the claim.
- A frame that is not a JSON object is dropped and reported once (`parseStreamFrame` in
  `api/helpers.ts`, the one reader of both sockets' `onmessage`). A `JSON.parse` throw inside `onmessage`
  is swallowed by the event loop: the frame is lost, the socket stays open, and the app believes it is
  current. Test: `api/__tests__/ws-frame.unit.ts`.
- A server **progress stream** has one client reader: `readEventStream` in `api/helpers.ts`. It splits the body
  on the blank line that terminates a frame and yields `{ event, data }`. The writers are `data: %s\n\n`
  (`backend/api/project.go`, `backend/extensions/handler.go`) and `event: %s\ndata: %s\n\n`
  (`backend/exec/merge.go`). A response with no body is the reader's error. A frame the stream stopped inside
  is dropped, because half a frame is not one. Whether an early end is a failure is the caller's decision,
  since only the caller knows which frame it needed: a `done` event, a build result, a cloned project. The
  reader applies no deadline — a stream ends when the caller's `AbortSignal` does, and that cancellation never
  reads as an outage. A component iterates typed frames from the API layer instead of reading bytes
  (`streamCommitPreview` in `api/tasks.ts`). Four streams read through it: clone progress, a sandbox image
  build, an extension install, a commit-message preview. `grep -rn getReader frontend/src` re-counts them.
  Test: `api/__tests__/event-stream.unit.ts`.
- Use optimistic UI for user messages (add immediately, skip the echo).
- Model/provider changes ride a `user_message` with optional `model_name`/`llama_server_url`.

### The task row a browser holds has no result text

`output` is the text the agent reported when its turn ended. It is the largest field a row has, and nothing in
`frontend/src` reads it: the chat shows the thread's messages, and the `token` frames carried those words
already. So a whole task row reaches a browser only through `stream.TaskForBrowser`, which clears the field —
in the `task_updated` frame and on the board route. The key still arrives, empty, so a row always has the same
shape. `GET /api/projects/{projectID}/tasks[/{taskID}]` answers in full; it is token-scoped and its reader
asked for the result (`docs/api.md`). This holds because every client write names the fields it changed
(*Partial updates decode into pointer fields*). Measured with the byte counter in `backend/stream/traffic.go`:
a row is about 750 bytes of fixed shape, and one holding a 6.3 KB result cost 55 KB over 8 broadcasts to one
tab, against 6.4 KB with the field cleared. Tests: `backend/stream/browser_row_test.go`,
`backend/server/board_row_test.go`.

### A retried LLM call names itself in a field

When the first attempt of an LLM call fails, `sendRetryHint` (`backend/exec/loop.go`) broadcasts one
`hint` frame for the whole retry sequence — never one per attempt — carrying `role: "system"`,
`kind: "retry"` and `error: "<cause>"` beside the stored row's id and sentence. The client marks the
awaiting line from `kind` and takes the reason from `error`; it never reads `content` to work out what
the frame is, because the sentence is copy and the two fields are the contract. Tests: the abort cases
in `backend/exec/llm_crash_test.go` (one stored row for three attempts), `stream-handler.unit.ts`.

The stored row is the permanent record that the provider was interrupted; the awaiting line is live
state and clears when the retried call streams (`clearAwaiting`) or when the turn ends (`complete`,
`error`). A turn that ran tools and then lost the provider leaves no partial assistant row: the dead
call never reached `finalizeTurn`, which is what persists it. So the client drops the half-streamed
text with `dropAbandonedTurn()` and the retry streams the answer in its place.

### A broken failure loop adds no frame shape

The `loop_breaker` preference (Settings → Task defaults, `docs/adding-a-preference.md`) answers the run that
reached its consecutive-failed-turn limit instead of ending it. What it sends is three frames the client
already knows, in this order: a `hint` with `role: "system"` and `kind: "loop_breaker"`, the `user_message`
echo of the `Proceed` instruction it stored as a user row, and a `status` frame reading `running`. The rows
are the record — a `system` note (display-only) and a real user row — and the frames only show them landing,
so a client that reconnects mid-break reads the same conversation. `chat.system.loopBreaker` words the note
and `loopBreakerNoteText` (`backend/exec/loop_breaker.go`) stores that sentence on the row; the kind strings
and `chat-entries.ts` must agree case for case. Tests: `backend/exec/loop_breaker_test.go`,
`chat-entries.unit.ts`.

### A tool call the user stopped says so in a field

A user message does not queue behind the tool call that is running. `toolCallContext`
(`backend/exec/interrupt.go`) cancels the context one turn's tool calls run on: a command dies with its
process group, and the calls that have not started never start. Every call of that turn answers
`Interrupted` — the killed one and each one that never ran — because the assistant row already asked
for them, and the card and the provider are both waiting on a result.

`tool_result` carries `interrupted` beside `success` because `success: false` covers a crash and a stop
alike, and the card colours them differently (green, red, yellow). The same key rides the row's
`tool_result` JSON, so a reload keeps the colour. An interrupted turn settles nothing: no failure
counted, no question awaited, no commit, merge or Done (`docs/commit-and-done.md`). The message that
stopped it is the next call's input.

A task stop is not a message. It cancels the wider context, so the call reports the command failing and
the run ends instead of looping. Tests: `TestUserMessageInterruptsRunningToolCall` and
`TestStoppingATaskIsNotAnInterrupt` (`backend/exec/interrupt_tool_call_test.go`),
`TestBuildToolResultEvent_Interrupted`, `stream-handler.unit.ts`, `chat-entries.unit.ts`.

### What an interrupting message may delete

The rows decide, and the screen is told which they are. `finalizeTurn` (`backend/exec/loop.go`) persists
the assistant row and one `tool_call` row per call before running any of them, and as each row lands a
`tool_progress` frame names it: `{"tool_id": …, "message_id": …}`. So a card carrying `messageId` is a
row while its command is still running — no answer need have arrived.

`StreamState.abandonInterruptedTurn` (`frontend/src/lib/services/stream-state.svelte.ts`) is the one place
that rule is applied, and both paths into it go through it: the chat's own send, and the
`status: cancelled` frame that tells every other screen. A streaming entry goes only when nothing in it
carries `messageId`. Drop a row's card and the `tool_result` frames that answer the stopped call resolve
nothing: the turn and its text vanish until the transcript is read from the database again.

Filter before closing the entry. Closing clears `isStreaming`, the flag that identifies the entry, so a
handler that closes first deletes neither the half-streamed text nor the card no row answers.

Tests: `stream-handler.unit.ts` (the kept card, the dropped one, the cancelled frame arriving alone) and
`TestRunningToolCallNamesItsRowWhileItRuns` (`backend/exec/interrupt_tool_call_test.go`), which reads the
wire while a command runs.

## Provider routing

`PUT /api/config` is a partial update with two required pairs:

- `worker_provider`/`worker_model` — chat; resolved frontend-side by `resolveWorker` in
  `$lib/utils/providers.ts`.
- `utility_provider`/`utility_model` — titles, summaries, commit messages, checkpoint labels;
  `utilityTarget()` in `backend/exec/executor.go`, falling back to the task's own URL/model.

## Partial updates decode into pointer fields

Never a `map[string]any` read behind type assertions. `PUT /api/projects/{projectID}/tasks/{taskID}` uses
`taskUpdateRequest` (`backend/api/task_update.go`) and `PUT /api/config` uses `configUpdates`
(`backend/config/updates.go`): absent key = leave alone, present key = write (including `""` and
`false`), JSON null = absent. A single-value assertion (`v.(string)`) panics and the recovered panic
reaches the browser as a 500; the `, ok` form silently drops the value and answers 200, so the UI believes
its own save. Wrong types are a 400 naming the field, and validation runs before anything is written.
Tests: `backend/api/task_update_test.go`, `backend/config/updates_test.go`.

A `providers` payload is judged by value as well as by shape: a sampler override outside its range
(`validateModelSampling` in `backend/config/sampling.go`) is a 400 naming the provider, the model and
the field, and the stored configuration is untouched. Test: `backend/config/sampling_test.go`.
`docs/llm-streaming.md → Sampler overrides: a blank field sends nothing` owns what each field means
on the wire.

## The acknowledged memory alert

`tasks.memory_alert_ack` is on every task payload: `null` when nobody dismissed the alert, an object
`{peak_mb, oom_kills, limit_mb}` when someone did. The object is the numbers the alert showed at the
time, which is what
decides whether it comes back (`memoryAlertAckCovers` in `$lib/utils/memory-stats.ts`; the client owns
that rule and the server stores only what it is told). It is a column on the row, so one dismissal
serves every device, and the route that writes it broadcasts the task the way any other task change
does: `POST /api/projects/{projectID}/tasks/{taskID}/memory-alert-ack`. All three fields are required —
an ack covering less than the alert dismissed would leave it standing on the device that did not click —
and each is a 400 naming itself when absent, wrongly typed, or negative.

## Prioritizing a task that waits for a slot

`POST /api/projects/{projectID}/tasks/{taskID}/prioritize` moves that task to the front of the provider
queue it is waiting in for a running-task slot, and answers `{"promoted": true|false}`. The false is the
queue's answer, not a fault: the task may already hold its slot, or wait on the memory budget, which no
click reorders. `backend/api/task.go:HandlePrioritize` reads the queue through
`exec.TaskExecutor.PrioritizeQueuedTask`, which knows a task's waiter only while that wait lasts — the
queue lives in memory, so a restart returns it to arrival order and no column records the request.
Nothing is broadcast: the queue position the task's own stream reports every second is what tells every
open tab what changed, which is also why the client offers the button only for the wait this route can
answer. Tests: `TestTaskGate_PrioritizeRunsTheQueuedTaskFirst`, `TestPromote_ServesThePromotedWaiterNext`,
`TestPrioritizeAnswersWhetherTheTaskIsQueued`, and the client's rule for which wait may ask in
`frontend/src/lib/utils/queue-priority.ts`.

## Route verbs

Every `/api` route declares its verb in the pattern (`s.mux.HandleFunc("POST /api/…")`), so the mux
answers 405 with an `Allow` header before any handler runs, and handlers do not check `r.Method` to
refuse. A new methodless `/api` route is an archgate `routes` violation, so it fails `make check` rather
than being noticed in review. A methodless route also escapes `CSRFMiddleware`, which guards non-GET only.

That skip is a client-side rule as well: `apiFetch` asks for the token only for a method the
middleware guards (`needsCsrf`), so a GET sends no `X-CSRF-Token` and issues no probe for one. It matters
because `/api/auth/session` is the request that *supplies* the token. A safe method that asked for one
would spend a round trip fetching a header the server ignores — the probe for the answer first fetching
the credential for the question. Tests: `frontend/src/lib/api/__tests__/auth.unit.ts`.

Exemptions, each with a reason: the extensions file-server subtree (`/extensions/`) and the two WebSocket
upgrades (`/api/stream`, `/api/lsp`), which upgrade on GET and never inspect the verb.
The four passkey ceremony routes are public for the reason the ceremony exists: no session exists when
they are called, so there is no cookie for CSRF to protect and nothing for a token to reach. They are the
only public `/api` paths besides `/api/health`, `GET /api/auth/session` and `GET /api/tls/ca` — the last
public for the reason `docs/tls.md → Certificates on disk` gives. Each ceremony route declares its verb and
runs behind the login rate limiter — `docs/passkeys.md` owns what the pairing code inside one of them
is and is not allowed to buy.
`handler_preview.go`'s two `r.Method != http.MethodHead` checks suppress a HEAD *body*, not a
request, and `handleTaskMessages` takes three verbs through one function.

The frontend is served **behind** the mux (`spaBehind`, `backend/server/routes.go`), never as a route in
it. That is a route-verb rule rather than a styling preference: a catch-all pattern inside a
`ServeMux` outranks that mux's own verb-mismatch answer. A `GET` on a POST-only `/api` route would
be served the application document — a 200 that reads to a caller as a route which accepted the request. Behind the
mux, an unmatched path outside `/api/` is the SPA's and everything under `/api/` keeps the mux's answer,
405 and `Allow` included. `TestSPAServesNothingToAWrongVerbOnAnAPIPath` and
`TestMuxRefusesAnUnregisteredVerbWhileTheSPAServes` pin the two directions.

A handler that switches on `r.Method` is not a methodless route and is not stale either. Nine branches
across six files (`api/task.go`, `api/project.go`, `config/config.go`, `extensions/handler.go`,
`server/handler_message.go`, `server/handler_thread.go`) end a verb switch in
`httputil.MethodNotAllowed`. Because a `GET` pattern also matches `HEAD`, it is that arm which answers
HEAD — deliberately, so `HEAD /api/config` refuses while `HEAD /api/health` answers.
`TestGETPatternAlsoAnswersHEAD` pins which side each route is on; deleting those arms would turn nine
deliberate 405s into bodiless 200s.

Two consequences: net/http matches the **most specific path first**, so `GET .../tasks/done` reaches the
`{taskID}` handler and answers 404, not 405; and a `GET` pattern also matches `HEAD`. Tests:
`backend/server/routes_method_test.go`; gate: `scripts/archgate` `routes` rule.

One action is one verb: `/api/extensions` answers `PUT {id}`, `PATCH {id}/enabled` and
`PUT {id}/dependency-cache` — the verb its caller sends, not that verb plus an alias — and
`POST .../tasks/{taskID}/resume` is registered on `executor.HandleRetry` directly, because the client's
Resume and a restart of a failed turn are the same operation.

`GET /api/preview-nav/{token}` answers the preview watchdog's question — which page the token last
served — and sits *beside* `/api/preview/` rather than inside it. Everything under that prefix is
public and answered by the token alone, so a session-authed control living there needs a carve-out in
the auth rule and reserves one name inside every previewed repository.

`apiroutes` asks the other half of the question: a registered `/api` pattern must match a path literal in
`frontend/src` or in `scripts/mike-healthcheck.sh`, so a route nobody calls fails `make check` as surely as
a route missing its verb. It is the same debt as an unheard stream event, and a route nobody reaches still
costs a verb, a CSRF arm and a lifecycle classification. A path counts as a caller through the idiom that
sends it — `` `${API_BASE}/x` ``, a bare `'/api/x'`, and the relative path handed to `streamOperation` in
`api/extensions.ts` — and test files are not callers. A route whose path no literal can spell (a
`{name...}` tail) is exempted by name, naming where the URL is written instead. Three exemptions: the two
WebSocket upgrades, whose URL is built from `window.location.origin`, and
`GET /api/preview/{token}/{path...}`, whose sub-resource URLs the server writes into the document it
serves (`rewriteRootRelative`). Gate: `scripts/archgate` `apiroutes`.

## A send may take the thread back before it lands

`POST /api/projects/{projectID}/tasks/{taskID}/send` carries one field the executor never reads:
`replace_message_id`. Set, the thread is taken back to that row first and the message lands where the deleted
row stood, so an edited message and the turns that answered the old text are one request. Unset, the route is
the executor's own send unchanged.

The named row has to be the active thread's **last user row** at the moment of the request. A newer user row
means the conversation moved somewhere this click cannot speak for, and a send flushed from the offline queue
lands long after the click that wrote it: the route answers 409 and truncates nothing. `handleTaskSend`
(`backend/server/handler_send.go`) peeks that one field and hands the body on, so every other field stays the
executor's to parse and its duplicate-send guard sees a thread ending where the message belongs.

**Tests:** `backend/server/handler_send_replace_test.go` — an edit-and-send leaves one user row and moves the
task out of Review, with and without an attachment, and a replace naming a row that is not the thread's last
user row leaves
the thread whole.

## A replayed write carries the identity it sent first

Three bodies carry a `client_token` the server acts on — `POST …/tasks/{taskID}/send`,
`POST /api/projects/{projectID}/tasks` and `POST …/tasks/{taskID}/threads/fork`. The client mints one identity
per write and sends that same one with every replay of it, so a request whose answer was lost arrives as the
same write rather than as a second one: `docs/reconnection.md → The Offline Queue`.

A token already answered is answered with what it produced, and nothing runs again. All three answers carry
201 and the body the first arrival got: the send the message row (`id`, `role`, `content`, plus `thread_id`
when the row has one), the create the task, the fork the thread. Each route asks `ClaimedWrite` or
`ClaimedCreate` (`backend/store/claims.go`) **before** it changes anything, which is what keeps a replayed
edit-and-send from being refused by the truncation its own first arrival caused. The claim is recorded after
the write lands, by `ClaimWrite`, against the task or project the request named.

Four bounds on the answer. A token is text from the request, so it is answered only inside the scope the
request named. `client_claims` is looked up with `task_id`, or `project_id` for a create, which has no task
yet; a token presented under another task claims nothing. A claimed row that has since been deleted answers
404 for a create and a fork, never a fresh write: recreating the row would undo that deletion. A send whose row
is gone (a later edit truncated it) is written again as the new message it now is. And an empty token claims
nothing, so a body that sends none keeps the behaviour it had, the send route's own last-row dedupe among them.
And a claim row is dropped when its task is deleted or once it passes `claimRetention`, so an identity older than
that runs again rather than being answered.

Every other route ignores the field: a move, a rename, a status change and `PUT /api/config` are
last-writer-wins and answering the same value twice is the same result. The guard answers a replay, never a
race — two arrivals carrying one token that land together both find nothing claimed.

**Tests:** `backend/server/client_claim_test.go` — a replayed send, create and fork leave one row each, a fresh
identity for the same words still runs, one task's identity answers nothing for another, and an edit replayed
after its own first arrival still lands. `backend/store/claims_test.go` holds the claim itself.

## Child rows under a task path

A route under `/api/projects/{projectID}/tasks/{taskID}/` names a task, but threads, messages and the
diff cache are keyed by task or message id alone, so the lookup that fetches one never learns which task
asked. Each of these resolves the task in the path first (`store.GetTask(projectID, taskID)`). Where
the row carries a task of its own it compares its `TaskID` with the path before anything is read or
deleted; `server.messageInTask` is the helper for the message routes, and `HandleTaskDiffFile` settles
the pair before the cache. A handler that hands `{messageID}` straight to `GetTaskMessageByID` answers
for another project's row.

Tests: `backend/server/handler_message_ownership_test.go`, `backend/api/git_diff_file_ownership_test.go`.

## The conversation is a window, not the thread

`GET /api/projects/{projectID}/tasks/{taskID}/messages` answers the **newest window** of the task's active
thread: the body stays the flat array of rows (chronological, oldest first) and the window rides two
response headers — `X-Messages-More`, and `X-Messages-Before`, the opaque cursor that asks for the
next-older page. Nothing counts the conversation: a row total is a second walk of it, and no client asks
for one. `?limit=` sizes the window **in units of conversation, not rows**. An assistant row and the
`tool_call` rows whose `assistant_msg_id` names it are one answer; a user row is one on its own. Opening a
task wants the newest handful of answers, and the row count of five answers depends on how much tool work
each one did. Default 5, ceiling 25 — outside that a 400 naming the parameter — and `?before=<cursor>` walks
back. `?from=<cursor>` **floors** one page of that walk. The named row is the oldest the page may answer,
and the rows below it stay for the next page rather than being skipped. A floor travels with `before` — alone
it would name an older end for a read starting at the newest one — and is a 400 naming `from` if it is not a
cursor this route issued. A cursor the route never issued is a 400, not an empty page: a client
must not read its own bad token as "no history". `full=1` changes only the redaction, never the window,
and `GET …/messages/{messageID}` is unchanged.

Two invariants hold the transcript together under paging. **A page never opens on a row it cannot explain**.
The cut falls at a unit boundary, so a window never starts on a `tool_call` whose assistant row is missing:
a card is never drawn for a response the page never received. The one case that breaks it is an answer
holding more rows than the read's own row ceiling, which the read cannot see past; that page opens mid-answer and
the row that opened the answer arrives on the next page. One turn can hold more answers than a
window, so a page may open mid-turn. `X-Messages-More` is what says older content exists. **Every row is handed
over exactly once across the pages.** The cursor names the oldest row this page holds, so the next page
starts older. Nothing between two reads is skipped or repeated. **A floor does not break that rule.** It
bounds where one page stops. `X-Messages-Before` of a floored page names that page's oldest row like any
other, so the rows below it stay one read away — answered later, never jumped.

**The prompts are a read of their own.** `GET /api/projects/{projectID}/tasks/{taskID}/turns` answers where
the user rows are: the newest prompts of the task's active thread, oldest first, with `X-Turns-More` and
`X-Turns-Before` naming the older page. Each carries its row id, its instant, and `text` — the head of the
prompt up to `store.TurnLabelChars` characters, bounded in the read rather than in the response. Each also
carries `cursor`: that prompt's own position, in the encoding the window's `before` and `from` take. A client
that wants one prompt's rows asks for them by name instead of walking to them. The chat's sticky header
needs the prompt above the rows it holds, and the window counted in answers cannot name it. One turn is
routinely more answers than a window, so the prompt that opened that turn is older than every row the task
opens on. Default 10, ceiling 50 — outside that a 400 naming `limit` — and a `before` this
route never issued is a 400 naming `before`. Its cursor names a prompt and the window's names a row: the
two reads share the cursor's encoding, not its meaning. Tests: `backend/store/messages_turns_test.go`,
`backend/server/handler_turns_test.go`.

**The client owns the merge.** `TaskChat` keeps every row it has fetched and rebuilds the transcript from
all of them. `prependRows` in `$lib/utils/chat-window.ts` dedups by row id, so a turn longer than a page is
still one entry once both pages are held. **A newest-window read joins the rows held, it never replaces
them**. The window is the newest answers, so every row it does not name is older than every row it does.
`applyWindowRead` puts the window behind the older pages, and the cursor naming their oldest row keeps
answering "what is above the screen". The transcript draws a read ahead of the entries it does not name, so
a read that answers alone puts the newest answers above the older pages. The bottom of the list stops being
the newest content, and the jump-to-latest button lands on an old row. A window that opens nowhere inside the
rows held cannot be joined: another thread's read, or a truncation that cut the overlap away, answers alone.
**Paging has two starters**, and each names what it wants. The list edge is the passive one: it fires a
request a viewport and a half before the reader gets there (`atTopOfList` and its `EARLIER_EDGE_VIEWPORTS`,
in `frontend/src/lib/utils/chat-window.ts`), so the page lands while they are still reading toward it. The
bubble floors one page at the prompt it names. The go-to-first control walks `before` until
`X-Messages-More` runs out, so it lands the conversation's first message rather than the top of the window
loaded so far.

The scroll
position is preserved by re-finding the entry under the top edge of the viewport (`data-entry-id`), never
by a pixel guess, and re-applied over the frames after the insert. Entries are `content-visibility: auto`,
so the rows above report placeholder heights when a page lands and grow after it. Measured, one landing
slid the reader 156 px off their row before this settled. The reader's own scroll **ends the settling**, and
it is read off the position, not off a gesture event: a kinetic scroll fires no `wheel`, `touchstart` or
`keydown` once the finger leaves the screen. On touch the listeners cannot see it, so the settle writes
against the fling (measured on a phone viewport: 28 writes in 91 frames). `readerScrolledUp` is the rule —
an upward move the loop did not write is the reader's — and a move downward is not, because scroll
anchoring writes that direction too.
The offline cache holds **the newest window plus the rows this browser already saw directly above it**,
bounded by `CACHED_ROWS_FLOOR`. A window of five answers is fewer rows than an earlier open may have read, and
an offline reload opens on the bigger of the two. Rows a window leapt past are dropped, not kept: a page is
only ever prepended, so a holed mirror either skips the hole or lands the fetched run above older rows. A page
fetched by cursor is still not cached on its own — that is what the merge is for, and the `mergeCachedRows`
cases in `chat-window.unit.ts` are what holds the bound.

The read walks the `(scope, created_at)` index for the window's positions, then fetches those rows by
`rowid`. Positions come first, so the cut and the sort run over index entries, and the answer to "is there
an older page" is one bounded `EXISTS` on the same index. The sort, the cut and the redaction pass are
therefore proportional to the window, not to the thread. The walk is not covering: `id` and `role` are not
index columns, so every position is looked up in the table — measured at 3x the cost of the same walk over
`created_at` and `rowid`. `role` is what the answer boundary is cut on; dropping `id` from the walk is the
lever if that number ever matters.

**The window bounds rows, not bytes.** The read takes at most `MessageRowCeiling` positions, which is the
response guard when the newest few answers are enormous; a conversation of a few rows holding tool output
costs what reading those rows costs. Keeping that case bounded by bytes is a budget over the window, which
this does not have.

Tests:
`backend/store/messages_window_test.go` (including the `EXPLAIN QUERY PLAN` claim),
`backend/server/handler_message_window_test.go`,
`frontend/src/lib/utils/__tests__/chat-window.unit.ts` (the page-up dedup and the join a newest-window read
makes). `frontend/tests/e2e/chat-window-scroll.spec.ts` answers what only a browser can: nothing is rendered asking
the reader to fetch a page, reaching the top edge fetches it unasked, and the page leaves the reader on the
row they were reading. `frontend/tests/e2e/chat-scroll-jitter.spec.ts` answers the touch half, which the
wheel spec cannot: it tags every `scrollTop` write so an app write is distinguishable from the browser's
kinetic scroll and anchoring, and asserts the settle leaves a fling alone and a still reader on their row.

## Diffs

`?parsed=1` is a contract every path on `/tasks/{id}/diff` must honour, including the fast ones. The panel
renders `parsed_files` and never re-parses patch text, so `git.ParseUnifiedDiff`
(`backend/git/diff.go`, served through `git.SortedParsedFiles`) is the only diff grammar in the
product. `GET /api/projects/{id}/git/diff` answers that grammar unconditionally — it has no raw mode and
reads no `parsed` flag — while `/tasks/{id}/diff` still branches on `?parsed=1`, because its manifest and
`TaskDiff` answers carry per-file data rather than patch text. The immutable done-task snapshot stores its
body *as the parsed body* (`doneDiffResponse.ParsedFiles`) and stays a byte-for-byte write: a response
missing that key renders "No changes since last commit" over a diff it still carries. A task whose worktree
is gone is diffed from the project repo, and that fallback calls `git.WorktreeManager.TaskDiffFromRepo`
as well, so no path can answer with patch text alone. Tests:
`backend/stream/done_snapshot_test.go`, `backend/api/git_diff_parsed_test.go`.

**The badge a diff row wears comes from the parsed file.** `getStatusBadge` (`$lib/utils/diff-parser.ts`) is
the only source: it reads the parsed status. `GitStatusPanel.svelte` keeps its own badge for a git status
entry, which is a different grammar with different inputs. Tests:
`frontend/src/lib/utils/__tests__/diff-parser.unit.ts`.

**A diff row's number is what its gutter shows, not a line of one file.** A unified diff carries one gutter:
`git.ParseUnifiedDiff` numbers a removed row from the old file and every other row from the new one, so one
number names two lines and nothing but the row's own text identifies it. `DiffViewer.svelte` looks the
highlighted HTML up by that text for the same reason. A click resolves through `jumpTargets`
(`$lib/utils/diff-parser.ts`): the editor opens the new file, so a removed row opens the new-side line its
deletion left behind, and a row of a file with no new side is no control at all. The highlight pass runs for
every file the panel draws — tracked, committed and untracked — rather than for the ones the reader has
collapsed and reopened. Tests:
`frontend/src/lib/components/__tests__/diff-row-text.unit.svelte.ts` (a row paints its own text),
`frontend/src/lib/components/__tests__/diff-row-keyboard.unit.svelte.ts` (where a click lands).

**A ref comparison is not reconciled by working-tree stats.** The panel shows one comparison and the board
stream keeps pushing the task's working-tree stats, so the view that came from `?head=` or `?base=` has to
say so. `DiffCache.loadDiff` and `refreshManifest` record whether the view on screen compares refs
(`DiffCache.refDiff`), and `processStats` returns while that holds. Without it a stats frame refetches every
path from the other comparison — `/diff/file` answers the worktree against the baseline — and overwrites the
immutable done-task diff the reader asked for. Tests:
`frontend/src/lib/services/__tests__/diff-cache.unit.ts`.

**A ref comparison carries no committed half.** `GetTaskDiff` (`backend/git/diff.go`) attaches the
`baseRef..HEAD` patch as each file's committed rows to the working-tree comparison only, where HEAD is one
of the two trees being compared. A request that names a head ref already names both trees, and HEAD is a
third thing: attaching it lists files the head ref never saw. `git.WorktreeManager.TaskDiffFromRepo`, the
worktree-gone path, has the same rule, so a task with no worktree answers the same question. Tests:
`backend/git/diff_committed_half_test.go`, `backend/api/checkpoint_diff_test.go`.

**One diff answers one hash.** `stream.DiffHash` takes the whole `git.TaskDiff` rather than four strings, so
no producer picks its own answer to "what counts as committed work": `git.TaskDiff.CommittedHalf` is the
overlay for a working-tree comparison and the comparison itself for a ref comparison, and
`has_committed_changes` comes from the same method (`HasCommittedWork`). The stored done-task snapshot
(`backend/stream/done_snapshot.go`), the worktree watcher and the executor all build `task_diff_stats` that
way. Two producers describing one diff with two hashes is what makes every client refetch a diff that never
changed; a false `has_committed_changes` drops a card's mark for work that is committed but unmerged.
Tests: `backend/stream/diff_stats_producers_test.go`.

**An absent committed half stays absent.** `DiffCache.committedHalf` leaves `committedRows` out when the
response carries none, and the committed section of `DiffViewer.svelte` tests `committedRows?.length` rather
than truthiness. An empty array is a truthy "there is a committed half", which is what makes that section
paint "Empty file" over the rows the file holds. Tests:
`frontend/src/lib/components/__tests__/diff-merged-committed-half.unit.svelte.ts`.

**A ref comparison never reads the file cache.** `GET /api/projects/{id}/tasks/{taskID}/diff/file` answers one
of two questions, and which one the request says. Without refs it answers the task's worktree against HEAD at
the baseline `git.WorktreeManager.TaskDiffBaseline` names for that task, which is all `api.taskDiffCache`
(`backend/api/git.go`) may hold — one entry per task, that comparison only. With `?head=` (and optionally
`?base=`, defaulting to that same baseline) it recomputes that path between the two named trees and never
consults the cache. A checkpoint panel whose "show full diff" fetched the worktree would print later work
under the checkpoint's heading, and write those rows into the panel behind it. A ref comparison names the base
it measured from in `base_ref`, the key the manifest already uses, so the panel prints both sides of what it
shows. The worktree watcher's
cache update skips a done task, because for a done task it computes `BaseCommit..BranchTip`. The
panel lists files from one diff and then fetches their rows by path, so a second comparison stored under the
same task id arrives labelled with the stats above it: the reader sees lines the task never touched, and no
request failed. Tests:
`backend/api/git_diff_identity_test.go`, `backend/stream/worktree_watcher_baseline_test.go`,
`backend/api/checkpoint_diff_test.go`.

## The directory listing's ignored rows

`GET /api/fs?project=&task=&path=&files=1` answers one directory. Every `entry` carries `name`,
`isDir`, `path`, `isGitRepo` and `ignored`.

- **`ignored` is git's answer, never this server's guess at a name.** One
  `git check-ignore --stdin -z` process judges the whole directory (`git.IgnoredPaths`), run in the
  listed directory so a nested `.gitignore` speaks for the directory that holds it. A tracked path is
  never `ignored`, so a rule written after a commit does not dim committed work. Any git failure
  answers `false` for every row.
- **The explorer draws every row the server answers, and dims the ignored ones.**
  `FileBrowserView.svelte` filters nothing; the one name kept out of a listing is `.git`, and it is kept
  out at the source. `file-browser.css` gives an `ignored` row the muted text colour, and a selection
  still reads as one.
- **No listing offers a row for `.git`.** `listingSkipsName` (`backend/server/handler_fs.go`)
  drops the name from both listing branches — the host `os.ReadDir` walk and a sandboxed project's
  `ls -pA` — so a sandboxed project cannot show what a host one hides. Both shapes of it go: the
  directory of a full checkout, and the one-line gitfile of a linked worktree. The reason is that a row
  is a click target. The add-project picker's Select answers with the directory it has loaded, so a
  `.git` row could become a project's path, and Settings → Sandbox offers every row as a mount source.
- **Unlisted is not forbidden to a read.** A path box in either picker, an Explorer deep link and every read
  route still resolve `.git`, so a deep link can open it and archiving a parent still carries it. Nothing that
  changes a file does: the batch routes refuse a `.git` row, and `requireNoGitSegment` refuses it for a write
  and an upload — `docs/confinement.md → File endpoints: the project plus the task the request names`. The task
  explorer browses by breadcrumb with no path box, so within that panel the name is reachable only by a deep
  link.
- **Nothing about `.git` is git's verdict.** The row is dropped before `markIgnored` runs, and
  `check-ignore` answers `false` for `.git` anyway — `..` is the only name there that fails the batch.
  A listing keeps `.github` and `.gitignore`, because those are the person's own files.
- **A listing of no repository marks nothing.** The directory picker — the request with no `project`
  — asks no question about git at all, so every `ignored` it receives is false.
- **`ignored` decides nothing else.** `fs_event` still drops an ignored path (`shouldSkipFsEvent`), so
  a regenerated `dist/` reaches a listing on the next request rather than by itself.
  `/api/fs/search` narrows further: it skips a dot-prefixed name (`searchSkipsName`) and the
  well-known build directories (`git.ShouldIgnorePath`), whether or not a rule mentions them.

Tests: `go test ./backend/server/ -run TestFSListing` — which covers the dimming, the listing with no
repository, and the absent `.git` row — and
`frontend/src/lib/components/__tests__/file-browser-lists-everything.unit.svelte.ts` for the rows that
vanish or stop being dimmed.

## Raw file bytes

`GET /api/fs/raw?project=&task=&path=&ref=` is the only route that returns a project file's **bytes**.
`/api/fs/read` answers JSON, and a JSON string of a PNG is that PNG with every byte that is not valid
UTF-8 replaced, so the diff panel's "View image" cannot use it. Because the response is an `<img>`
subresource, what it can carry is the whole permission:

- `path` is **relative to the repository root** — the shape every diff surface already holds. An
  absolute path is refused rather than translated, so the route never has to guess whether it was sent a
  host path or a container path.
- One map (`rawImageTypes`) is both the allowlist and the `Content-Type`. A name off it is a 415, which
  is what keeps SVG out: SVG is markup, served inline from the app's own origin it would run, and no CSP
  header on an image response stops that. `frontend/src/lib/utils/diff-image.ts` holds the same set.
- `Cache-Control: no-store`, because the same URL answers differently after the next edit. `nosniff` and
  the `img-src 'self'` CSP come from `CORSMiddleware` like everywhere else.
- **No `Content-Disposition`.** The name comes from the repository, and putting it in a header makes a
  crafted filename a header writer; `inline` is the default the route wants anyway. A response that
  *saves* a file instead of showing one is `GET /api/fs/download`, below, which carries the name through
  an encoder rather than echoing it.
- **`CompressMiddleware` passes this response through without buffering it.** The middleware keeps a
  whole GET body to hash an ETag and then compresses it. For an image that costs several times the file
  in memory plus a brotli pass over bytes a codec already finished with, and the buffer sits outside the
  route's cap so the cap stops being the bound. The escape is by `image/` content type, beside the SSE
  one, and it keeps the handler's `Content-Length`. `TestGetImageResponseIsPassedThrough` and
  `TestFSRawImageIsNotBufferedOrRecompressed` hold the two halves.
- `ref` reads the committed object instead of the file on disk, with `types.ValidateGitRef` and
  `--end-of-options` like every other client-supplied ref. It runs in **`project.Path`**, not the task's
  worktree: worktrees of a project share its object database, so a done task whose worktree commit-and-done
  deleted still answers — which is when the merged diff panel asks. A ref is any rev that database
  resolves — a branch, a commit, a bare object id — so this half is jailed to the **project**, and is not
  limited to commits reachable from whatever `?task=` says; `?task=` is still bound to `?project=` before
  the read, so another project's task gets nothing.
- Its own size cap (`maxRawFileBytes`, the upload cap) is checked before any byte is read — on the stat
  for a working tree, on `git cat-file -s` for a `ref` — and again by the bound on the read that follows:
  `io.CopyN` on the file, `io.LimitReader` plus a process kill on the object.

Tests: `backend/server/handler_fs_raw_test.go`.

## Archive listing

`GET /api/fs/archive?project=&task=&path=&ref=` answers what one archive holds. `GET /api/fs/archive/entry`
adds `&entry=` and answers the text of one member (`backend/server/handler_fs_archive.go` and
`backend/archive`). Both are read-only: the server walks the container, and no member is ever written to a
disk.

- **The format comes from the bytes, never the name.** `archive.Open` sniffs gzip, bzip2, zip and tar
  magic. A file whose suffix claims an archive and whose bytes say otherwise is a 415, so the pane shows
  the server's sentence instead of drawing bytes as text. The suffix decides only which pane opens
  (`frontend/src/lib/utils/archive-file.ts`), so the names there and the formats the server reads must stay
  in step.
- **The listing is the archive's own order.** `Reader.List` walks members as the container holds them and
  never sorts, so the answer is a property of the file and no locale touches it. A row is
  `{name, size, mtime, isDir}`. `mtime` is fixed-width UTC and empty when the container recorded none.
  The column says "Modified": tar and zip store no creation time, and a heading that promises one lies.
- **A member is matched by exactly the name its row shows.** `requestName` strips a leading `./` or `/`,
  and one normalisation names both the row and the lookup, so no prefix or separator trick reaches a
  different member. One name twice is legal in both containers, so the listing repeats it and the read
  answers the first — which is why the pane keys its rows by position and not by name.
- **The member answer is the `/api/fs/read` answer** (`fsReadResponse`), so a member and a file are shown
  in the same words by the same editor.
- `ref` reads the committed object the way `/api/fs/raw` does: same validation, same `project.Path` object
  database. The panel that offers the button shows a side that may not be in the worktree at all.
- **The refusals are the whole vocabulary.** 415 is `not a readable archive` or `not a text file`, 404 is
  `no such file in the archive`, and 413 is a member over `maxFSReadBytes` or a committed archive over
  `archiveCommitBufferBytes`. 504 means the walk ran out of `archiveWalkBudget` before it could say
  anything. A walk cut short after it had read rows is not a fault: it answers those rows with
  `truncated: true`, because they are true.

Tests: `backend/archive/archive_test.go`, `backend/server/handler_fs_archive_test.go`,
`frontend/src/lib/components/__tests__/archive-listing.unit.svelte.ts`, and
`frontend/tests/e2e/explorer-archive-listing.spec.ts`.

## Downloading a file or a folder

`GET /api/fs/download?project=&task=&path=&path=…` (`backend/server/handler_fs_download.go`) is the route
the Explorer's "Download" asks for. One URL answers two questions, and the response says which one it
answered:

- **One path naming a regular file** is that file's own bytes. `application/octet-stream`, the handler's
  `Content-Length`, and the name it has on disk in `Content-Disposition`. Wrapping a single file in an
  archive hides its name and its type under a layer the person then has to undo.
- **A folder, or several paths,** is one gzip'd tar (`application/gzip`). No `Content-Length`, because
  gzip's size is not known before the walk. `fsutil.WriteTarGz` names every entry relative to the first
  path's folder, so one folder unpacks to that folder and a selection unpacks to exactly the rows it held.

What holds it up:

- **`path` is repeatable** and takes the shape `/api/fs/read` takes: the host or container path the
  listing handed the browser. A selection is a list of rows, not one path.
- **Every path of the request is jailed before anything opens** — `fsRequestScope` then `fsScopeTarget`,
  the batch routes' two halves — so a selection whose last row leaves the project sends no bytes. A
  `.git` row refuses the whole request, as it does on the batch routes.
- **A path above the first path's folder is a 400.** Its archive name would walk out of the folder that
  was clicked. `fsutil.WriteTarGz` refuses that shape too; the route answers it as a request refusal
  rather than as a failure halfway down a stream.
- **Nothing is capped, because nothing is held.** One 32 KiB buffer per download, the archive written
  straight to the response, and no `WriteTimeout` on the server. A large folder costs time and no
  memory. What the request keeps is `maxFSOpsBatch` (100 paths), which bounds its shape rather than its
  content.
- **The tree rules live in `fsutil.walkTree`, the walk a copy takes**, so the two cannot drift apart.
  Symlinks are archived as links and never read through, so no bytes from outside the jailed tree
  arrive. Irregular files are skipped. `.git` stays out: the listing never shows one, and a `.git`
  config can hold a remote URL with a token in it.
- **The name in the header is encoded, never echoed.** `attachmentDisposition` builds it through
  `mime.FormatMediaType`, which percent-encodes what RFC 2231 keeps out of a header value, and adds an
  ASCII-folded `filename=` twin for a recipient that reads neither. `sanitizeDownloadName` folds
  separators, control characters, a trailing dot, and a name longer than a file name.
- **`/api/fs/raw` refuses a name in a header and this route cannot.** That response renders on this
  origin; this one is saved. The encoder is what makes the difference safe.
- **`CompressMiddleware` passes an `attachment` response through**, beside the SSE and `image/` escapes.
  The middleware keeps a whole GET body to hash an ETag and then brotli-compresses it. For a download
  that is the entire archive in memory, gzipped twice, with the single file's `Content-Length` deleted.
  The disposition is the trigger because it is what the handler sets when the response is a file rather
  than a document.
- **A failure after the first byte cannot be a status.** `fsutil.WriteTarGz` reports a tree that changed
  mid-stream; the route logs it and the download ends short. A truncated file the person asks for again
  beats an archive whose framing is broken.

The client hands the URL to the browser instead of fetching it: `downloadURL` in
`frontend/src/lib/api/fs.ts`, clicked from an anchor with `target="_blank"`. The browser's own download
UI is what streams and reports bytes this app never holds. A refusal then arrives as a page in a tab of
its own, not in place of the explorer.

Tests: `backend/server/handler_fs_download_test.go`, `backend/fsutil/tar_test.go`,
`frontend/src/lib/components/__tests__/file-browser-download.unit.svelte.ts`.

## The Explorer's file batch

`POST /api/fs/delete`, `/copy`, `/move` and `/rename` (`backend/server/handler_fs_ops.go`) are the four writes
`handler_fs.go` does not already have, and they share one answer: `{ done: [...], failed: [...] }`, each entry
`{ from, to?, renamed?, reason? }`.

- **Both lists are always present** — an empty `failed` is the answer the caller reads, not a missing key — and
  `from` is the path *as the caller named it*, so a browser browsing a sandboxed project never has a host path
  handed back to it. `to` is absent when the item went nowhere.
- **`renamed` is the server admitting it chose a different name.** A destination folder that already holds the
  name takes `name copy`, `name copy2`, … rather than overwriting or failing, and only the server knows which
  items needed one, so the listing lists them in a dialog (`pasteRenamedEntry`) instead of leaving a paste that
  renamed files to be discovered later.
- **A refusal has two altitudes.** The jail's and the request's — an uncorrelated `?task=`/`?project=`, a path
  outside the project, a destination that is not a folder, an empty batch or one over
  `maxFSOpsBatch` (100 paths), a destination folder that no longer exists — fail the **request** before a path
  is touched, which is what makes a paste atomic. Anything learned while running (a source that vanished, a
  `.git` row, a tree over its copy bound) is one entry in `failed` and the rest of the selection still lands,
  because a batch is a person's selection. The vocabulary is `fsOpsReason`. Fsutil's own refusals and
  `os.ErrNotExist` are sentences the requester can act on, so they pass through. Anything else is
  this server's problem: its cause goes to the log and the caller gets MIKE's sentence, like
  everywhere else (`Errors` below).
- A move into the folder the path already sits in answers `done` with no `to` — the no-op the click asked for —
  while the same request as a copy is a duplicate, which is why only the move short-circuits. `/rename` is the
  one single-path route, and a name the folder already holds there is a **409** rather than an auto-rename:
  the person typing the name chose it, and a third name nobody asked for would be the worse answer.
- Like `write` and `mkdir`, these routes carry `?project=` (plus `?task=` for a task's listing), declare their
  verb in the pattern, and take the CSRF header.

Tests: `backend/server/handler_fs_ops_test.go`; client side `frontend/src/lib/api/fs-ops.ts` and
`frontend/src/lib/components/__tests__/file-browser-context-menu.unit.svelte.ts`.

## Errors

A server fault answers MIKE's own sentence; the cause goes to the log. `errs.Operation(operation, cause)`
+ `httputil.Fault(w, operation, err)` is that classification, and `httputil.Fail` is the only place a
status code is chosen. Echoing `err.Error()` hands the browser git's stderr, a driver message or an
absolute worktree path, and logs nothing.

- An `Operation` does **not** unwrap its cause: `IsNotFound` must not inherit from a failed lookup, or a
  broken repository answers 404 "task not found" about a task that exists.
- A handler whose callee can refuse with an `errs.Conflict`/`ErrNotFound` sentinel uses `Fail`, not
  `Fault` — `RetryTask` propagates `ErrTaskAlreadyRunning`, so retrying a running task answers 409.
- `httputil.Fail` maps `context.Canceled` to 499 at debug — no fault to report, and an operator's log is
  not a transcript of every closed tab — and everything else to 500 at error.
- An **admission refusal** is not a server fault and reaches neither `Fail` nor `Fault`: a socket over its
  connection cap is answered where the count is read — `http.Error` 503, naming what is refused and never
  a cause — while the live count and the limit go to the log at `Warn`. Same for a subscription a single
  connection asks for past its own cap: the refusal is logged, the connection keeps running and keeps
  every key it already held. See *Every upgrade is admitted by a count, never a deadline*.
- Every answer of 400 or above is logged once, by `StatusLogMiddleware`, at the level its status class
  deserves: `Error` for a 5xx, `Warn` for the four statuses that mean someone was kept out (401, 403,
  421, 429), and `Debug` for every other 4xx. It sits just inside `PanicRecoveryMiddleware` and outside
  the bearer-token, CSRF and session middlewares, so a recovered panic and a refusal at the gate are
  reported by one rule.
- That record names the **route**, never the request. `routeFor` asks the mux which route a request would
  reach, because a middleware that calls `r.WithContext` copies the Request and the mux records the
  pattern on that copy. The path and query stay out: a preview path segment is a live bearer token, and a
  pairing URL query carries a code. `addr` is `clientIP`, the address the login limiter budgets under. The
  cause of a 500 is a second record — `Fail` writes it, and this one cannot know it.
- The ratchet is the `status500` archgate rule (0 violations across 289 examined).
- One seam decides whether upstream text reaches the browser. Words from a remote **stay** for clone,
  checkout, pull and push: they are the diagnosis of a repository the person owns. Nothing else tells
  them the branch does not exist, or that the remote rejected a non-fast-forward. They are bounded by
  `subproc.Summarize` at `subproc.AnswerMax`, and the full run is mirrored to the log at `Warn`. A
  sandbox `exec` failure and a database fault **do not stay**: each answers MIKE's own sentence and puts
  its cause in the log, since both describe this server and the caller can act on neither. A sandbox
  image build refusal is neither case, because its Dockerfile is the caller's own file, so its text
  stays with the 502. A refusal written for the caller (a path outside the jail, a label too long) keeps
  its sentence too.

Tests: `backend/errs/errs_test.go`, `backend/httputil/httputil_test.go`, and
`backend/server/status_log_test.go` (which level each refusal reaches, and that the route is named while
the path and the query are not).

## Response headers

Every response carries `Permissions-Policy` (camera/microphone/geolocation/usb/serial/hid/bluetooth/
payment/display-capture/wake-lock/idle-detection/keyboard-map/window-management denied — everything with
no caller in `src/`; the clipboard stays, the terminal pastes with it) **and**
`Cross-Origin-Resource-Policy: same-origin`, except the two trees an opaque-origin frame loads as
subresources: `/extensions/` and `/api/preview/{token}/{path...}` (`isOpaqueOriginAsset`). Marking those
breaks the extension iframe and the preview's own assets.

Nothing here answers with `Access-Control-Allow-Origin`, in any value. With the preview frame's opaque
origin, that absence is what makes a previewed file loadable and unreadable at the same time. The one
value that would lift it is `null` — what an opaque origin calls itself — and it admits every sandboxed
iframe on the internet. Gate: `TestPreviewResponsesNeverGrantACrossOriginRead`.

`CompressMiddleware` puts a strong ETag — a digest of the body — on every GET it buffers, and answers
`If-None-Match` with a 304 that repeats the `ETag` and whatever `Cache-Control` the handler set. A
handler that sets its **own** `ETag` therefore loses the 304: the middleware skips the `If-None-Match`
check when a tag is already there. What a handler decides is the lifetime. The
extension lists decide `Cache-Control: no-cache` — "keep a copy, ask before showing it" — so Settings → Extensions →
Browse can paint from a stored index only after this server agreed it has not moved
(`HandleList`, `HandleRegistryIndex`, `HandleLocalExtensions`). `handleTaskMessages` decides the same
lifetime: reopening a task asks the same question, and a thread that has not moved answers `304` with no
body. `TaskHandler.HandleSync` (`GET …/tasks/{taskID}/sync`) decides it for the same reason in the other
direction — a returning window asks whether anything moved, and a kept digest *is* the stale answer it called
to replace — so the route is `no-cache` and its 304 carries the header back. It is a task route, so it lives
in `backend/api`. The handler sits in `task.go`: the `http` archgate rule counts a domain package's files that
import `net/http` and refuses a new one. `syncDigest` (`task_sync.go`) builds the answer and imports nothing of
the transport. It reads the hub for the conversation revision, which the one-way
`server → api` edge permits because `api` already holds the manager. Tests:
`backend/server/handler_task_sync_test.go` (the 304 round trip and the `no-cache` on both answers),
`backend/api/task_sync_test.go` (the body). An answer holding arbitrary extension data is not worth storing
anywhere, so `GET /api/extensions/{id}/storage` declares no lifetime at all.
SSE and `GET /api/fs/raw` image bytes are passed through unbuffered and carry no ETag.
Tests: `backend/server/handler_headers_test.go`, `backend/server/compress_test.go`,
`backend/server/handler_message_window_test.go`.

## WebSocket upgrades

The terminal WebSocket verifies `Origin` in `httputil.AcceptWS` (`backend/httputil/ws.go`), which every
WebSocket route in this server upgrades through, so the rule is stated once and is not inherited from
`coder/websocket`'s `authenticateOrigin` (that one answers "allowed" the moment the header is empty, so
"the library checks Origin" is really "a browser always sends one"). `SameSite=Lax` blocks the cross-site
request first; this is the second lock on an endpoint that hands out a shell. A request with **no** Origin
is still accepted: only a non-browser client omits it, and that client can set the session cookie by hand
anyway. Test: `backend/httputil/ws_origin_test.go`.

### Every upgrade is admitted by a count, never a deadline

`/api/stream`, `/api/lsp` and the two terminal routes refuse a socket **before** `httputil.AcceptWS` —
`http.Error` 503 plus a `Warn` naming the live count and the limit — so the browser sees a refusal rather
than an upgrade it has to work out is dead. Each bound is a package constant mirrored by a field on the
manager (`maxConns` on `AppStreamManager` and `TerminalManager`, `maxSessions` on `LSPHandler`) so a test
shrinks the number instead of opening hundreds of sockets. Nothing here reads an environment variable, and
nothing here is a deadline: the counts decide, and the one wait a request suffers is its own, off any
other request's path (`docs/lsp-transport.md`).
The same rule holds the bookkeeping *behind* the upgrade — a bound that is reached answers what it
overturns, it never drops it silently.

| Bound | Default | Counts | When it is reached |
|---|---|---|---|
| `stream.DefaultMaxConnections` | 256 | live `/api/stream` sockets | 503 before the upgrade, and a `StatusTryAgainLater` close for the handshakes that arrive during the gap (see below) |
| `lsp.DefaultMaxSessions` | 64 | live `/api/lsp` sessions | 503 before the upgrade |
| `term.DefaultMaxConnections` | 64 | live terminal sockets, across every session | 503 before the upgrade |
| `lsp.DefaultMaxPendingRequests` | 256 | requests one session has waiting for an answer | the oldest is answered with a JSON-RPC error and loses its slot; an answer that does arrive frees its slot first |
| `lsp.DefaultMaxQueuedInitializes` | 64 | `initialize` requests waiting behind the one in flight | the whole queue is failed as a failed send fails it, the overflowing request in the same sentence, so a later `initialize` is forwarded |
| `lsp.DefaultMaxQueuedMessages` | 128 | messages waiting for one workspace's language server | a request is answered with a JSON-RPC error; a notification is logged, because the server's copy of that document is then unknown |
| `lsp.DefaultMaxQueuedFrames` | 128 | frames one browser has not read off its `/api/lsp` socket | that socket is ended with `StatusTryAgainLater`, so the browser reconnects and resyncs |

Terminal counts connections rather than PTYs because a session is per task or project and every client of
it shares one shell, so connections are what a client can multiply.

A pre-upgrade count is an offer, not a decision: handshakes arrive during the gap between reading it and
registering the socket. `/api/stream` is the one endpoint that closes the gap — counting and joining
`clients` are one decision under `asm.mu`, and a connection that turned out to be over the bound is
closed after its upgrade with `StatusTryAgainLater`, which the client already reads as "reconnect and
try again" (`docs/reconnection.md`). The other three read the count before the upgrade and register
after it, so a burst can land a few sockets past the bound: a bound that stops one browser from making
the server hold thousands, not an exact admission control. Tests: `backend/stream/app_stream_bounds_test.go`,
`backend/lsp/handler_bounds_test.go`, `backend/term/terminal_bounds_test.go`.

### Which socket carries the app-level ping

Every upgraded connection is kept alive by `httputil.Keepalive` (`backend/httputil/ws.go`), whose control
ping runs every 27 s (`KeepaliveInterval`) — under the 30–60 s idle timeout a proxy or VPN enforces.
Control frames are invisible to JavaScript, so they never fire `onmessage` and never reset a browser's
own silence timer; the app-level frame `{"type":"ping"}` (`KeepaliveFrame`) rides beside the control ping
on **`/api/stream` only**, because that client stamps `lastActivityAt` from `onmessage` and retires a
silent socket at `STALE_THRESHOLD_MS` (60 s, calibrated against the 27 s tick). `/api/lsp` speaks JSON-RPC
and the terminal speaks `types.TerminalMessage`, so both get the control ping alone: the frame is opt-in
(`httputil.WithAppFrame()`) rather than something the shared helper always sends. Nothing has to ignore
a frame it never receives.

Two rules keep a **busy** connection from being read as a dead one, and both are about `writeMu`, which the
pinger holds so a ping cannot interleave with the frames of the one endpoint that passes it — `/api/stream`.
`/api/lsp` and the terminals pass nil: there a frame is written by whichever goroutine produced it —
the session's own writer for `/api/lsp`, the connection's read loop for a terminal — each under its own
`WriteTimeout`. The pinger therefore has no lock of ours to refuse to wait behind. The ping's deadline
is built *after* the lock is taken, so `KeepaliveTimeout`
bounds the ping and never the queue in front of it, and the lock is taken with `TryLock`, because a lock
another writer holds is a frame on its way out — liveness evidence — so that tick is skipped. Queuing
instead was measurable: holding `writeMu` longer than the ping timeout lets a queued tick's deadline expire
in the queue, and `coder/websocket` closes the socket for an expired write context even when it wrote
nothing. The shape that made it measurable was a reconnecting client holding the lock across a whole replay
backlog (the per-task buffer); that backlog is a queue now, and what still holds the lock is one
frame's write, up to `WriteTimeout` of it. The peer is healthy, dies mid-replay, reconnects and replays
again. A pinger blocked on the lock also sends nothing, which retires the same socket from the client side
at 60 s. Tests: `backend/httputil/ws_test.go`.

### Who writes an app-stream connection

One goroutine per connection (`runSender`) is the only writer to its socket besides that connection's
pinger. Everything else — a turn's `SendTaskEvent`, the board broadcasts, a task's replay and the
sandbox-stats frame that extends it, a project's initial diff data — hands the frame to that
connection's bounded queue (`sendQueue`) and returns, so the goroutine producing a frame never waits
on a browser. The queue is bounded by frame count and bytes (`DefaultBufferSize`, `DefaultBufferBytes`)
and overflows by dropping **the oldest** frames, never the newest: a client that stopped reading is
caught up on the tail.

A connection that missed frames is ended once its queue has drained — see *What a reconnect brings
back* in `docs/reconnection.md` for why the close is the resync and not a new frame type, and
`backend/stream/app_stream_sender_test.go` with it. A replay goes in behind any replay already
queued and ahead of every live frame, which is the ordering the client can see: `subscribe_task` for
two tasks in a row replays one and then the other, the `sandbox_stats` frame lands at the end of the
replay it extends, and a live frame never lands inside either. Beyond that one rule the queue makes no
ordering promise between two producers.
