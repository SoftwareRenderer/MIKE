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

## Frames

- Inbound frames are `stream.AppStreamMessage` (`Data` is `json.RawMessage`). **Outbound** frames are
  flat maps built where they're broadcast (no shared envelope struct in Go) — a `task_event` carries
  `task_id`/`event` beside `data`, so the client reads `msg.task_id`, **not** `msg.data.task_id`.
- A frame that is not a JSON object is dropped and reported once (`parseStreamFrame` in
  `api/helpers.ts`, the one reader of both sockets' `onmessage`). A `JSON.parse` throw inside `onmessage`
  is swallowed by the event loop: the frame is lost, the socket stays open, and the app believes it is
  current. Test: `api/__tests__/ws-frame.unit.ts`.
- Use optimistic UI for user messages (add immediately, skip the echo).
- Model/provider changes ride a `user_message` with optional `model_name`/`llama_server_url`.

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

## Child rows under a task path

A route under `/api/projects/{projectID}/tasks/{taskID}/` names a task, but threads, messages and the
diff cache are keyed by task or message id alone, so the lookup that fetches one never learns which task
asked. Each of these resolves the task in the path first (`store.GetTask(projectID, taskID)`) and, where
the row carries a task of its own, compares its `TaskID` with the path before anything is read or
deleted; `server.messageInTask` is the helper for the message routes, and `HandleTaskDiffFile` settles
the pair before the cache. A handler that hands `{messageID}` straight to `GetTaskMessageByID` answers
for another project's row.

Tests: `backend/server/handler_message_ownership_test.go`, `backend/api/git_diff_file_ownership_test.go`.

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
- The ratchet is the `status500` archgate rule (0 violations across 248 examined).

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
check when a tag is already there. What a handler decides is the lifetime, and the extension lists
decide `Cache-Control: no-cache` — "keep a copy, ask before showing it" — so Settings → Extensions →
Browse can paint from a stored index only after this server agreed it has not moved
(`HandleList`, `HandleRegistryIndex`, `HandleLocalExtensions`). An answer holding arbitrary extension
data is not worth storing anywhere, so `GET /api/extensions/{id}/storage` declares no lifetime at all.
SSE and `GET /api/fs/raw` image bytes are passed through unbuffered and carry no ETag.
Tests: `backend/server/handler_headers_test.go`, `backend/server/compress_test.go`.

## WebSocket upgrades

The terminal WebSocket verifies `Origin` in `httputil.AcceptWS` (`backend/httputil/ws.go`), which every
WebSocket route in this server upgrades through, so the rule is stated once and is not inherited from
`coder/websocket`'s `authenticateOrigin` (that one answers "allowed" the moment the header is empty, so
"the library checks Origin" is really "a browser always sends one"). `SameSite=Lax` blocks the cross-site
request first; this is the second lock on an endpoint that hands out a shell. A request with **no** Origin
is still accepted: only a non-browser client omits it, and that client can set the session cookie by hand
anyway. Test: `backend/httputil/ws_origin_test.go`.
