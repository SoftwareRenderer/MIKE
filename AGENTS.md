# AGENTS.md

This file holds **rules**. Mechanism, tables and test lists live in `docs/`; **why a rule is this shape** lives in
`docs/adr/` — one numbered file per decision, written when the choice is made, never afterwards (ADR-0001). Start
from the index `docs/adr/README.md`: rule files and subsystem docs do not link the records behind them. Read the
pointer before editing that subsystem, put new detail in the file that owns it, and defend a choice by opening a
record there rather than arguing inside the rule. When you learn something, *replace* the stale sentence.

## Repo Map

- `backend/` — Go server: API handlers, exec loop, LLM client, git/worktrees, sandbox, extensions.
- `frontend/` — Svelte 5 SPA (Vite), built to `frontend/dist/`, which `go:embed` (`assets.go`) compiles
  into the server binary. A `-tags release` build (`make package`, `make release`, the image) serves that
  copy and no other; a developer's build prefers `frontend/dist` on disk, so `npm run build` needs no Go
  rebuild. No dev server in the normal workflow.
- `pkg/bridge/` — sandbox-bridge protocol/client. `pkg/sandboxruntime/` — podman/gVisor runtime, bridge
  binary discovery.
- `extensions/` — **installed** extensions (`<author>/<name>`), materialized by the installer and
  **deleted with the extension**. Don't hand-edit unless asked for a custom extension. Source is
  `../MIKE-extensions/`, whose `registry.json` is **generated** (`scripts/gen-registry.py`; `--check`
  verifies) — never edit it by hand.
- `Makefile` — build/run/test entry points. `data/` — SQLite DB, TLS certs, trace files. `prompts/` — LLM
  prompt templates.
- `Dockerfile` + `compose.yaml` + `.env.example` — deployment. The image carries podman and `runsc` and drives
  them nested (`privileged`), so neither is a host prerequisite there. There is no `README.md` in this tree:
  `scripts/package-release.sh` writes one into the package, and its "Running it" section owns the run/deploy
  story. `.env` is the only source of the master password, encryption key and `MIKE_ALLOWED_HOSTS`.

## Non-Negotiables

- **Never commit.** The user commits manually.
- **Never leave the server running** after a crash or panic — stop it first.
- **`make check` is the gate** and there is no CI file, so it is the only thing that catches a
  regression: `lint` (gofmt → vet → staticcheck → deadcode, every root) → `archgate` + `test-archgate`
  (structural debt against its frozen baselines) → `docs-check` + `test-docs-check` (the same, for docs) →
  `bridge-check` → build → `check-release` → the Go suite → the frontend gates, type-check included. Run it
  before claiming done, and `make check-race` on whatever you touched — a package's own green tests prove
  nothing about code that only runs when a task does. `RACE_PKGS` in the Makefile names the packages,
  and `docs/testing.md` names the ones it omits. `make lint` is the static half alone.
- **E2E runs kill your dev server** (global-setup owns ports 3000/3001/9999) — restart with `make run`
  afterwards.
- Run the dev server in **background** (`&`).

### Workflow

- **Frontend:** edit `frontend/src/` → `npm run check` (svelte-check; stay TS-clean; the gate runs the same
  pass stricter as `check:types`) → `npm run build` →
  hard refresh. No server restart: a developer's build serves `frontend/dist` from disk (`MIKE_FRONTEND_DIST`
  moves it, and the startup log names which copy is being served). A `-tags release` binary cannot be shown
  a build it was not compiled from — prove a packaged artifact by unpacking it, never by refreshing a tab.
- **Backend:** edit `backend/`/`pkg/` → `gofmt -w` the touched files → `go build -trimpath -o mike .`
  (or `make build`) → restart. Config persists in SQLite.

## Verify the tool output, not the expectation

The commonest false "done" here is an agent believing a tool call that agreed with what it expected.
Re-run the measuring command after the change and let the command make the claim: the `grep` that still
counts matches, the script that rewrote two files of five, the file believed read that is not on disk.
Keep a visible "not done" list in `TODO.md` — note it is gitignored, so a plan worth keeping belongs in
`docs/`.

## Comments: one home, plain words, only what the code cannot say

A comment earns its lines by holding something the code cannot: an **invariant**, a **non-obvious why**,
or a **cross-boundary contract** (a rule one language owns and another mirrors, which must keep agreeing
case for case). Delete on sight:

- **History and defence** — what the code used to do, which input used to be refused, why the new
  behaviour is reasonable. The commit message and `TODO.md` own that story; in source it can only go
  stale, and stale comments get believed.
- **A rule restated per call site** — write it once, at its definition; every other site gets one line
  pointing there.
- **Paraphrase of the code** — if the next line says it, the comment says nothing.

Write them flat: no advocacy, no adjectives doing the arguing, no narrative longer than the rule. A test
title names what breaks if the behaviour regresses, never what the change fixed. Measure the pile with
`git diff HEAD -- <files> | grep '^+' | grep -cE '^\+[[:space:]]*(//|/\*|\*)'`; when it rivals the added
code, the change is arguing with itself.

## Known gaps — do not trust these

- **Scope every Go command**: `./backend/... ./pkg/... .`. `go build ./...` and `go vet ./...` fail at
  HEAD on the installed extensions under `extensions/`, which are pulled artifacts. A red `./...` is not
  your regression until you've seen it survive the scoping.
- **A green package is not proof.** `backend/exec`'s tests never start a task, so `executionState` locking
  bugs only surface under `-race` in `./backend/server/`.
- **`deadcode` needs every main as a root** — `. `, `./pkg/bridge/agent`, and the two `scripts/` mains. A
  missing root calls live bridge code dead. A method whose type satisfies some used interface counts
  as live even when nothing selects it. Such a method never appears in the ALLOWLIST, which holds only
  what the tool reports, so nothing here measures an exported method no caller calls.
- **Some tests fail only under load.** Read `docs/testing.md → Tests that fail only under load` before
  believing a red that names them.
- **A `WaitDelay` bounds a subprocess's output pipes, not the child; only a cancelled context bounds the
  child.** Every git and podman subprocess comes from a constructor that sets one. A context exists only
  where a caller has something worth cancelling (clone, pull/push, extension install); local git calls are
  deliberately context-less. If you add a long-running subprocess, give it both.
- **An LSP extension's `environment` check decides nothing** — core stores and validates the declaration and
  runs no check, so no health verdict exists anywhere to read. `docs/extension-dependency-cache.md`.
- **A translation pack overlays `*.tmpl` prompt templates, not `prompts/skills/`** — skill bodies stay
  English whatever pack is enabled (`docs/i18n.md`).

## Server Management

```bash
make run    # background dev server, https on the local CA (dev-only encryption key; the boot prints a pairing code)
make stop   # pkill the server
curl -sk https://localhost:3000/api/config   # 401 JSON = up; -k because the CA is not in the trust store
```

- **TLS is the only mode**: a local CA in `data/tls/` signs a server leaf, and a plain connection to the port
  gets a 301. The leaf is re-issued when the host's names change or within a month of its expiry. The CA is
  reused while usable and replaced only when it is not — corrupt, expired, not a CA, key gone — and that
  replacement logs an `Error`, because every device that imported the old root stops trusting the server.
  A certificate/key pair that cannot be loaded stops startup naming the flag; it is never discovered mid-handshake.
  **A certificate is not the gate**: reachability is the listen address plus Host
  pinning. The CA downloads without a session, because the browser that needs it is the one that has not
  paired. `docs/tls.md`.
- **Never `log.Fatalf` on a `Serve()` error** — run the signal cleanup path, then exit 1.
- **Shutdown is a declared stage list.** A component registers its stop step with `s.reg(stage, name, step)`
  *where it is constructed* (the stages are declared in `backend/server/lifecycle.go`), so construction order
  is shutdown order. Duplicate or nil steps are refused at registration, and the first stage cancels the
  server's base context. SIGTERM must end the process: a wedged shutdown keeps the data directory open, so
  the next run boots on the previous run's database. **`docs/lifecycle.md`**.
- **Every base context in the tree is registered with a reason** (`root`, `detached`, `legacy`) by the
  `ctxroot` archgate rule; `backend/server/sideeffects.go` states the same classification for HTTP
  handlers. `docs/lifecycle.md`.
- **The TLS/plain first-byte sniff must not run inside `Accept()`** — a queue and a per-connection reader
  hold it instead. **`docs/lifecycle.md`**.

## Configuration

- Flags take priority over env vars, which take priority over defaults, and the whole ladder resolves
  **once** in `backend/startup` into an immutable `startup.Config`. A consumer receives the decision; it
  never re-reads a variable where it is used.
- A malformed value **stops startup and names the variable**, before anything else happens. A variable set
  to empty means unset. An opt-in switch needs exactly `1`; any other value warns.
- Only `main.go` and `backend/startup` read `MIKE_*`; the `env` archgate rule holds that.
- Every variable, the flag twins, and the exceptions: **`docs/configuration.md`**.

## Testing

```bash
make check | make lint | make check-race                 # gate / static half / -race over the lock packages
make test-go | make test-frontend                        # Go unit / vitest + the frontend gates
make test-diff                                           # only the touched Go packages, cache left on
make test-frontend-diff                                  # only the vitest files importing a touched frontend file
make test-e2e [SPECS=chat-messages]                      # e2e lane (mock LLM): builds the frontend, needs MIKE_ENCRYPTION_KEY
make test-e2e-diff                                       # only the e2e spec files this working tree touched
cd frontend && npm run check:dead-surface                # untagged test-only exports; the duplicated-CSS count
cd frontend && npm run check:e2e-ownership                # a spec that starts a turn owns its project
python3 scripts/gen-registry.py --check                  # registry.json matches the manifests
```

- **A spec exists only if it needs a browser or the real backend.** Component state belongs in vitest.
  Never write a `readFileSync('../Foo.svelte')` + `toContain(...)` "test".
- **A spec skips only for a prerequisite global setup cannot provide** (gVisor is the one left) and says
  which in the skip reason; "no worktree appeared" and "the registry did not answer" are failures, not skips.
  A spec that can only run in a mode this suite never runs is deleted, not left green-but-skipped.
- **Config-modifying specs run in the `isolated` project** (`ISOLATED_SPECS`), and **any spec that runs an
  executor turn owns its project and releases its tasks when the test ends**. One lane server answers every
  worker, and a turn waits for a provider slot, so a shared project adds board-level interference to an
  already-queued turn; specs on the global view must not. A live task is charged its container's memory cap
  against the executor's admission gate for as long as it exists, so an unreleased task closes that gate on
  the tests after it: `clearTasksAfterEach()` in `tests/e2e/helpers.ts`. `npm run check:e2e-ownership` holds
  the ownership line against a frozen register of the specs that predate it,
  and `docs/testing.md → E2E conventions` carries both measurements.
- **Read a turn from the store by row id, never by a length or a status.** `…/messages` answers a bounded
  window — 5 units by default — and answers an error with a null body. A finished run is `review` + `idle`;
  `failed` means failed, and `in_progress` survives a Stop. A `/send` that lands mid-turn is absorbed with a
  200. `tests/e2e/helpers.ts` owns the four rules as `messageRows`, `sendTurn`, `notInFlight`, `runHasEnded`;
  `docs/testing.md → Reading a turn from the store, not from the screen` owns why.
- **`[role="dialog"]` is never a unique locator** — filter by content.
- Go tests are deterministic: `testServer(t)` for an isolated server, `t.TempDir()` for files, no network.
- A frontend test file that imports a module using Svelte compiler macros (`$state`, `$derived`, `$effect`)
  **must** be `.svelte.ts` (or `.unit.svelte.ts`); plain `.ts` does not see the macros.
- The e2e harness must leave nothing behind — verify processes, ports and `/tmp` state after touching
  setup or teardown.
- Environment prerequisites, the port table, teardown by identity, and the load-sensitive tests:
  **`docs/testing.md`**.

## API

- The frontend talks **only** to the Go server; all LLM traffic is proxied. Realtime is WebSocket
  through Go.
- Inbound frames are typed; outbound frames are flat maps built where they are broadcast — read
  `msg.task_id`, not `msg.data.task_id`. A frame that is not a JSON object is dropped and reported once:
  a throw inside `onmessage` is swallowed and leaves the app believing it is current.
- **One goroutine per app-stream connection writes its socket**, besides that connection's pinger; every
  delivery path queues into it and never calls `Write` itself. The queue drops the **oldest** frames and
  ends the connection so the client resyncs — a slow browser degrades to a reconnect, never to a timeout
  on someone's wait, and never to a producer that waits for it. `docs/api-contract.md`,
  `docs/reconnection.md`.
- **Every `/api` route declares its verb in the pattern.** Handlers don't check `r.Method`, and a
  methodless route also escapes the CSRF middleware. Exemptions and consequences: `docs/api-contract.md`.
- **A partial update decodes into pointer fields, never a `map[string]any` behind type assertions.** A field that
  is absent — or sent as `null` — is left alone; a field that is present is written, including `""` and `false`. A
  field of the wrong type is a 400 naming that field, and nothing is written before every field validates.
- **`?parsed=1` is a contract every diff path must honour** — `git.ParseUnifiedDiff` is the only diff
  grammar, and the panel never re-parses patch text.
- **A server fault answers with MIKE's own message, never the underlying error text; the cause goes to the
  log.** `httputil.Fail` is the only place a status code is chosen, and an `Operation` does not unwrap its cause.
- Full frame shapes, route exemptions, headers, error vocabulary, and which package owns a new endpoint:
  **`docs/api-contract.md`**.

## Frontend

- **Everything the app did not write goes through `$lib/utils/sanitize`** — including LSP hover,
  completion and signature documentation, which reaches an `innerHTML` sink in a dependency that no gate
  scans. `html-sinks.py` lists every `{@html}`/`innerHTML` sink in core, plus every URL and style-attribute
  binding the browser interprets on its own, each with the mechanism it relies on, and it fails if its own list
  misses a sink; what the sanitizer refuses and why is in `docs/markup-sanitizing.md`.
- **Formatting follows the active locale; sorting never does.** Pass `activeLocale()` to every `Intl.*`
  call and never a literal tag or `undefined`. Never cache a locale-derived value at module scope;
  `document.documentElement.lang` has exactly one writer (the i18n runtime); `localeCompare` keeps its
  current arguments so file, branch, symbol, session and model lists stay stable whatever the UI language.
  Phrases are catalog keys in `$lib/i18n/en.ts`, never literals in components — `i18n-gate.py` and
  `i18n-count.py` measure it.
- **All client-side storage goes through `frontend/src/lib/storage.ts`** (one IndexedDB store).
  Never touch IndexedDB from a component; `gc()` is called only from `Board.svelte`. Sign-out erases
  everything — `clearQueue()` then `clearAll()` — because a queue left in memory is written back into the
  emptied database by the next enqueue, and `gc()` prunes only entries whose task no longer exists.
- **Svelte 5:** `$state`/`$derived`/`$derived.by`/`$props`, and `get` from `svelte/store` for the Svelte 4
  stores still in use; runes work inside classes in `.svelte.ts` — no version bumpers or `$effect` mirrors.
  `$lib/services/` (reactive services), `$lib/composables/`, `$lib/utils/` (pure functions).
- **Keyboard navigation moves real DOM focus** — targets come from pure resolvers, never a parallel
  "highlighted" store. `docs/navigation.md`.
- **Project rows and task cards are real links** so the browser's own link actions work; never fake the
  context menu, and keep selectors tag-agnostic. `docs/link-rows.md`.
- **Shortcuts and UI preferences** ride the shared Settings Save/Cancel flow; `PUT /api/config` replaces
  the whole `keyboard_shortcuts` object, so always send all bindings. `docs/adding-a-preference.md`.
- **CJK typography is scoped to `:lang()`.** `docs/typography.md`.
- Chat: never disguise a system message as a user message; streaming cursor only during an active
  response; stats are `X tokens · Ys · Z tok/s` (completion tokens).
- **Chat autoscroll ends on the scroll event that moves the viewport up *and* widens the gap under the
  reader** — only a request for what is above produces that pair. Content arriving below the fold widens
  the gap without moving `scrollTop`; a document that changes height above the fold moves `scrollTop`
  with the reader's row held. Judge it from the move, never from a gesture listener — a dragged scrollbar
  sends this element scroll events and no `pointermove`, and a scroll key `CardDetail` handles never
  reaches the scroller's listener. A viewport that was not being followed cannot be taken over; the
  sticky header moves it up itself. Re-engaging needs a downward arrival at the bottom. A scroll event
  that merely finds the viewport inside the bottom band is where a growth pin left it; a small wheel notch
  undone by the next pin is what a reader calls sticky. The jump-latest button measures `atBottom`, which
  is a position; only `following` decides whether new content re-pins.
  `frontend/src/lib/composables/use-chat-follow.svelte.ts`.
- **A Review notification is the client's decision, never the server's.** The choice is stored in the
  browser that will have to show it (`uiStore.notifyOnReview`, never `/api/config`). An arrival is
  detected from the `project_counts` rise and announced only when the Review column is not in view and
  that task is not the one open. `docs/notifications.md`.
- **A card lists its subtasks as flat rows in run order, never as nested card boxes** — a descendant above
  the row that waits for it, nothing indented, one band per sequence. One pure function owns the order and
  it mirrors `exec`'s schedule. `docs/subtask-outline.md`.
- Diff text is parsed once in `$lib/utils/diff-parser.ts` (pure, unit-tested) and the diff badges come from
  there (`getStatusBadge`); `GitStatusPanel.svelte` keeps its own for git entries. Sticky section headers
  use `position: sticky` with an opaque background.

## Storage

- SQLite, one connection, one serialisation point. **A multi-statement write that must land entirely runs
  in `Store.tx`** — with one connection and the store mutex, that buys failure atomicity, not isolation.
  Anything outside `tx` states its reason at its definition.
- **`ON DELETE CASCADE` deletes rows, nothing else.** One `DELETE FROM projects` removes the rows; a task's
  container, worktree, branches and uploads are stopped by the deletion path, not by the schema. See *Deletion*.
- **The schema is created whole.** A new column is added to the DDL — `baseSchemaSQL`, or a credential
  table's own statement beside its accessors. There is no probe chain and no `PRAGMA user_version`, because
  nothing older has shipped.
- **An instant that SQL compares is stored as fixed-width UTC text.** `RFC3339Nano` trims the zeros off the
  fraction and `Z` sorts above every digit, so trimmed text orders two nearby instants backwards; a zone
  offset repeats an hour at a fall-back. The columns and the tie rule: **`docs/storage.md`**.
- A test that needs a write to fail half way through installs a SQLite `RAISE(ABORT)` trigger, never a hook
  in production code.
- Permissions, ID generation, what is in and out of `tx`: **`docs/storage.md`**.

## Sessions & Auth

Passkeys, not a password: a pairing code authorizes **one registration** and authenticates nothing, and a
session exists only as the answer to a finished ceremony. There is no login password anywhere — a route
that opens a session on any other proof is a regression. The rpID and origin are the *request's*, so one
install holds a different credential set per hostname, and a bare IP address is refused as a relying
party rather than failing inside the browser's prompt. `--reset-passkeys` is a one-shot command that
deletes passkeys **and sessions** and refuses a data directory a running server holds; the GUI cannot
revoke the last passkey, so that command is the only way out of a lost device. Session tokens come from
`crypto/rand` and are stored hashed, so a copied database can't be replayed. The session cookie's `Secure`
flag is decided **per request** (TLS terminated here, or the forwarding header under
`MIKE_TRUST_PROXY`) and there is deliberately no variable for it; logout clears with the flag it set.
`/api/sessions*` are session-authed, CSRF-guarded, never public, and their responses carry no token of any
kind. Ceremonies, pairing codes, tickets and the reset: `docs/passkeys.md`; storage shape, endpoint shapes
and the Settings UI: `docs/sessions.md`.

- **A bearer token is a second credential with a narrower scope.** Issued, labelled and revoked in
  Settings → About; stored as `hex(sha256(token))` like a session and shown once. It authenticates
  only the project-list and task-CRUD/start/stop routes in `tokenScope`
  (`backend/server/token_auth.go`) — everything else, including `/api/tokens` itself, stays
  cookie+CSRF, so a leaked token cannot mint or revoke tokens. A request that presents one is
  answered by the token or not at all: it never falls back to a session cookie, and it needs no
  CSRF header because a browser never attaches this header on its own. Issuance, the allowlist and the
  client-facing route reference: `docs/api.md`.

## Sandboxing

gVisor (`runsc`) is required: task tool calls and terminal sessions run in gVisor containers, rootless,
via podman. Without it, sandboxed projects refuse to start tasks — hard error, no fallback.

- **Where a task runs is one column, `project.sandbox_enabled`, and it is not re-checked.** A sandboxed
  task with a missing bridge gets an error, never a host fallback — that refusal is the whole boundary.
  `DANGER_ZONE=1` permits *disabling* sandboxing; it never gates execution.
- **Boot decisions are one injected value** (`sandboxruntime.Boot`), and the zero value **declines** the
  host-wide stale-container sweep, so a test binary can't delete a live server's containers by forgetting
  a variable. One availability probe, one sweep, shared by both managers.
- **The container environment is only what was configured** — every `-e` is a `KEY=VALUE` assignment from
  one builder; a bare `-e NAME` would copy the server's value into the guest. Invent no defaults: no
  hardcoded `PATH`, no container-wide `TERM`.
- **Container arguments come from one package.** `pkg/sandboxruntime` owns path/capability validation, the
  create-argument preface, and the only way to add a capability (which refuses unless the isolation
  posture is already there). Extensions delegate to it; a project's own Sandbox Configuration is checked by
  the same rules, and again at container creation because a row can predate them.
- **A task container's git pointer is pinned read-only, and a task container that cannot pin it is
  refused.** The pointer is a plain file inside the writable worktree, and the gitdir it names decides
  which configuration the host's next `git status` reads. A project terminal keeps its writable one.
  `docs/sandbox-runtime.md → A task worktree's git pointer is pinned read-only`
- **Extension sandbox mounts are an allowlist, and borrowed roots are read-only** however the manifest
  asks. A manifest is text that came from the registry, not from a person, and an agent extension's
  `sandbox.mount` is a request rather than a grant — only a person's answer under Settings → Extensions reaches
  a container. A capability is judged by its shape, never its name; `--privileged` is an argument and is refused.
- **The bridge is stdio-only**: one `podman exec` per connection, no listener and no port. Interactive
  terminals bypass it. Rebuild the committed `sandbox-bridge` after any edit under `pkg/` —
  `make bridge-check` fails when the artifact is not a build of this tree.
- **Plan mode's bash restriction is prompt text, not executor-enforced.** What holds a planning turn is
  the read-only worktree mount plus the sandbox — don't add a fake allowlist.
- Invariants that must not break: a connection *is* the sandbox's reference, `start` is idempotent, every
  request is answered, a retired wire ID is never reused, diagnostics report what is unknown instead of assuming
  clean, and forwarded ports survive. Each mechanism: **`docs/sandbox-bridge.md`**.
- Boot injection, the sweep's running-container rule, environment construction, capability measurements,
  the mount allowlist, podman subprocess rules: **`docs/sandbox-runtime.md`**.

## Confinement (host side)

Enforced by the server process, so it holds for non-sandboxed projects too.

- **File endpoints are jailed to the project plus the task the request names** — never the workspace dir.
  `?project=` and `?task=` must be correlated before either is honoured, and containment checks the
  lexical **and** the symlink-resolved path. Writes go through the one helper that runs the steps in order;
  a handler that drops a step must not compile.
- **The no-project file flow is jailed to `$HOME` plus the declared directories**, decided once at startup
  and injected, so the picker and the create path can never disagree about what was declared.
- **Two host reads sit outside the tool path** (LSP document sync, a project's Dockerfile) and are jailed
  where the path is accepted, because the stored value is what the later read trusts.
- **Nothing is read or captured unbounded**, and every limit is applied before the bytes are read: request
  bodies, proxied responses, subprocess output, file reads and bridge frames each have one. Bound output where
  it is captured, not where it is shown.
- **No subprocess inherits the server environment.** Exactly three allowlists, one per family (git,
  podman, agent), each returning the whole environment with extras last. `os.Environ()` survives in one
  sanctioned place: the operator's own host terminal under `DANGER_ZONE=1`.
- **The git author identity is accessor-only** and process-wide.
- **The release check is the only place where something downloaded from the network becomes running code**, so
  it is opt-in and never self-applying. The preference turns it on (`MIKE_DISABLE_UPDATES=1` forbids it for every
  browser), a newer release is downloaded and verified without touching the install directory, and *Install* — which
  swaps the files and restarts — is the one click that changes anything. An install that is not an unpacked release
  directory — a container, a source tree, anything unnamed — refuses, and gets nothing downloaded either. No auto-apply, ever.
  Mechanism, refusals and what is verified: **`docs/auto-update.md`**.
- Mechanisms, helpers and tests: **`docs/confinement.md`**.

## Concurrency

- **Keyed locks come from `pkg/keylock`, one named domain type per domain**, embedded by value. Distinct
  types and distinct tables are both required: task IDs and project IDs come from the same alphabet, so a
  shared table merges two domains and nothing reports it.
- **Lock tables never leave their package** — accessors return a release function, never a mutex.
- **Lock order: merge before stash, never after.** Never hold a mutex across a `Wait` for a goroutine that
  takes it. A goroutine that wins a lock after its caller timed out must release it.
- **Do not add a seconds-scale bound to a git-family lock** — the wait is usually legitimate; wedged
  sections are surfaced by a slow-wait report instead. A wait a person is watching announces itself; a
  wait nobody watches stays in the log.
- `executionState.sandboxMu` is a **leaf lock**: it may be taken under `taskMu`, never the reverse, and its
  fields are read as a snapshot.
- Domain names, timeouts, watched-wait wiring: **`docs/locking.md`**.

## Git

- **`git.Command`/`git.CommandContext` are the only way this repo starts git**; they set directory,
  environment and `WaitDelay` together, and the execution fence that stops repo content executing cannot
  be forged from a call site.
- **The execution fence pins every setting that names a program by key** (`core.hooksPath`, `core.editor`,
  `core.askPass`). A program the repository picks *by name* — `filter.<name>.clean` — has no key to pin, and
  the read-only git pointer is what stops it.
- **A stash entry is identified by hash, never by position** — the stack is shared by every worktree of the
  project and holds the user's own entries. Work needing a temporarily clean tree goes through `WithStash`,
  which refuses to run when the push failed.
- **Client-supplied refs are validated and fenced** (`--end-of-options`), **clones speak only allowed
  transports** (validated host-side *and* by git's protocol config), and **a stored `commit_hash` is an
  object id** validated where it is accepted, because the later reset never re-checks it.
- **One slugifier** for every name derived from user text, with its matching parser beside it — a GC that
  parses a different shape than the executor writes deletes live branches.
- Fences, the stash bracket, and the accepted user-visible consequences: **`docs/git-operations.md`**.

## Subtask schedule

- **A paused row is a stop, not a schedule decision.** The scheduler starts nothing inside a paused branch,
  and it never restarts a paused row on its own. Whichever of its two writers stopped the row answers it: a
  person's start for a pause, the supervisor's own resume for a memory pause. A pause writes `paused` to the
  task and to every descendant that could still run, for the branch it names. `backend/exec/pause.go`
  writes the pause and marks the branch before it cancels anything, because a cancelled run hands its exit
  to `AutoStartNextSibling`, which reads those rows.
- **A task with unfinished children never runs its own turn.** Start, resume and dragging to In Progress
  start what the schedule picks under it; the row rests started-but-pending. For a click that decision has
  one home, `startUnderSchedule` (`backend/api/task.go`), and it starts a row with a parent through
  `StartSubtask` — the only starter that inherits the parent's model config.

## Commit and Done

- **Only staged changes are committed.** Untracked files stay in the worktree and are discarded when the
  task is deleted — never copied into the main checkout or merged. A conflict-resolution commit stages with
  `git add -u`, **not** `git add -A`.
- **A merge conflict interrupts the click, it does not cancel it.** The turn that finishes resolving merges
  and marks Done whatever `auto_merge` says, but only the recorded click (`ArmCommitAndDone`) authorizes
  that merge, and the resolution it waited for consumes it.
- **Nothing is marked Done that did not reach the target branch.** `docs/commit-and-done.md`.
- **A run the user did not start is not left waiting in Review on a text-only turn** — the parent's
  context answers it, as it answers `ask_question`. Which runs that covers, and why the answer is an
  advisory row, is `docs/auto-answer.md`; the merge flags are never consulted on the way.

## Deletion

- **Deleting a task is one path** — the deleted-events, every teardown step for its subtree, then one
  prune and one board broadcast for the batch. No caller states a subset of those steps.
- **Deleting a project deletes its tasks**, root by root, and then the project's own container, terminal
  session and worktree directory. A second list of "what a task needs when it goes" is how containers get
  left running: the startup sweep skips running containers, so nothing else will ever remove them.
- **Cleanup failures are reported, not fatal.** A project that cannot be deleted cannot be re-added under
  its own ID, and the leftover is logged by name.

## Extensions

Types: `lsp`, `ui`, `css`, `i18n`, `agent`, `llm`, `tool`.

- **An installed extension's directory *is* the install.** Deleting the row deletes the directory, and
  both directions are derived from the stored record, never from a request field. A row whose files are
  gone is repaired at startup from its own pin — never a newer commit.
- **Install is atomic and answers as a stream**; the image is built before the row exists, a failure
  removes the checkout, and the terminal event is emitted last.
- **An extension sandbox has no default image**, and an image is built in a context this package creates —
  never the working directory, which is both a `.dockerignore` accident and a jail break.
- **An extension's only egress is the proxy route**, which enforces the manifest's domain allowlist,
  refuses redirects, caps the response and never echoes the upstream's content type. Never build an
  `http.Client` at a call site.
- **A dependency-cache grant is two fields with two origins** — the manifest declares the container path,
  a human configures the host path — reaches one extension's sandbox, and is always read-only. Never
  discover where a toolchain's cache lives. `docs/extension-dependency-cache.md`.
- **A contributed tool is data, and its address is a grant.** The manifest declares the tool and core runs
  it. The only origin it may contact is the origin stored in a `config_schema` property of type `"url"`, so
  every peer is one a person typed. These tools execute in the server process, so a project's network
  isolation does not cover them and the Sandbox Configuration says so — as it does for an MCP tool, which
  is the other family that runs there. `docs/extension-tools.md`.
- **Language knowledge lives in extensions.** The core holds no language names, keyword lists, comment
  markers or bracket tables, in either language; both sides read the manifest through one helper each, and
  a syntax is data, never code. `docs/languages.md`.
- **The UI language is whichever `i18n` extension is enabled** — there is no language setting and nothing
  stored. Any load failure means whole-catalog English plus a report. `docs/i18n.md`.
- Agent extensions: the backend knows nothing about agent protocols; `EventTypePersist` is the only path
  to the database; the adapter names its binary; an enabled agent extension *is* a provider, merged into
  the config per request and never persisted. `docs/agent-extensions.md`.
- UI extension modules are plain JS, not Svelte components, and the postMessage channel is armed by the
  frame's `exthello` echo. `docs/ui-extensions.md`. An extension entry is also a public document on this
  origin, so its response carries a `sandbox` CSP — the mounted frame's own attribute is not the only
  lock. The preview token is a credential and its asset root
  is the jail: `docs/preview.md`.
- Install/build/delete/repair mechanics, REST surface, capability gates, changelogs:
  **`docs/extension-lifecycle.md`**.

## MCP

- **Speak one era: protocol `2026-07-28`, over Streamable HTTP.** A server that answers another version is
  refused by name, never negotiated down to, and no stdio means no subprocess family to sandbox or to leak
  an environment through.
- **Decline every capability out loud.** Pass an explicitly empty `ClientCapabilities`: the SDK declares
  `roots` for every client unless the options say otherwise, so setting no handler is not a decline.
- **Never bridge `elicitation/create` to `ask_question`.** The pinned era forbids a server eliciting while a
  call is open, and the question ends the turn while its answer returns as a new one. Register no handler and
  never park a turn on a person waiting for a socket.
- **A project opts in per server, and the offer and the lookup read one path**, so a name the offer
  withheld is `Unknown tool` at dispatch and never a call. `mcp_` is a reserved prefix: an extension
  manifest that names a tool with it is refused.
- **One attempt, and only the session's context bounds a wait.** No retries — a tool is not idempotent. The
  server's deadline is on the session, and the answer is capped where it is captured. A timeout of 0 sets no
  deadline, and the call then runs until it answers or the task calling it is stopped.
- **The address a person typed is the only widening.** It goes through the egress client, which still
  refuses the metadata address and follows no redirect; the server's own headers ride that same client,
  because the SDK's transport has no per-request hook.
- **The registry is configuration, and the operator can freeze it.** `MIKE_RESTRICT_MCP=1` means no add, edit,
  removal or Test from a browser, and a config save whose registry differs from the stored one is refused by
  name. A stored server is still listed, offered and called, and a project still opts in per server.
- **A stored header never leaves the process that stores it.** The config view answers the masked
  placeholder, `POST /api/mcp/test` resolves a placeholder back to what is stored rather than trusting the
  browser, and that route writes nothing and is out of `tokenScope`.
- MCP mechanics, the caps, and the elicitation argument: **`docs/mcp.md`**.

## Reconnection & Offline

The client distinguishes a cancellation from a lost connection and treats neither as a deadline: an abort
never marks the app offline, and a long wait gets a report naming what it waits on rather than a timeout.
Nothing in the product puts a deadline on a wait for this server. A write queues while offline, **and so does a
write whose request got no answer**: `writeOrQueue` decides that once, at the api layer, and a refusal of any
status is an answer that reaches the caller. The exception is a request carrying a plaintext secret, which is
refused rather than queued — the queue is persisted, so a queued value outlives the tab and the session. A connection's **subscriptions** are not queued frames:
what it owes the server is its current state per project or task key, coalesced and bounded by what the app
has open, because two holds of one key are undone by one release. **A task's status is never the answer to
"is it still running"** — a stopped task keeps `status = in_progress` and only moves `execution_state` to
`paused`, and `taskIsInFlight` (`$lib/utils/task-utils.ts`), mirrored server-side by `terminalTurnStates`
(`stream/app_stream.go`), is what decides whether an unanswered tool call is work or a leftover. **The offline
mirror holds only what this browser received** — a board response or a frame it was sent — and pending intent
stays in the queue, replayed onto every mirror landing rather than mirrored; a queued message on screen is the
queue's projection, and a mirror landing **paints a view that holds nothing rather than replacing one that
holds something** (`windowIsReading`). Mechanisms: `docs/reconnection.md`.

## Licensing

Apache-2.0, and a dependency's license is not changed by ours.

- Never remove a dependency's header or NOTICE. **No GPL/AGPL dependencies**; MPL-2.0 is fine (don't
  modify MPL-2.0 files); an Apache-2.0 dependency with a NOTICE requires its content in ours (currently
  wazero). Build/dev-only dependencies stay out of the distributed artifact (`lightningcss`,
  `caniuse-lite`).
- Each extension carries its own `LICENSE`, `NOTICE` and `THIRD_PARTY_NOTICES.md`; bundled third-party
  files keep their own notice.
- Regenerate the notices whenever a dependency is added, removed or upgraded: `go mod tidy` +
  `go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./backend/... ./pkg/... . | sort -u`
  (or `go-licenses report ./...`), `npx license-checker --production --summary`, then update
  `THIRD_PARTY_NOTICES.md` and `NOTICE`. Both commands over-claim — `go list -m all` answers the
  module graph, which is larger than the binary, and `license-checker` counts a package a dependency
  imports as types only — so what is listed is what is compiled or bundled into the shipped binaries.
- **The GPL/AGPL rule is about what MIKE links.** The programs the runtime image installs (podman,
  crun, runsc, git) are separate executables the server runs, not dependencies of its code.

## Debugging

Logs go to server stdout or `/tmp/kanban.log` through `pkg/logging` (see below) and are greppable by
`component=` and `task_id=`. Worktrees live under `.worktrees/`. A stale frontend is a rebuild plus a hard
refresh. E2E connection failures are stale processes on 3000/3001/9999 — see `docs/testing.md`. The flight
recorder is always on, writing `data/kanban.trace` and rotating the previous file to `kanban.trace.old`
every 30 s: `go tool trace -http=:8080 data/kanban.trace`. Ad-hoc browser probes: `docs/testing.md`.

### Logging

Every diagnostic goes through `pkg/logging`; a `log.Printf` in non-test code is a regression.

- `component` is the subsystem, taken from the fixed vocabulary in `logging.Components` — never invented
  per call, and there is no second label attribute. `task_id`/`project_id` are attributes lifted with
  `With`, not text inside a sentence: lift a leading `task=%s`, and re-flow the prose by hand when the id
  sits mid-sentence.
- Level policy: `Error` when the fault reaches a person, `Warn` when the operator can recover and the run
  continued, `Info` for a state change worth keeping, `Debug` (`logging.Debugf`) for per-event chatter.
  Don't raise a line to `Error` because it says "failed".
- `Infof/Warnf/Errorf/Debugf` are printf wrappers so `go vet` checks every call site; `logging.Info(msg)`
  has no format and no args for that reason.
- Tests capture with `logging.TestSink()` (a mutex-guarded buffer — exec, websocket and shutdown goroutines
  write concurrently), never `log.SetOutput`: that idiom captures nothing, and a test asserting on an empty
  buffer passes.

## Writing a doc

A doc is a guideline for whoever changes that subsystem next. It answers four questions in order: what the
subsystem owns, what must stay true, how the code holds it, and which one command fails when the doc has
stopped being true. Put the load-bearing rule first. Size is never a finding; past tense, a sentence that
has to be held in your head, and a doc that names no check all are. A thing the doc itself does not check
is a claim, not a rule — measure it or move it to an ADR. **`docs/documentation.md → What a doc owes the
reader`**.

## Docs Index

Read the file before editing that subsystem — each holds the mechanism, history and tests this file
deliberately omits.

| File | Subsystem |
|---|---|
| `docs/adr/README.md` | what belongs in a decision record, its shape, and the index of the records that exist |
| `docs/configuration.md` | every env var, the flag twins, startup resolution, the archgate exceptions, the release packages |
| `docs/sandbox-runtime.md` | boot injection, the stale sweep, container environment, podman args, capabilities, mount policy, the git pointer pin, memory caps and admission, podman subprocesses |
| `docs/sandbox-bridge.md` | bridge connections, LSP session binding, wire IDs, payload limits, upload lifetime |
| `docs/confinement.md` | path jails, the no-project host jail, size and output caps, subprocess environments |
| `docs/locking.md` | keylock domains, accessor shape, lock order, watched waits, `sandboxMu` |
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
