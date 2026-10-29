# AGENTS.md

This file holds **rules**, in the imperative, one line where one line does it: it is read on every task, so
it holds the decision and nothing else. Mechanism, tables and test lists live in `docs/`; why a rule is this
shape lives in `docs/adr/` (start at `docs/adr/README.md`). Read the subsystem's doc before editing that
subsystem, put new detail in the file that owns it, and argue a choice in a record there rather than here.
When you learn something, *replace* the stale sentence.

## What This Is

A self-hosted kanban of coding tasks. One Go binary serves the API, the board's WebSocket, the SPA, and the
executor that runs an LLM turn per task; each task edits its own git worktree inside a gVisor container
driven through podman, and a person reviews the result in the browser. Everything runs on the operator's
own machine against real repositories.

## Repo Map

- `backend/` — Go server: API handlers, exec loop, LLM client, git/worktrees, sandbox, extensions.
- `frontend/` — Svelte 5 SPA (Vite) built to `frontend/dist`, which `assets.go` embeds. No dev server in
  the normal workflow; see *Workflow* for what a rebuild does and does not need.
- `pkg/bridge/` — sandbox-bridge protocol/client. `pkg/sandboxruntime/` — podman/gVisor runtime.
- `extensions/` — **installed** extensions (`<author>/<name>`), materialized by the installer and deleted
  with the extension. Don't hand-edit. Source is `../MIKE-extensions/`, whose `registry.json` is generated
  — never edit it by hand.
- `Makefile` — build/run/test entry points. `data/` — SQLite DB, TLS certs, trace files. `prompts/` — LLM
  prompt templates, which the binary also carries, and the bundled skill packages. `Dockerfile` +
  `compose.yaml` + `.env.example` — deployment.
- `.env` is the only source of the encryption key, and of any `MIKE_ALLOWED_HOSTS` widening beyond the `localhost` default.

## Non-Negotiables

- **Never commit.** The user commits manually.
- **Never leave the server running** after a crash or panic — stop it first.
- **`make check` is the gate**, and there is no CI file, so it is the only thing that catches a regression.
  Run it before claiming done.
- **`make check-race` on whatever you touched** — a package's own green tests prove nothing about code that
  only runs when a task runs.
- **Scope every Go command**: `./backend/... ./pkg/... .` — never `./...`.
- Run the dev server in **background** (`&`), and restart it after an E2E run.

### Workflow

- **Frontend:** edit `frontend/src/` → `npm run check` → `npm run build` → hard refresh. No server restart:
  a developer's build serves `frontend/dist` from disk.
- **Backend:** edit `backend/` or `pkg/` → `gofmt -w` the touched files → `make build` → restart.
- **Rebuild the committed `sandbox-bridge`** after any edit under `pkg/` — `make bridge-check` fails
  otherwise.
- A `-tags release` binary must run the frontend it was built with; verify packaged artifacts by unpacking them, not by refreshing a browser tab.

## Verify the Tool Output, Not the Expectation

The commonest false "done" is an agent believing a tool call that agreed with what it expected. Re-run the
measuring command after the change and let it make the claim. Keep a visible "not done" list in `TODO.md`;
it is gitignored, so a plan worth keeping belongs in `docs/`.

## Comments: one home, plain words, only what the code cannot say

A comment earns its lines by holding what the code cannot: an **invariant**, a **non-obvious why**, or a
**cross-boundary contract** two languages must keep agreeing.

Delete on sight: history and defence (the commit message owns that story); a rule restated per call site
(write it once, at its definition, and point at it); paraphrase of the code.

Write them flat — no advocacy, no adjectives doing the arguing. A test title names what breaks if the
behaviour regresses, never what the change fixed. A reference to another function is written `Foo()`; the
name a doc comment starts with is its subject, not a reference, and stays bare. Measure the pile with
`git diff HEAD -- <files> | grep '^+' | grep -cE '^\+[[:space:]]*(//|/\*|\*)'`; when it rivals the added
code, the change is arguing with itself.

## Known Gaps — Do Not Trust These

- **A red `./...` is not your regression** until it survives scoping: `go build ./...` and `go vet ./...` may fail due to pulled artifacts in `extensions/`.
- **A green package is not proof** — `docs/testing.md → Go unit test environment`.
- **`deadcode` needs every main as a root** — `scripts/deadcode-check.sh` holds them, and a missing root
  calls live code dead.
- **Some tests fail only under load** — read `docs/testing.md → Tests that fail only under load` before
  believing a red that names them.
- **Auto-summarization and the must-fit gate both need a reported context window** — a provider that reports none gets neither, and an oversized turn reaches it anyway. `docs/context-window.md`.
- **A `WaitDelay` bounds a subprocess's output pipes, not the child**; only a cancelled context bounds the
  child. A long-running subprocess needs both.
- **An LSP extension's `environment` check decides nothing** — no health verdict exists to read.
  `docs/extension-dependency-cache.md`.
- **A translation pack overlays `*.tmpl` prompts, not `prompts/skills/`** — skill bodies stay English.
  `docs/i18n.md`.

## Server Management

```bash
make run    # background dev server, https on the local CA (dev-only key; boot prints a pairing code)
make stop   # pkill the server
curl -sk https://localhost:3000/api/config   # 401 JSON = up; -k because the CA is not in the trust store
```

- **TLS is the only mode**, and a certificate is not the gate: reachability is the listen address plus Host
  pinning. `docs/tls.md`.
- **Never `log.Fatalf` on a `Serve()` error** — run the signal cleanup path, then exit 1.
- **Shutdown is a declared stage list**: register a component's stop step where the component is
  constructed, so construction order is shutdown order. `docs/lifecycle.md`.
- **Every base context is registered with a reason** by the `ctxroot` archgate rule, and the TLS/plain
  first-byte sniff must not run inside `Accept()`. Both: `docs/lifecycle.md`.

## Configuration

- Flags beat env vars beat defaults, and the ladder resolves **once** at startup into an immutable config.
  A consumer receives the decision and never re-reads a variable.
- A malformed value **stops startup and names the variable**, before anything else happens. An empty value
  means unset; an opt-in switch needs exactly `1`.
- Only `main.go` and `backend/startup` read `MIKE_*` — the `env` archgate rule holds that.
- Every variable, the flag twins and the exceptions: **`docs/configuration.md`**.

## Testing

```bash
make check | make lint | make check-race                 # gate / static half / -race over the lock packages
make sandbox-probe                                       # can a gVisor container start on this host?
make test-go | make test-frontend                        # Go unit / vitest + the frontend gates
make test-diff                                           # only the touched Go packages
make test-frontend-diff                                  # only vitest files importing a touched file
make test-e2e [SPECS=chat-messages]                      # e2e lane (mock LLM); needs MIKE_ENCRYPTION_KEY
make test-e2e-diff                                       # only the e2e specs this tree touched
cd frontend && npm run check:dead-surface                # untagged test-only exports; duplicated-CSS count
cd frontend && npm run check:e2e-ownership               # a spec that starts a turn owns its project
python3 scripts/gen-registry.py --check                  # registry.json matches the manifests and pins
make test-gen-registry                                   # the generator's own tests, both directions
```

- **A spec exists only if it needs a browser or the real backend.** Component state belongs in vitest;
  never write a `readFileSync` + `toContain` "test".
- **A spec skips only for a prerequisite global setup cannot provide**, and says which in the skip reason.
- **A test runs only where its container can start** — `sandboxcheck.Require(t, image)` in Go,
  `requireSandbox()` in e2e. A host whose podman storage cannot initialise skips with that reason; it does
  not fail. `docs/testing.md → Go unit test environment`.
- **Config-modifying specs run in the `isolated` project**, and **a spec that runs an executor turn owns its
  project and releases its tasks when the test ends** — an unreleased task closes the admission gate on the
  tests after it.
- **Read a turn from the store by row id, never by a length or a status.** `tests/e2e/helpers.ts` owns the
  rules: `messageRows`, `sendTurn`, `notInFlight`, `runHasEnded`.
- **`[role="dialog"]` is never a unique locator** — filter by content.
- **Go tests are deterministic**: `testServer(t)`, `t.TempDir()`, no network.
- A frontend test file importing Svelte compiler macros **must** be `.svelte.ts`; plain `.ts` does not see
  them.
- The e2e harness leaves nothing behind: verify processes, ports and `/tmp` after touching setup.
- Environment prerequisites, the port table, teardown and the load-sensitive tests: **`docs/testing.md`**.

## API

- The frontend talks **only** to the Go server; all LLM traffic is proxied, and realtime is WebSocket
  through Go.
- Inbound frames are typed; outbound frames are flat maps built where they are broadcast — read
  `msg.task_id`, not `msg.data.task_id`.
- **One goroutine per app-stream connection writes its socket** — every delivery path queues into it. A slow
  browser degrades to a reconnect, never to a producer that waits for it.
- **Every `/api` route declares its verb in the pattern** — handlers don't check `r.Method`, and a
  methodless route also escapes the CSRF middleware.
- **A partial update decodes into pointer fields**, never a `map[string]any` behind type assertions.
- **An archive is listed by the server and never unpacked** — the bytes decide the format, and a member is
  read by exactly the name its row shows. `docs/api-contract.md → Archive listing`.
- **`?parsed=1` is a contract every diff path must honour**; `git.ParseUnifiedDiff` is the only diff grammar.
- **A server fault answers with MIKE's own message and the cause goes to the log.** `httputil.Fail` is the
  only place a status code is chosen.
- Frame shapes, route exemptions, headers, the error vocabulary and endpoint ownership:
  **`docs/api-contract.md`**.

## Frontend

- **Everything the app did not write goes through `$lib/utils/sanitize`** — including LSP hover, completion
  and signature documentation. `html-sinks.py` lists every sink and fails if its own list misses one;
  `docs/markup-sanitizing.md`.
- **Formatting follows the active locale; sorting never does.** Never cache a locale-derived value at module
  scope. Phrases are catalog keys in `$lib/i18n/en.ts`, never literals. `docs/i18n.md`.
- **All client-side storage goes through `frontend/src/lib/storage.ts`.** Never touch IndexedDB from a
  component; sign-out runs `clearQueue()` then `clearAll()`, in that order. `docs/reconnection.md → Signing
  out empties both halves`.
- **Svelte 5:** `$state`/`$derived`/`$derived.by`/`$props`, and `get` from `svelte/store` for the Svelte 4
  stores still in use — no version bumpers, no `$effect` mirrors. Reactive services live in
  `$lib/services/`, helpers in `$lib/composables/`, pure functions in `$lib/utils/`.
- **Keyboard navigation moves real DOM focus** — targets come from pure resolvers, never a parallel
  "highlighted" store. `docs/navigation.md`.
- **Project rows and task cards are real links**; never fake the context menu. `docs/link-rows.md`.
- **Adding a project navigates to its board**, and a clone's row is on screen while the clone runs — a
  placeholder address is never a Back entry. `docs/link-rows.md → A project the app just made`.
- **Shortcuts and UI preferences ride the shared Settings Save/Cancel flow**, and `PUT /api/config` replaces
  the whole `keyboard_shortcuts` object, so always send every binding. `docs/adding-a-preference.md`.
- **CJK typography is scoped to `:lang()`.** `docs/typography.md`.
- **A form reflows to its own width, never the window's** — a size container decides when an add-row
  stacks into one field per line, and every control below that width is 44px. The 768px `isMobile`
  breakpoint is for app chrome, not a row's arithmetic.
  `frontend/src/lib/styles/sandbox-config.css` holds the arithmetic, and the phone block of
  `frontend/tests/e2e/sandbox-mounts.spec.ts` is what fails when it stops holding.
- Chat never disguises a system message as a user message, shows the streaming cursor only during an active
  response, and formats stats as `X tokens · Ys · Z tok/s`.
- **Chat autoscroll is judged from the scroll event, never from a gesture listener**, and new content
  re-pins only while the reader is being followed. `frontend/src/lib/composables/use-chat-follow.svelte.ts`,
  `docs/api-contract.md → The conversation is a window, not the thread`.
- **A Review notification is the client's decision, never the server's** — the choice lives in the browser
  that shows it (`uiStore.notifyOnReview`), never in `/api/config`. `docs/notifications.md`.
- **A card lists its subtasks as flat rows in run order, never as nested card boxes.** One pure function
  owns the order and it mirrors `exec`'s schedule. `docs/subtask-outline.md`.
- **A branch another worktree holds is never a checkout option, and the row naming one opens its task.** The
  screen decides from the holder the route reports, never from the ref's name.
  `docs/api-contract.md → The branch listing names its holder`.
- Diff text is parsed once in `$lib/utils/diff-parser.ts`, and its badges are the diff badges; git status
  entries keep their own.
- **A diff row's gutter number is not its identity** — one column numbers a removed row from the old file and
  every other row from the new one. Identify a row by its text; resolve a click through `jumpTargets`.
  `docs/api-contract.md → Diffs`. Sticky section headers use `position: sticky` over an opaque background.

## Storage

- SQLite, one connection, one serialisation point. **A multi-statement write that must land entirely runs in
  `Store.tx`**; anything outside `tx` states its reason at its definition.
- **`ON DELETE CASCADE` deletes rows, nothing else** — containers, worktrees, branches and uploads are
  stopped by the deletion path, not by the schema. See *Deletion*.
- **Structure is completed by shape, data by a stamped step** — `applySchema` adds what a file lacks and
  nothing else; a rename, a rewrite or a meaning change is a keyed `schemaStep` recorded in `schema_meta`,
  never an edit to the DDL alone. `docs/storage.md → The marker, and the step ladder beside the shape pass`.
- **`PRAGMA user_version` is read and never written** — 1..12 there marks a pre-release file, which the boot
  says out loud. A file carrying a one-way step this build does not have refuses the boot naming the step.
- **A boot that changed a database logs what it changed**, and nothing else announces it.
  `docs/storage.md → Every upgrade is logged`.
- **An instant that SQL compares is stored as fixed-width UTC text.** `docs/storage.md`.
- A test that needs a write to fail halfway installs a SQLite `RAISE(ABORT)` trigger, never a hook in
  production code.
- What is inside `tx`, the DDL, timestamps, permissions, space reclamation and ID generation:
  **`docs/storage.md`**.

## Sessions & Auth

Passkeys, not a password. There is no login password anywhere, and a route that opens a session on any other
proof is a regression.

- **A pairing code authorizes one registration and authenticates nothing**; a session exists only as the
  answer to a finished ceremony.
- **The rpID and origin are the request's**, so one install holds a different credential set per hostname,
  and a bare IP address is refused as a relying party.
- **`--reset-passkeys` is the only way out of a lost device**: it deletes passkeys *and* sessions, and
  refuses a data directory a running server holds.
- **Session tokens come from `crypto/rand` and are stored hashed**; the cookie's `Secure` flag is decided
  per request, and logout clears with the flag it set.
- **`/api/sessions*` are session-authed, CSRF-guarded, never public**, and their responses carry no token.
- **A bearer token is a second credential with a narrower scope**: it authenticates only the routes in
  `tokenScope` (`backend/server/token_auth.go`), never falls back to a session cookie, and cannot mint or
  revoke tokens.
- Ceremonies, pairing codes and the reset: **`docs/passkeys.md`**. Storage, endpoints and the Settings UI:
  **`docs/sessions.md`**. Issuance and the route allowlist: **`docs/api.md`**.

## Sandboxing

gVisor (`runsc`) is required: task tool calls and terminal sessions run in gVisor containers, rootless, via
podman. Without it, sandboxed projects refuse to start tasks — hard error, no fallback.

- **Where a task runs is one column, `project.sandbox_enabled`, and it is not re-checked.** A sandboxed task
  with a missing bridge gets an error, never a host fallback — that refusal is the whole boundary.
  `DANGER_ZONE=1` permits *disabling* sandboxing; it never gates execution.
- **Boot decisions are one injected value** whose zero value **declines** the host-wide stale-container
  sweep, so a test binary cannot delete a live server's containers.
- **The container environment is only what was configured**, from one builder. Invent no defaults.
- **Container arguments come from one package** — `pkg/sandboxruntime` owns validation and the only way to
  add a capability.
- **A task container's git pointer is pinned read-only, and a task container that cannot pin it is refused.**
  `docs/sandbox-runtime.md → A task worktree's git pointer is pinned read-only`
- **Extension sandbox mounts are an allowlist, and borrowed roots are read-only** however the manifest asks:
  a manifest is text from the registry, not a grant from a person.
- **A capability is judged by its shape, never its name** — `--privileged` is an argument, not a
  capability, and it is refused. `docs/sandbox-runtime.md → Capabilities are judged by shape, not name`
- **The bridge is stdio-only** — no listener and no port. Interactive terminals bypass it.
- **A project's own skills reach its containers read-only** — the bind is that project's `.agents/skills/`
  directory and nothing wider, and the prompt lists skills by the path the container can open.
  `docs/skills.md`.
- **Plan mode's bash restriction is prompt text, not executor-enforced.** What holds a planning turn is the
  read-only worktree mount plus the sandbox — don't add a fake allowlist.
- Bridge invariants: **`docs/sandbox-bridge.md`**. Boot, the sweep, environment construction, capabilities
  and podman subprocess rules: **`docs/sandbox-runtime.md`**.

## Confinement (host side)

Enforced by the server process, so it holds for non-sandboxed projects too.

- **File endpoints are jailed to the project plus the task the request names** — never the workspace dir —
  and containment checks the lexical **and** the symlink-resolved path.
- **A jailed read opens the file the check named**, not the name — `fsutil.OpenVerified` — because whoever
  owns the directory can move other bytes under that name in the gap.
- **The no-project file flow is jailed to `$HOME` plus the declared directories**, decided once at startup
  and injected.
- **Two host reads sit outside the tool path** (LSP document sync, a project's Dockerfile) and are jailed
  where the path is accepted.
- **Nothing is read or captured unbounded**, and every limit is applied before the bytes are read.
- **No subprocess inherits the server environment.** Exactly three allowlists, one per family (git, podman,
  agent). `os.Environ()` survives in one sanctioned place: the operator's own host terminal under
  `DANGER_ZONE=1`.
- **The git author identity is accessor-only**, process-wide, and stamped at boot from git's own
  `GIT_COMMITTER_*`/`GIT_AUTHOR_*` variables — a commit path appends `git.GitAuthorFlags()`.
- **The release check is the only place something downloaded from the network becomes running code**, so it
  is opt-in, never self-applying, and it refuses an install that is not an unpacked release directory.
  **`docs/auto-update.md`**.
- Mechanisms, helpers and tests: **`docs/confinement.md`**.

## Concurrency

- **Keyed locks come from `pkg/keylock`, one named domain type per domain**, embedded by value: task IDs and
  project IDs share an alphabet, so a shared table merges two domains silently.
- **Lock tables never leave their package** — accessors return a release function, never a mutex.
- **Lock order: merge before stash, never after.** Never hold a mutex across a `Wait` for a goroutine that
  takes it, and a goroutine that wins a lock after its caller timed out must release it.
- **Do not add a seconds-scale bound to a git-family lock** — the wait is usually legitimate; wedged sections
  announce themselves through a slow-wait report.
- `executionState.sandboxMu` is a **leaf lock**: taken under `taskMu`, never the reverse.
- Domain names, timeouts and watched-wait wiring: **`docs/locking.md`**.

## Context Window

- **Judge the request about to be sent** — the provider's own count for the conversation, plus the tool
  results appended since, which it never saw. Usage on its own describes a call that finished.
  `docs/context-window.md`.
- **A word count prices nothing the provider counts** — the system prompt, the tool definitions and its
  tokenizer are in its number, so where a provider reported one, that number is the measure and the estimate
  is the fallback.
- **A crossing is answered by its cause**: a conversation already past the budget is summarized, tool results
  that do not fit are trimmed. Never the reverse — a summary drops everything older than its marker, which
  is the read the model just asked for.
- **Trimming lands on the copy a call is built from** — the stored tool row, the reviewer's card and the
  in-memory conversation keep every byte, so the cut is re-derived on every send.
- **Every assistant `tool_call` keeps its tool message** — content is ours to cut, the row is not.

## Git

- **`git.Command`/`git.CommandContext` are the only way this repo starts git** — the execution fence cannot
  be forged from a call site.
- **The execution fence pins every setting that names a program by key**; a program the repository picks
  *by name* has no key to pin, which is what the read-only git pointer stops.
- **A stash entry is identified by hash, never by position** — the stack is shared by every worktree and
  holds the user's own entries. Work needing a clean tree goes through `WithStash`.
- **Client-supplied refs are validated and fenced, clones speak only allowed transports, and a stored
  `commit_hash` is an object id** validated where it is accepted.
- **One slugifier** for every name derived from user text, with its matching parser beside it.
- Fences, the stash bracket and the accepted consequences: **`docs/git-operations.md`**.

## Subtask Schedule

- **A paused row is a stop, not a schedule decision**: the scheduler starts nothing inside a paused branch
  and never restarts a paused row on its own. `backend/exec/pause.go` owns the ordering this depends on.
- **A task with unfinished children never runs its own turn**; for a click that decision has one home,
  `startUnderSchedule` (`backend/api/task.go`), and it starts a child through `StartSubtask` — the only
  starter that inherits the parent's model config.
- Which stops get answered from the parent's context: **`docs/auto-answer.md`**. The row order a card draws:
  **`docs/subtask-outline.md`**.

## Commit and Done

- **Only staged changes are committed**; untracked files stay in the worktree and are discarded with the
  task. A conflict-resolution commit stages with `git add -u`, **not** `git add -A`.
- **A merge conflict interrupts the click, it does not cancel it** — only the recorded click
  (`ArmCommitAndDone`) authorizes that merge.
- **Nothing is marked Done that did not reach the target branch.**
- A run the user did not start is not left waiting in Review on a text-only turn: the parent's context
  answers it, as it answers `ask_question`. **`docs/auto-answer.md`**.

## Deletion

- **Deleting a task is one path** — deleted events, every teardown step for its subtree, one prune and one
  board broadcast. No caller states a subset of those steps.
- **Deleting a project deletes its tasks**, roots first, then its container, terminal session and worktree
  directory: the startup sweep skips running containers, so nothing else removes them.
- **Cleanup failures are reported, not fatal**, and the leftover is logged by name.

## Extensions

Types: `lsp`, `ui`, `css`, `i18n`, `agent`, `llm`, `tool`.

- **An installed extension's directory *is* the install**: deleting the row deletes the directory, and both
  directions come from the stored record, never a request field.
- **Install is atomic and answers as a stream**; the image is built before the row exists and a failure
  removes the checkout.
- **An extension sandbox has no default image**, and an image is built in a context this package creates —
  never the working directory.
- **An extension's only egress is the proxy route**, which enforces the manifest's domain allowlist, refuses
  redirects and caps the response. Never build an `http.Client` at a call site.
- **A dependency-cache grant is two fields with two origins** and is always read-only.
  `docs/extension-dependency-cache.md`.
- **A contributed tool is data, and its address is a grant**: the only origin it may contact is the one
  stored in a `config_schema` property of type `"url"`. These tools run in the server process, so a project's
  network isolation does not cover them. `docs/extension-tools.md`.
- **Language knowledge lives in extensions** — the core holds no language names, keyword lists or bracket
  tables, in either language. `docs/languages.md`.
- **The UI language is whichever `i18n` extension is enabled**; any load failure means whole-catalog English
  plus a report. `docs/i18n.md`.
- **Agent extensions:** the backend knows nothing about agent protocols, and `EventTypePersist` is the only
  path to the database. `docs/agent-extensions.md`.
- **UI extension modules are plain JS, not Svelte components**, and the postMessage channel is armed by the
  frame's `exthello` echo. An extension entry is a public document, so its response carries a `sandbox` CSP.
  `docs/ui-extensions.md`, `docs/preview.md`.
- Install/build/delete/repair mechanics and the REST surface: **`docs/extension-lifecycle.md`**.

## MCP

- **Speak one era: protocol `2026-07-28`, over Streamable HTTP.** A server answering another version is
  refused by name, never negotiated down to.
- **Decline every capability out loud** — pass an explicitly empty `ClientCapabilities`; setting no handler
  is not a decline.
- **Never bridge `elicitation/create` to `ask_question`**, and never park a turn on a person waiting for a
  socket.
- **A project opts in per server, and the offer and the lookup read one path**, so a withheld name is
  `Unknown tool` at dispatch. `mcp_` is a reserved prefix.
- **One attempt, and only the session's context bounds a wait** — a tool is not idempotent, so nothing
  retries.
- **The address a person typed is the only widening**, and it still goes through the egress client.
- **The operator can freeze the registry** (`MIKE_RESTRICT_MCP=1`).
- **A stored header is spent only on the host the row that stores it names**, and never echoed back.
- Mechanics, the caps and the elicitation argument: **`docs/mcp.md`**.

## Reconnection & Offline

The client distinguishes a cancellation from a lost connection and treats neither as a deadline: an abort
never marks the app offline, and nothing in the product puts a deadline on a wait for this server.

- **A write queues while offline, and so does a write that got no answer** — `writeOrQueue` decides that at
  the api layer. A request carrying a plaintext secret is refused rather than queued, because the queue is
  persisted.
- **An unsaved file draft carries the bytes it diverged from**, so a file that moved under it is merged on the
  way back in. A region both sides changed is painted into the buffer for the person to resolve, never
  dropped. `docs/reconnection.md → The Editor Buffer Across a Browser Being Away`.
- **A task's status is never the answer to "is it still running"** — `taskIsInFlight` is, and the
  server-side mirror `terminalTurnStates` must agree with it.
- **A screen that cannot order its own reading of a task against the board's copy asks the server** — a turn
  that starts and ends between the two readings leaves the copy on the value it held. Left to the rule alone
  the screen says "Agent is responding" over a finished task, with nothing left to tell it.
  `docs/reconnection.md → A row reading the board's copy cannot be ordered against`.
- **The offline mirror holds only what this browser received**, and a mirror landing paints a view that
  holds nothing rather than replacing one that holds something.
- **The offline screen answers from the mirror** — its projects, board and config come out of IndexedDB with
  no request in front of them, and the loading screen stays up over that read.
- Mechanisms: **`docs/reconnection.md`**.

## Licensing

Apache-2.0, and a dependency's license is not changed by ours.

- **No GPL/AGPL dependencies**; MPL-2.0 is fine (don't modify MPL-2.0 files); an Apache-2.0 dependency with a
  NOTICE requires its content in ours. Never remove a dependency's header or NOTICE. Build-only
  dependencies stay out of the distributed artifact.
- The rule is about what MIKE **links**: the programs the runtime image installs (podman, crun, runsc, git)
  are separate executables, not dependencies.
- Regenerate the notices whenever a dependency changes: `go mod tidy`, then
  `go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./backend/... ./pkg/... . | sort -u`, then
  `npx license-checker --production --summary`, then update `THIRD_PARTY_NOTICES.md` and `NOTICE`. Both
  commands over-claim; list only what is compiled or bundled into the shipped binaries.
- Each extension carries its own `LICENSE`, `NOTICE` and `THIRD_PARTY_NOTICES.md`.

## Debugging

Logs go to server stdout or `/tmp/kanban.log` and are greppable by `component=` and `task_id=`. Worktrees
live under `.worktrees/`. A stale frontend is a rebuild plus a hard refresh. E2E connection failures are
stale processes on 3000/3001/9999 — restart the server with `make run`. The flight recorder is always on:
`go tool trace -http=:8080 data/kanban.trace`. Ad-hoc probes: `docs/testing.md`.

### Logging

- Every diagnostic goes through `pkg/logging`; a `log.Printf` in non-test code is a regression.
- `component` comes from the fixed vocabulary in `logging.Components`, and `task_id`/`project_id` are
  attributes lifted with `With` — never text inside a sentence.
- Level policy: `Error` when the fault reaches a person, `Warn` when the run continued, `Info` for a state
  change worth keeping, `Debug` for chatter. Don't raise a line to `Error` because it says "failed".
- The `Infof`/`Warnf`/`Errorf`/`Debugf` forms are printf wrappers so `go vet` checks every call site;
  `logging.Info` takes no format.
- Tests capture with `logging.TestSink()`, never `log.SetOutput` — that idiom captures nothing.

## Writing a Doc

`AGENTS.md` holds the rule, `docs/<subsystem>.md` holds the guideline, `docs/adr/` holds the decision and its
proof. Put new detail in the file that owns it, and name the one command that fails when the doc stops being
true. **`docs/documentation.md → What a doc owes the reader`**.

## Docs Index

Read the file before editing that subsystem — each holds the mechanism, history and tests this file
deliberately omits.

| File | Subsystem |
|---|---|
| `docs/adr/README.md` | what belongs in a decision record, its shape, and the index of the records that exist |
| `docs/configuration.md` | every env var, the flag twins, startup resolution, the archgate exceptions, the release packages |
| `docs/docker.md` | the three build stages, the gVisor bundle and its sidecar tree, the flags and volumes a container needs, first boot, the limits an operator meets |
| `docs/sandbox-runtime.md` | boot injection, the stale sweep, container environment, podman args, capabilities, mount policy, the git pointer pin, memory caps and admission, podman subprocesses |
| `docs/sandbox-bridge.md` | bridge connections, LSP session binding, wire IDs, payload limits, upload lifetime |
| `docs/skills.md` | the two skill roots a task is listed from, the paths it is told to open, same-named packages |
| `docs/confinement.md` | path jails, the no-project host jail, size and output caps, subprocess environments |
| `docs/locking.md` | keylock domains, accessor shape, lock order, watched waits, `sandboxMu` |
| `docs/context-window.md` | the prompt budget, what each trigger measures, the cause rule, the trim floor and its notice, an unreported window |
| `docs/storage.md` | what is in `tx`, the schema and its DDL, stored timestamps, file permissions, ID generation |
| `docs/git-operations.md` | command construction and execution fence, worktree layout, stash identity, ref/clone/hash fences, slugify |
| `docs/lifecycle.md` | construction order = shutdown order, the base-context registry, the TLS listener |
| `docs/api-contract.md` | frame shapes, route verbs, partial updates, diff contract, error vocabulary, headers, endpoint ownership |
| `docs/lsp-transport.md` | the `/api/lsp` socket's bounds, one ordered writer per workspace, exactly-once answering, the browser's wait ceilings |
| `docs/markup-sanitizing.md` | sanitizer deltas from DOMPurify's defaults, the sink audit, `Icon.svelte` |
| `docs/extension-lifecycle.md` | install/build/delete/repair, SSE ordering, egress, capability gates, changelogs |
| `docs/extension-tools.md` | contributed-tool grammar, the `url` grant and its loopback exception, the pre-save probe |
| `docs/mcp.md` | the pinned era and who enforces it, the caps, the declined capabilities, the elicitation rule, the Test action |
| `docs/extension-dependency-cache.md` | the `dependency_cache` grant, `ro` mounts, `environment` check |
| `docs/typography.md` | CJK font chains and how to verify them with pixels |
| `docs/testing.md` | test environment, teardown, e2e conventions, mock-LLM fixtures, load-sensitive tests |
| `docs/tls.md` | certs/SANs, distribution, proxy + `Host`, weak-password and cookie rules |
| `docs/sessions.md` | session storage shape, `/api/sessions*` responses, Settings → About UI |
| `docs/passkeys.md` | the two ceremonies, pairing codes, ceremony tickets, `--reset-passkeys`, the boot pairing banner |
| `docs/api.md` | the bearer-token surface: provisioning, the route allowlist, what each route does |
| `docs/threads-checkpoints.md` | branch vs snapshot layers, checkpoint pinning/restore, fork inheritance |
| `docs/commit-and-done.md` | what the click does, conflict arming, which files never get committed |
| `docs/auto-answer.md` | `Autostarted` provenance, which stops are answered from the parent, the advisory row |
| `docs/languages.md` | `language` vs `language_id`, `syntax` as data, identifier shapes |
| `docs/i18n.md` | the derived-language rule, catalog load and failure policy, pre-paint cache, locale offer |
| `docs/ui-extensions.md` | `/extensions` serving + CSP, channel handshake, install model |
| `docs/preview.md` | preview routes, ref rewriting, nav record, iframe isolation |
| `docs/agent-extensions.md` | adapter interface + events, WASM memory model, new-adapter recipe |
| `docs/navigation.md` | board + task-view arrow-key resolution |
| `docs/notifications.md` | the Review-arrival rule, the counts ledger, the permission policy |
| `docs/subtask-outline.md` | subtask row order and shape, the wave rule, the row's two lines, the row badge rule |
| `docs/link-rows.md` | project-row/task-card markup, click routing, known limitation |
| `docs/reconnection.md` | WS reconnect/offline queue mechanism |
| `docs/llm-streaming.md` | provider silence and what measures it, the per-provider ceiling, retrying a quiet provider, keeping the socket honest, the model's sampler overrides and what a blank one sends |
| `docs/frontend-updates.md` | stale-asset detection, the build-id tag contract, prompt-mode worker, the day gate |
| `docs/auto-update.md` | the release manifest, the opt-in ladder, the install shapes that refuse, staging and restart |
| `docs/adding-a-preference.md` | the recipes for a shortcut or a `*bool` preference |
| `docs/documentation.md` | what a doc is for, the four questions it answers, the nine rules of the docs gate, the long-sentence budget |
