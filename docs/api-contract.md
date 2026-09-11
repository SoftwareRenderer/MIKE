# HTTP and WebSocket contract

The rules are in `AGENTS.md → API`. This is the shape of the frames, the route table's exemptions, and the
error vocabulary.

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

## Provider routing

`PUT /api/config` is a partial update with two required pairs:

- `worker_provider`/`worker_model` — chat; resolved frontend-side by `resolveWorker` in
  `$lib/utils/providers.ts`.
- `utility_provider`/`utility_model` — titles, summaries, commit messages, checkpoint labels;
  `utilityTarget()` in `backend/exec/executor.go`, falling back to the task's own URL/model.

## Partial updates decode into pointer fields

Never a `map[string]any` read behind type assertions. `PUT /api/projects/{projectID}/tasks/{taskID}` uses
`taskUpdateRequest` (`backend/managers/task_update.go`) and `PUT /api/config` uses `configUpdates`
(`backend/config/updates.go`): absent key = leave alone, present key = write (including `""` and
`false`), JSON null = absent. A single-value assertion (`v.(string)`) panics and the recovered panic
reaches the browser as a 500; the `, ok` form silently drops the value and answers 200, so the UI believes
its own save. Wrong types are a 400 naming the field, and validation runs before anything is written.
Tests: `backend/managers/task_update_test.go`, `backend/config/updates_test.go`.

## Route verbs

Every `/api` route declares its verb in the pattern (`s.mux.HandleFunc("POST /api/…")`), so the mux
answers 405 with an `Allow` header before any handler runs, and handlers do not check `r.Method` to
refuse. A new methodless `/api` route is an archgate `routes` violation, so it fails `make check` rather
than being noticed in review. A methodless route also escapes `CSRFMiddleware`, which guards non-GET only.

Exemptions, each with a reason: the two file-server subtrees (`/`, `/extensions/`), the two WebSocket
upgrades (`/api/stream`, `/api/lsp` — they upgrade on GET and never inspect the verb),
`handler_preview.go`'s two `r.Method != http.MethodHead` checks (they suppress a HEAD *body*, not a
request), and `handleTaskMessages` (three verbs through one function).

Two consequences: net/http matches the **most specific path first**, so `GET .../tasks/done` reaches the
`{taskID}` handler and answers 404, not 405; and a `GET` pattern also matches `HEAD`. Tests:
`backend/server/routes_method_test.go`; gate: `scripts/archgate` `routes` rule.

## Diffs

`?parsed=1` is a contract every path on `/tasks/{id}/diff` must honour, including the fast ones. The panel
renders `parsed_files` and never re-parses patch text, so `git.ParseUnifiedDiff`
(`backend/git/worktree.go`, served through `git.SortedParsedFiles`) is the only diff grammar in the
product. The immutable done-task snapshot stores its body *as the parsed body*
(`doneDiffResponse.ParsedFiles`) and stays a byte-for-byte write: a response missing that key renders "No
changes since last commit" over a diff it still carries. Tests:
`backend/stream/done_snapshot_test.go`, `backend/managers/git_diff_parsed_test.go`.

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

## WebSocket upgrades

The terminal WebSocket verifies `Origin` in `httputil.AcceptWS` (`backend/httputil/ws.go`), which every
WebSocket route in this server upgrades through, so the rule is stated once and is not inherited from
`coder/websocket`'s `authenticateOrigin` (that one answers "allowed" the moment the header is empty, so
"the library checks Origin" is really "a browser always sends one"). `SameSite=Lax` blocks the cross-site
request first; this is the second lock on an endpoint that hands out a shell. A request with **no** Origin
is still accepted: only a non-browser client omits it, and that client can set the session cookie by hand
anyway. Test: `backend/httputil/ws_origin_test.go`.
