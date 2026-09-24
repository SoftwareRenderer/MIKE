# HTTP and WebSocket contract

The rules are in `AGENTS.md → API`. This is who owns an endpoint, the shape of the frames, the route
table's exemptions, and the error vocabulary.

## Which package owns an endpoint

**A domain package serves its own endpoints.** `backend/api` holds the handlers for domain resources —
`TaskHandler`, `ProjectHandler`, `BoardHandler`, `GitHandler` — so a new handler for a task, project, board
or git route goes there, constructed in `NewServer` and registered on the `*Server`'s mux. `backend/server`
holds the transport: the listener, TLS, session/CSRF middleware, the shutdown stage table, the detached
base context, and therefore the endpoints that are *about the listener* — the `(*Server)` methods in
`handler_*.go` (`handleFS…`, `handleThread…`, `handleSessions…`), which are methods on `Server` precisely
because they call `s.broadcastTaskDiff`, `s.lockWorktree`, `s.fsWriteTarget` and take the `s.gitTask` lock.
`handleTaskSend` is one of them for the same reason: the truncation it may perform releases the checkpoints
the discarded turns pinned and the forks keyed on their rows, which is `handler_message.go`'s work.

The direction is `server → api` and never back; `backend/api` imports no `backend/server`, which is why a
handler that needs the hub or a lock is *injected* with a function value instead. That one-way edge is what
makes two plausible refactors unavailable rather than merely deferred: folding every handler into one
package means moving `Server` with them or taking a `server ↔ api` import cycle, and splitting
`backend/api` by domain means inventing a shared-helper package to solve a naming problem, because
`resolveTask`, `requireWorktreeMgr` and `resolveWorktree` (`http.go`) are shared between `task.go` and
`git.go`. The mixed `Handle*`/`handle*` naming inside `backend/api` is the same boundary showing through,
not drift: a handler is exported because `backend/server`'s route table names it and unexported because only
the package calls it, so renaming one set into the other changes who may reach it.

## Who may call a route at all

Two credentials reach this server. A login cookie reaches everything the middleware guards. A bearer
token reaches the patterns in `tokenScope` (`backend/server/token_auth.go`) and nothing else, which is
checked against the pattern the mux reports for the request — so a route renamed without editing that
map loses its grant rather than keeping one under the old name. `docs/api.md` is the client-facing
version of that list and of what each route does; `TestTokenScopeIsTheApprovedSet` holds the code and
that list together. Nothing else in this file changes for a token caller except CSRF, which has no
cookie to protect.

The five `/api/updates/*` routes stay out of `tokenScope` for the reason that section is about: a bearer token
is what a headless client carries, and no headless client needs to replace this server's binary. They are
session + CSRF routes, all five declare their verb, and `GET /api/updates/status` answers from what the last
check learned rather than making a request of its own — the row that route serves is docs/auto-update.md.
`POST /api/updates/prepare` and `POST /api/updates/apply` are the pair that changes anything, split so the
fetch can run on a detached context while the swap stays behind a click; the route that ends the process is
`POST /api/updates/restart`.

## Frames

- Inbound frames are `stream.AppStreamMessage` (`Data` is `json.RawMessage`). **Outbound** frames are
  flat maps built where they're broadcast (no shared envelope struct in Go) — a `task_event` carries
  `task_id`/`event` beside `data`, so the client reads `msg.task_id`, **not** `msg.data.task_id`.
- `subscribe_task` carries `no_rows`: this connection holds no conversation rows of its own, so the task's
  replay buffer is kept whole for it. `docs/reconnection.md → The reload meets the replay buffer` owns that
  rule and what clears the claim.
- A frame that is not a JSON object is dropped and reported once (`parseStreamFrame` in
  `api/helpers.ts`, the one reader of both sockets' `onmessage`). A `JSON.parse` throw inside `onmessage`
  is swallowed by the event loop: the frame is lost, the socket stays open, and the app believes it is
  current. Test: `api/__tests__/ws-frame.unit.ts`.
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
`error`). A turn that ran tools and then lost the provider leaves no partial assistant row — the dead
call never reached `finalizeTurn`, which is what persists it — so the client drops the half-streamed
text with `dropAbandonedTurn()` and the retry streams the answer in its place.

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

## The acknowledged memory alert

`tasks.memory_alert_ack` is on every task payload: `null` when nobody dismissed the alert, an object
`{peak_mb, oom_kills, limit_mb}` when someone did — the numbers it showed at the time, which is what
decides whether it comes back (`memoryAlertAckCovers` in `$lib/utils/memory-stats.ts`; the client owns
that rule and the server stores only what it is told). It is a column on the row, so one dismissal
serves every device, and the route that writes it broadcasts the task the way any other task change
does: `POST /api/projects/{projectID}/tasks/{taskID}/memory-alert-ack`. All three fields are required —
an ack covering less than the alert dismissed would leave it standing on the device that did not click —
and each is a 400 naming itself when absent, wrongly typed, or negative.

## Route verbs

Every `/api` route declares its verb in the pattern (`s.mux.HandleFunc("POST /api/…")`), so the mux
answers 405 with an `Allow` header before any handler runs, and handlers do not check `r.Method` to
refuse. A new methodless `/api` route is an archgate `routes` violation, so it fails `make check` rather
than being noticed in review. A methodless route also escapes `CSRFMiddleware`, which guards non-GET only.

Exemptions, each with a reason: the extensions file-server subtree (`/extensions/`), the two WebSocket
upgrades (`/api/stream`, `/api/lsp` — they upgrade on GET and never inspect the verb),
`handler_preview.go`'s two `r.Method != http.MethodHead` checks (they suppress a HEAD *body*, not a
request), and `handleTaskMessages` (three verbs through one function).

The frontend is served **behind** the mux (`spaBehind`, `backend/server/routes.go`), never as a route in
it, and that is a route-verb rule rather than a styling preference: a catch-all pattern inside a
`ServeMux` outranks that mux's own verb-mismatch answer, so `GET` on a POST-only `/api` route gets served
the application document — a 200 that reads to a caller as a route which accepted the request. Behind the
mux, an unmatched path outside `/api/` is the SPA's and everything under `/api/` keeps the mux's answer,
405 and `Allow` included. `TestSPAServesNothingToAWrongVerbOnAnAPIPath` and
`TestMuxRefusesAnUnregisteredVerbWhileTheSPAServes` pin the two directions.

A handler that switches on `r.Method` is not a methodless route and is not stale either. Nine branches
across six files (`api/task.go`, `api/project.go`, `config/config.go`, `extensions/handler.go`,
`server/handler_message.go`, `server/handler_thread.go`) end a verb switch in
`httputil.MethodNotAllowed`, and because a `GET` pattern also matches `HEAD` it is that arm which answers
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

## Child rows under a task path

A route under `/api/projects/{projectID}/tasks/{taskID}/` names a task, but threads, messages and the
diff cache are keyed by task or message id alone, so the lookup that fetches one never learns which task
asked. Each of these resolves the task in the path first (`store.GetTask(projectID, taskID)`) and, where
the row carries a task of its own, compares its `TaskID` with the path before anything is read or
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
back. A cursor the route never issued is a 400, not an empty page: a client
must not read its own bad token as "no history". `full=1` changes only the redaction, never the window,
and `GET …/messages/{messageID}` is unchanged.

Two invariants hold the transcript together under paging. **A page never opens on a row it cannot explain**.
The cut falls at a unit boundary, so a window never starts on a `tool_call` whose assistant row is missing:
a card is never drawn for a response the page never received. The one case that breaks it is an answer
holding more rows than the read's own row ceiling, which the read cannot see past; that page opens mid-answer and
the row that opened the answer arrives on the next page. One turn can hold more answers than a
window, so a page may open mid-turn. `X-Messages-More` is what says older content exists. **Every row is handed
over exactly once across the pages.** The cursor names the oldest row this page holds, so the next page
starts older. Nothing between two reads is skipped or repeated.

**The prompts are a read of their own.** `GET /api/projects/{projectID}/tasks/{taskID}/turns` answers where
the user rows are: the newest prompts of the task's active thread, oldest first, with `X-Turns-More` and
`X-Turns-Before` naming the older page. Each carries its row id, its instant, and `text` — the head of the
prompt up to `store.TurnLabelChars` characters, bounded in the read rather than in the response. The chat's
sticky header needs the prompt above the rows it holds, and the window counted in answers cannot name it:
one turn is routinely more answers than a window, so the prompt that opened that turn is older than every
row the task opens on. Default 10, ceiling 50 — outside that a 400 naming `limit` — and a `before` this
route never issued is a 400 naming `before`. Its cursor names a prompt and the window's names a row: the
two reads share the cursor's encoding, not its meaning. Tests: `backend/store/messages_turns_test.go`,
`backend/server/handler_turns_test.go`.

**The client owns the merge.** `TaskChat` keeps every row it has fetched and rebuilds the transcript from
all of them. `prependRows` in `$lib/utils/chat-window.ts` dedups by row id, so a turn longer than a page is
still one entry once both pages are held. **Scrolling is the only
affordance** — no control asks the reader to fetch anything. The list edge is the request, and it fires a
viewport and a half before the reader gets there (`earlierEdgePx`) so the page lands while they are still
reading toward it.

The scroll
position is preserved by re-finding the entry under the top edge of the viewport (`data-entry-id`), never
by a pixel guess, and re-applied over the frames after the insert. Entries are `content-visibility: auto`,
so the rows above report placeholder heights when a page lands and grow after it; measured, one landing
slid the reader 156 px off their row before this settled. **The reader's own scroll ends the settling, and
it is read off the position, not off a gesture event**: a kinetic scroll fires no `wheel`, `touchstart` or
`keydown` once the finger leaves the screen, so on touch the listeners cannot see it and the settle writes
against the fling (measured on a phone viewport: 28 writes in 91 frames). `readerScrolledUp` is the rule —
an upward move the loop did not write is the reader's — and a move downward is not, because scroll
anchoring writes that direction too.
The offline cache holds **the newest window plus the rows this browser already saw above it**, bounded by
`CACHED_ROWS_FLOOR`. A window of five answers is fewer rows than an earlier open may have read, and an
offline reload opens on the bigger of the two. A page fetched by cursor is still not cached on its own —
that is what the merge is for.

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
`backend/server/handler_message_window_test.go`, `frontend/src/lib/utils/__tests__/chat-window.unit.ts`.
`frontend/tests/e2e/chat-window-scroll.spec.ts` answers what only a browser can: nothing is rendered asking
the reader to fetch a page, reaching the top edge fetches it unasked, and the page leaves the reader on the
row they were reading. `frontend/tests/e2e/chat-scroll-jitter.spec.ts` answers the touch half, which the
wheel spec cannot: it tags every `scrollTop` write so an app write is distinguishable from the browser's
kinetic scroll and anchoring, and asserts the settle leaves a fling alone and a still reader on their row.

## Diffs

`?parsed=1` is a contract every path on `/tasks/{id}/diff` must honour, including the fast ones. The panel
renders `parsed_files` and never re-parses patch text, so `git.ParseUnifiedDiff`
(`backend/git/worktree.go`, served through `git.SortedParsedFiles`) is the only diff grammar in the
product. `GET /api/projects/{id}/git/diff` answers that grammar unconditionally — it has no raw mode and
reads no `parsed` flag — while `/tasks/{id}/diff` still branches on `?parsed=1`, because its manifest and
`TaskDiff` answers carry per-file data rather than patch text. The immutable done-task snapshot stores its
body *as the parsed body* (`doneDiffResponse.ParsedFiles`) and stays a byte-for-byte write: a response
missing that key renders "No changes since last commit" over a diff it still carries. Tests:
`backend/stream/done_snapshot_test.go`, `backend/api/git_diff_parsed_test.go`.

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
  crafted filename a header writer; `inline` is the default the route wants anyway.
- **`CompressMiddleware` passes this response through without buffering it.** The middleware keeps a
  whole GET body to hash an ETag and then compresses it, which for an image costs several times the file
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
  because a batch is a person's selection. `fsOpsReason` is the whole vocabulary: fsutil's own refusals and
  `os.ErrNotExist` are sentences the requester can act on and are passed through; anything else is this
  server's problem, so its cause goes to the log and the caller gets MIKE's sentence, like everywhere else
  (`Errors` below).
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
- The ratchet is the `status500` archgate rule (0 violations across 289 examined).

Tests: `backend/errs/errs_test.go`, `backend/httputil/httputil_test.go`.

## Response headers

Every response carries `Permissions-Policy` (camera/microphone/geolocation/usb/serial/hid/bluetooth/
payment/display-capture/wake-lock/idle-detection/keyboard-map/window-management denied — everything with
no caller in `src/`; the clipboard stays, the terminal pastes with it) **and**
`Cross-Origin-Resource-Policy: same-origin`, except the two trees an opaque-origin frame loads as
subresources: `/extensions/` and `/api/preview/{token}/{path...}` (`isOpaqueOriginAsset`). Marking those
breaks the extension iframe and the preview's own assets.

`CompressMiddleware` puts a strong ETag — a digest of the body — on every GET it buffers, and answers
`If-None-Match` with a 304 that repeats the `ETag` and whatever `Cache-Control` the handler set. A
handler that sets its **own** `ETag` therefore loses the 304: the middleware skips the `If-None-Match`
check when a tag is already there. What a handler decides is the lifetime. The
extension lists decide `Cache-Control: no-cache` — "keep a copy, ask before showing it" — so Settings → Extensions →
Browse can paint from a stored index only after this server agreed it has not moved
(`HandleList`, `HandleRegistryIndex`, `HandleLocalExtensions`). `handleTaskMessages` decides the same
lifetime: reopening a task asks the same question, and a thread that has not moved answers `304` with no
body. An answer holding arbitrary extension
data is not worth storing anywhere, so `GET /api/extensions/{id}/storage` declares no lifetime at all.
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
nothing here is a timeout: inside a bound, a request waits exactly as long as its language server takes.
The same rule holds the bookkeeping *behind* the upgrade — a bound that is reached answers what it
overturns, it never drops it silently.

| Bound | Default | Counts | When it is reached |
|---|---|---|---|
| `stream.DefaultMaxConnections` | 256 | live `/api/stream` sockets | 503 before the upgrade, and a `StatusTryAgainLater` close for the handshakes that arrive during the gap (see below) |
| `lsp.DefaultMaxSessions` | 64 | live `/api/lsp` sessions | 503 before the upgrade |
| `term.DefaultMaxConnections` | 64 | live terminal sockets, across every session | 503 before the upgrade |
| `lsp.DefaultMaxPendingRequests` | 256 | requests one session has waiting for an answer | the oldest is answered with a JSON-RPC error and loses its slot; an answer that does arrive frees its slot first |
| `lsp.DefaultMaxQueuedInitializes` | 64 | `initialize` requests waiting behind the one in flight | the whole queue is failed as a failed send fails it, the overflowing request in the same sentence, so a later `initialize` is forwarded |

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
`/api/lsp` and the terminals pass nil: there a frame is written by whichever goroutine produced it, the
router or the connection's own read loop, each under its own `WriteTimeout`, so the pinger has no lock of
ours to refuse to wait behind. The ping's deadline is built *after* the lock is taken, so `KeepaliveTimeout`
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
