# AGENTS.md

Design narratives and mechanism walkthroughs live in `docs/` — read the pointer before touching that subsystem. When you learn something new here, *replace* the stale sentence rather than appending.

## Repo Map
- `backend/` — Go server: API handlers, exec loop, LLM client, git/worktrees, sandbox, extensions.
- `frontend/` — Svelte 5 SPA (Vite). Built to `frontend/dist/`, served as static files by the Go server. No dev server in the normal workflow.
- `pkg/bridge/` — sandbox-bridge protocol/client. `pkg/sandboxruntime/` — podman/gVisor runtime + bridge binary discovery.
- `extensions/` — **installed** extensions (`<author>/<name>`), materialized by the registry installer and **deleted with the extension** (see *Extensions*). Don't hand-edit except when a custom extension is requested. Source is `../MIKE-extensions/`, not this directory.
- `../MIKE-extensions/` — source repo for published extensions (`<type>/<name>/`). Its `registry.json` is **generated** — never edit by hand.
- `scripts/gen-registry.py` — regenerates `../MIKE-extensions/registry.json` from extension manifests (`--check` verifies).
- `docs/` — subsystem detail extracted from this file (see the index at the bottom).
- `Makefile` — build/run/test entry points. `data/` — SQLite DB, TLS certs, trace files. `prompts/` — LLM prompt templates.

## Non-Negotiables
- **NEVER commit.** The user will commit manually.
- **Never leave the server running** after a crash or panic — stop the server first.
- **Test-driven:** validate against established facts before calling a change done. A subsystem's tests are the fast loop and the full suite is the gate - a change to `backend/exec/` can pass `go test ./backend/exec/` and still panic in `./backend/server/`, where a task actually runs. Run `go test ./backend/... ./pkg/... . -count=1` before claiming done, and `-race` on whatever you touched.
- **E2E runs kill your dev server** (global-setup owns ports 3000/3001/9999) — restart with `make run` afterwards.
- **Background mode** (`&`) for development.

### Workflow
- **Frontend:** edit `frontend/src/` → `cd frontend && npm run check` (svelte-check; keep the codebase **TS-clean**) → `npm run build` → hard refresh. **No server restart needed.**
- **Backend:** edit `backend/`/`pkg/` → `gofmt -w` the touched files → `go build -trimpath -o kanban .` (or `make build`) → restart. Config persists in SQLite (`data/kanban.db`).

### Verify the tool output, not the expectation
The most common way this repo produces a **false "done"** is an agent believing a tool call that agreed with what it expected. Three that happened in one session: a conversion reported as landed while `grep` still counted 83 unconverted call sites; a comment cleanup that only rewritten two of five files; a file I believed I had read turned out not to exist on disk. So: re-run the measuring command after the change, and let the claim be the command, not the summary. `TODO.md` records this per task, with a "**not done**" list that is deliberately kept visible.

### Known gaps - do not trust these
- **`go build ./...` and `go vet ./...` fail at HEAD** on the installed extensions under `extensions/`, which are pulled artifacts, not this tree. Scope every command: `go build ./backend/... ./pkg/... .`, `go vet ./backend/... ./pkg/... .`, `staticcheck -checks=all ./backend/... ./pkg/... .`. A red `./...` is not your regression until you've seen it survive the scoping.
- **Most subprocesses are not cancellable.** 83 `exec.Command` calls carry no context; only a handful use `CommandContext`. Don't assume a killed request stops the work it started - and if you add one, use `CommandContext` *plus* `cmd.WaitDelay` (`pkg/sandboxruntime.PodmanWaitDelay`).
- **`environment` on an LSP extension always answers "healthy"** - declared, validated, documented, and never called (`ExtensionLSPManager.Environment` has no caller). See `docs/extension-dependency-cache.md`.
- **`prompts_dir` on an `i18n` pack is validated and has no caller** - it is the server-side prompt-locale directory, and the prompt side (T14) must read it through `extensions.ActiveI18N` rather than re-derive the winner. Until then the UI half of the capability is fully live and this one field is what nothing reads. See `docs/i18n.md`.
- **A green `./backend/exec/` is not proof.** The package's own tests never start a task, so locking bugs in `executionState` only surface under `-race` in `./backend/server/`.

## Required Dependencies

### gVisor
gVisor (`runsc`) is required for project sandboxing — task tool calls and terminal sessions run in gVisor-isolated containers, rootless, via `podman --runtime runsc`. Without it sandboxed projects refuse to start tasks (hard error, no fallback); sandbox unit tests need `runsc` + working podman storage (see *Testing → Go Unit Tests*).

- **One availability probe, one stale-container sweep.** `sandboxruntime.GVisorAvailable(tag)` backs both `SandboxManager.checkAvailable` and the extension manager's probe — they must agree, or a task sandbox and an extension sandbox disagree about whether the host can run gVisor at all. `Runtime.CleanupStaleContainers(tag, nameFilter)` is the shared best-effort, 15 s-bounded startup sweep (prefixes `kanban-sandbox-` / `kanban-ext-`); neither manager may re-implement the list/parse/remove loop. Tests: `pkg/sandboxruntime/runtime_test.go`.
- **The container environment is only what was configured.** `containerEnvArgs` (`backend/sandbox/sandbox.go`) is the single source of `-e` flags: the project's Sandbox Configuration envs plus an agent extension's declared `sandbox_env`, project config appended last so it wins a clash. Invent nothing — no hardcoded `PATH` (an `-e` overrides the image's `ENV PATH`, which dropped a custom image's tooling) and no container-wide `TERM` (the terminal sets `TERM` on its own `podman exec`, `shellExecArgs`: it belongs to the TTY session, not the sandbox). Tests: `backend/sandbox/containerenv_test.go`.
- **Reaching the host shell is opt-in:** a task/terminal runs on the host only for a project with sandboxing disabled, creatable (or switchable to unsandboxed) only with `DANGER_ZONE=1` in the server env — otherwise 403 (`cannot create project with sandbox disabled — set DANGER_ZONE=1 to allow`). `backend/managers/project.go`.
- **The terminal WebSocket verifies `Origin`** (`backend/term/terminal.go`, like the LSP endpoint): `SameSite=Lax` blocks the cross-site request first; this is the second lock on an endpoint that hands out a shell — don't remove it.
- **Extension sandboxes obey a mount/capability allowlist** (`backend/extensions/mount_policy.go`): host mounts must resolve inside the policy's roots (workspace dir, registered project paths, extensions dir, system temp dir, `MIKE_EXT_MOUNT_ROOTS`); container paths must match `^/[A-Za-z0-9._/-]+$` (`:` or `,` would rewrite the podman mount spec); capabilities reaching past the container (`SYS_ADMIN`, `SYS_PTRACE`, `DAC_OVERRIDE`, `NET_ADMIN`, `MKNOD`, `SETUID`, …) are refused even when requested. Enforced on `PUT /api/extensions/{id}` **and** at install time — a manifest that self-grants a bad sandbox is rejected, not persisted.
- **Plan mode's bash restriction is advisory by design** (prompt text in `prompts/plan-mode.tmpl`, not executor-enforced): the read-only worktree mount + sandbox is what actually holds a planning turn, so don't add a fake allowlist.

### Sandbox Bridge
`sandbox-bridge` is a static Go binary that runs inside each sandbox container and services tool calls (read, write, edit, bash — plus a `bash_cancel` notification and the LSP JSON-RPC passthrough) over **the stdin/stdout of one `podman exec -i … /sandbox-bridge` per connection**. There is **no listener and no port** (`stdioConn` in `pkg/bridge/agent/main.go` ↔ `sandbox.NewClient(stdin, stdout)`), nothing in the repo calls `net.Dial`, and logging goes to stderr only. Interactive terminals bypass the bridge entirely (host-side PTY, `backend/term/terminal.go`).

Invariants that must not break (each one's mechanism, history and open work: `docs/sandbox-bridge.md`):

- One connection per task sandbox for its lifetime; **one per LSP session** for an extension sandbox - the connection *is* the sandbox reference.
- A repeated `start` of the same command+args is **idempotent**; a different one is **refused, never replaced**. Nothing auto-restarts a server.
- **A client request is always answered** - a failed send must still answer the browser with a JSON-RPC error.
- A closed `LSPNotificationCh()` means "this server is gone" -> retire.
- **Absent diagnostics while a file is unloaded are unknown, not clean** (`backend/exec/diagnostics.go` refuses to read loader-state entries as findings).
- Wire IDs `0x05`-`0x08` are **retired and must not be reused** - a stale bridge must get "unknown method", not a wrong handler.
- The only `-p` publishing on a sandbox container is **user-configured** forwarded ports (bound to `127.0.0.1`), never the bridge.
- `sandbox-bridge` is **rebuilt from source and the artifact is committed** - an edit under `pkg/` that is not re-built is an edit that never runs: `make bridge-check` fails when the committed binary is not a build of this tree.

`executionState` locking: `sandboxMu` is a **leaf lock** - it may be taken under `taskMu`, and `taskMu` must never be taken under it. Fields guarded by it are read as a **snapshot** (`sandbox()`) and written through `setSandbox`, never captured piecemeal: `executeTask`'s wait reads some of them after a `StopTask` can have run and would assemble a mix of old and new. `sandboxRecreate.underLock` takes `*sandboxState` for the same reason - it runs while `sandboxMu` is held. Tests: `backend/exec/tools_test.go`.

## Server Management

```bash
make run    # background dev server, no TLS (dev defaults: password=test, dev-only MIKE_ENCRYPTION_KEY)
make stop   # pkill the server
curl -sk https://localhost:3000/api/config   # verify (401 JSON = up); use http:// with --no-tls
```
Or manually: `./kanban &` (requires `MIKE_ENCRYPTION_KEY` and `MIKE_LOGIN_PASSWORD` exported). `make run`'s defaults are overridable by exporting your own vars — export your real `MIKE_ENCRYPTION_KEY` if stored secrets in `data/kanban.db` must decrypt.

### HTTPS / TLS
TLS is **on by default**: a local CA in `data/tls/` (`mike-ca.crt`/`.key`) signs a server leaf (`mike.crt`/`.key`). Cert details, SAN set, distribution and the reverse-proxy/`Host` interaction: `docs/tls.md`.

- **The CA never rotates** and there is **no expiry-based renewal** — refresh an expired leaf by deleting `mike.crt`/`mike.key` and restarting (keep the CA). The leaf is re-issued only when the host's names change.
- **The TLS-vs-plain first-byte sniff must not run inside `hybridListener.Accept()`** - `net/http` accepts **serially**, so one peer that connects and sends nothing stalls every later handshake, and returning that peer's EOF reads as a dead listener, ends `Serve` and kills the process. `Accept()` waits on a queue a per-connection goroutine fills. Silent peers are closed and logged per host, never handed to `net/http`. Tests: `backend/server/listener_test.go`.
- **Never `log.Fatalf` on a `Serve()` error** (`main.go`): run the signal cleanup path (flight recorder, `srv.Shutdown`) and then exit 1 — `log.Fatalf` skips that cleanup and makes any listener error an abrupt exit.
- **SIGTERM must end the process.** `srv.Shutdown` runs under `server.RunShutdownWithDeadline` (`backend/server/shutdown.go`, 15s). Every step waits on something uninterruptible (a container not answering podman, an LSP bridge whose peer died), and a shutdown that hangs leaves the process **holding the data directory open** — it re-creates `kanban.db`/`-wal` after whatever deleted them, so the next run boots on the previous run's database. A bounded *context* bounds nothing here; the deadline is enforced by leaving anyway. Each step logs `shutdown: <name>` before running, so the last line names what wedged. Tests: `backend/server/shutdown_test.go`.
- **Every podman subprocess carries `cmd.WaitDelay`** (`sandboxruntime.PodmanWaitDelay`, 10s), set by `pkg/sandboxruntime`'s two helpers and by `sandbox.podmanCommand`, the only way `backend/sandbox` starts podman. A cancelled context is not the bound: after the kill, `cmd.Wait` still waits for the **output pipes**, and a container that stops answering holds them open — including the bridge connection, whose LSP child inherits those very pipes. Shutdown order follows from it: websocket watchers → terminal sessions → lsp → extensions → sandbox containers → database, so a session's shell is killed while its container can still receive it. Tests: `backend/sandbox/podman_wait_test.go`, `TestTerminalManagerClose`.
- **A certificate is not the gate** — reachability is `MIKE_LISTEN` + Host pinning (`MIKE_ALLOWED_HOSTS`), not the SAN set. Weak-password/non-loopback startup rules: `docs/tls.md`.
- Tests: `backend/tls/tls_test.go`, `backend/server/handler_tls_test.go` (the CA download offered by Settings → About).

### Required Environment Variables
Every `MIKE_*` below also has a `--flag` twin named after it (`--port`/`MIKE_PORT`, `--listen`, `--data-dir`/`MIKE_DATA_DIR`, `--no-tls`, `--tls-cert`/`--tls-key`, `--allowed-hosts`, `--worktrees-dir`); **flags take priority over env vars**. The flag → env → default ladder is resolved in one place (`resolveString`/`resolvePort`, `startup_config.go`) — a new variable calls those instead of adding another `os.Getenv` chain — and **a malformed value fails startup naming the variable**. A set-but-empty variable means *unset*: the two readings of `MIKE_LISTEN=` are "loopback" and "every interface". The one non-obvious flag: `--worktrees-dir` (`MIKE_WORKTREES_DIR`) is a runtime override that is **not** persisted to the DB `workspace_dir`.

| Env Var | Description |
|---------|-------------|
| `MIKE_ENCRYPTION_KEY` | Non-blank; derives the AES-256-GCM key for sensitive data. Server refuses to start without it. |
| `MIKE_LOGIN_PASSWORD` | Master password for auth/sessions. Server refuses to start without it. Only its `sha256` digest is kept in memory; a weak one (< 8 chars, or a known default) additionally refuses a non-loopback listen address. |
| `MIKE_REGISTRY_URL` | Extension registry index URL. Sourced **exclusively** from this env var (not DB-backed); falls back to `https://getmike.dev/extensions/registry.json`. A bare path (no `http(s)://`/`file://` scheme) is a local file resolved against the CWD. |
| `MIKE_TRUST_PROXY` | Set to `1` only behind a trusted reverse proxy that strips forwarding headers. Only then are `X-Forwarded-For` (rate-limit IP) and `X-Forwarded-Proto` (HTTPS redirect) honored; unset = ignored so they can't be spoofed. |
| `MIKE_LISTEN` | Which socket the API is reachable on — it **never authorizes a `Host` header**. **Defaults to `127.0.0.1`**: the API is RCE-complete (terminal WS, git ops, file writes), so it is never public by accident. `0.0.0.0`/`::` are the explicit all-interfaces values; any other address listens on that interface only. (Renamed from `--bind`/`MIKE_BIND` — the old names do nothing; a stale `MIKE_BIND=0.0.0.0` comes up loopback-only.) |
| `MIKE_ALLOWED_HOSTS` | Comma-separated extra hostnames accepted in the `Host` header (DNS-rebinding pinning, `HostPinMiddleware`). Accepted without listing: `localhost`, `127.0.0.1`, `::1`, the listen host. Anything else — a proxy's public domain, `hostname.local`, a LAN IP — gets **HTTP 421** until listed here; a missing/empty `Host` is rejected too. Ports are stripped on both sides. `--allowed-hosts` **overrides** (not extends); `Serve()` logs the effective value and every rejection names both. |
| `MIKE_LOGIN_RATE_LIMIT` | Login budget per client IP per minute (default `5`). **Cannot be disabled:** `0`, negative or unparsable values fall back to `5` with a startup warning, so a typo can't silently drop brute-force protection. |
| `MIKE_EXT_MOUNT_ROOTS` | `os.PathList`-separated list of **extra** host roots an extension sandbox may bind-mount, beyond the implicit ones (workspace dir, registered project paths, extensions dir, system temp dir). Still an allowlist — anything outside it is rejected. |
| `DANGER_ZONE` | Set to `1` to allow projects with sandboxing disabled (host-shell execution). Unset = such projects cannot be created or switched to unsandboxed (403). |

## Testing

### Commands
```bash
cd frontend && npx playwright test                       # e2e (auto-starts test servers + mock LLM)
npx playwright test task-message-actions                 # one spec file
npx playwright test --grep "should edit message"         # one test by name
cd frontend && npm test                                  # frontend unit (vitest, ~10s)
go test ./backend/... ./pkg/... -count=1 -parallel 8     # Go unit (~60s)
make test | make test-go | make test-frontend | make test-e2e
```

### Test Environment
Two dedicated test servers with isolated databases plus one shared mock LLM, fully managed by `frontend/tests/global-setup.ts` (torn down and rebuilt each run):

| Server | Port | Data dir | Purpose |
|--------|------|----------|---------|
| Main | 3000 | `/tmp/kanban-test-data` | `chromium` Playwright project (default tests) |
| Isolated | 3001 | `/tmp/kanban-test-data-isolated` | `isolated` project (serialized, 1 worker) — config-modifying suites; see `ISOLATED_SPECS` in `frontend/playwright.config.ts` |
| Mock LLM | 9999 | — | shared; `frontend/tests/mock-llm.js` |

Test repos live at `/tmp/kanban-test-repo` (+ `-2`), worktrees at `/tmp/kanban-test-worktrees/`. The registry is pinned to a local fixture (`frontend/tests/registry-fixture.json`) so registry e2e never hits the hosted registry - but the servers read `/tmp/kanban-test-registry.json`, which setup writes from that fixture with the i18n pack's `__I18N_PACK_COMMIT__` placeholder resolved to the HEAD of the local git repo it builds from `tests/fixtures/i18n-pack-repo`, because a git-pinned install needs a commit that exists.

Test servers are spawned via `go run .` and **inherit your shell environment** — export `MIKE_ENCRYPTION_KEY` before running e2e, or global-setup fails with "Server on port 3000 did not start within 15000ms".

**A run must leave nothing behind** — an enforced property, not a nicety. `globalTeardown` is `tests/global-teardown.ts`, whose default export is *only* teardown: it used to name `global-setup.ts`, whose default export is `main()`, so every run ended by running **setup again** and leaving a server pair, lock and database nothing would stop — and the next run booted on a *used* DB with the previous run's extensions, providers and tasks. Verify after any change to setup/teardown: `pgrep -xc kanban` → 0, nothing listening on 3000/3001/9999, no `/tmp/kanban-test-data*`. Teardown stops servers by **identity** (`--data-dir <dir>`): they are `setsid go run .` grandchildren, so the port owner is not the process that should stop, and `lsof -ti:PORT` also answers with clients.

If a prior run was interrupted, clean stale state first (stale processes hold the ports — symptom `ERR_CONNECTION_REFUSED`, or a stale TLS server answering a `--no-tls` test — and a stale lock makes global-setup run teardown instead of setup):
```bash
lsof -ti:3000,3001,9999 | xargs kill 2>/dev/null
rm -f /tmp/kanban-test-setup.lock
rm -rf /tmp/kanban-test-data /tmp/kanban-test-data-isolated /tmp/kanban-test-worktrees
```

### Frontend Unit Tests
- vitest + @testing-library/svelte, jsdom. Files: `src/**/*.unit.ts` / `src/**/*.unit.svelte.ts`, colocated in `__tests__/` dirs.
- Any test file importing a module that uses Svelte 5 compiler macros (`$state`, `$derived`, `$effect`) **must** use the `.svelte.ts` extension.
- Deterministic — no external dependencies (LLM, network, server).

### Go Unit Tests
- Use the `testServer(t)` helper for an isolated server with a temp DB; `t.TempDir()` for files. Deterministic — no external dependencies.
- **`MIKE_ENCRYPTION_KEY` must be set in the shell** for `go test ./backend/...`: the config tests that store a provider API key encrypt it on the way in (`backend/config` fails with `crypto: MIKE_ENCRYPTION_KEY environment variable is not set`, not with a test failure you can read past).
- A few tests shell out to a real `git commit` without `-c user.*` flags (e.g. the `backend/managers` remote-fixture tests), so the machine needs a git identity (`git config --global user.email …`) or they fail with `Committer identity unknown`. Do **not** also set `init.defaultBranch` to `main` globally: fixture repos then collide with their own `git worktree add -B main` and a batch of managers tests fails in setup, which looks exactly like a regression.- Sandbox tests need `runsc` **and** a working podman storage: under a nested/overlayfs root they all fail with `'overlay' is not supported over overlayfs, a mount_program is required` (podman exit status 125). That is environmental, not a regression — verify with `podman info` before reading anything into it.
- The Pi agent adapter's unit tests live in `../MIKE-extensions/agent/pi` and are run there — they are **not** part of `./backend/... ./pkg/...`.

### E2E Tests
Specs in `frontend/tests/e2e/` (helpers in `helpers.ts`); **mock LLM by default** (port 9999). Conventions that bite:

- **Config-modifying specs must run in the `isolated` project** - add the spec name to `ISOLATED_SPECS` in `frontend/playwright.config.ts` (one array; the chromium lane's `testMatch` is derived from it, so the two lanes cannot drift).
- **Every spec that runs an executor TURN owns its own project** via `isolatedProject()` + `gotoProject()`. Turns are queued per project, so on the shared project your fork's answer waits behind every other worker's task and the spec times out waiting for idle. Same for anything that mutates the project (`git add`/`commit`, or its **sandbox configuration**). Specs asserting on the **global** view (sidebar list, file browser, add-project flow) must NOT use it, and must never do `.project-item` + `.first()`.
- **`[role="dialog"]` is never a unique locator** (BaseModal, both explorer views, `TerminalPanel`, `CommitDiffOverlay`, and every ChatMessage's `<dialog>`) - filter by content.
- **A spec exists only if it needs a browser or the real backend.** Component state belongs in vitest (1.4k assertions in ~20s versus a page-load tax per spec). And **never** write a `readFileSync('../Foo.svelte')` + `toContain(...)` "test": it cannot fail a real bug and breaks on renames.

Pin the mock LLM's fixture with `llm-fixture:<name>` in the prompt - keyword routing is unreachable from an ordinary task prompt, because the plan-first skill is in every system prompt. `workers: 4` is measured: 6 is ~10% faster and buys 1-4 rotating turn-queue failures per run.

Full conventions, fixture format, the dedicated-repo recipe, and ad-hoc `.mjs` probes: `docs/testing.md`.

## Architecture

### Client-Server Boundary
- **The frontend ONLY talks to the Go server** (`/api/...`) — it never calls an LLM endpoint directly; all LLM traffic (chat, model listing, `POST /api/config/fetch-models`) is proxied through Go.
- All realtime is WebSocket through Go. Inbound frames are `stream.AppStreamMessage` (`Data` is `json.RawMessage`); **outbound** frames are flat maps built where they're broadcast (no shared envelope struct in Go) — a `task_event` carries `task_id`/`event` beside `data`, so the client reads `msg.task_id`, **not** `msg.data.task_id`. Use optimistic UI for user messages (add immediately, skip the echo).
- Model/provider changes ride a `user_message` with optional `model_name`/`llama_server_url`.
- **Provider routing:** `PUT /api/config` is a partial update. Two required pairs: `worker_provider`/`worker_model` (chat; resolved frontend-side by `resolveWorker` in `$lib/utils/providers.ts`) and `utility_provider`/`utility_model` (titles, summaries, commit messages, checkpoint labels — `utilityTarget()` in `backend/exec/executor.go`, falling back to the task's own URL/model).
- **A partial update decodes into pointer fields, never a `map[string]any` read behind type assertions.** `PUT /api/projects/{projectID}/tasks/{taskID}` uses `taskUpdateRequest` (`backend/managers/task_update.go`) and `PUT /api/config` uses `configUpdates` (`backend/config/updates.go`): absent key = leave alone, present key = write (including `""` and `false`), JSON null = absent. A single-value assertion (`v.(string)`) panics and the recovered panic reaches the browser as a 500; the `, ok` form silently drops the value and answers 200, so the UI believes its own save. Wrong types are a 400 naming the field, and validation runs before anything is written. Tests: `backend/managers/task_update_test.go`, `backend/config/updates_test.go`.

### Store & Migrations
SQLite, one connection (`SetMaxOpenConns(1)`) so the `PRAGMA`s apply everywhere; `store.Store.mu` is the serialisation point. Tests: `backend/store/` (`store_test.go`, `migrations_test.go`, `sessions_test.go`).

- **`PRAGMA user_version` gates the schema.** SQLite has no `ADD COLUMN IF NOT EXISTS`, so ~53 columns are each probed before their `ALTER` (`hasColumn`/`addColumn`/`renameColumn`, `backend/store/migrations.go`). `initSchema` returns early when the database is already at `schemaVersion`, and refuses to touch one stamped **newer** than this build. Adding a migration means extending the probe chain *and* raising `schemaVersion`, leaving the earlier steps in place — they must stay idempotent, and a step that must run exactly once needs its own marker (the config-row guard on the backfill) rather than trusting the number.

### Sessions & Auth
One master password (`MIKE_LOGIN_PASSWORD`). Storage shape, endpoint shapes and the Settings UI: `docs/sessions.md`.

- `SessionManager` (`backend/auth/session.go`) keeps **only `sha256(password)`** and compares with `subtle.ConstantTimeCompare` — both sides always 32 bytes (a length mismatch would make the constant-time compare short-circuit and leak).
- **Tokens:** 32 bytes from `crypto/rand`, 30-day TTL. The raw token exists only in the response/cookie; the DB stores `hex(sha256(token))`, so a copied `kanban.db` can't be replayed as a session.
- **`last_seen` writes stay throttled** (60 s, `lastSeenInterval`) — the store serialises on one connection, so an unthrottled write makes every authenticated request a DB write.
- **Files are owner-only:** data dir `0700`; `kanban.db` + `-wal`/`-shm` `0600` (`backend/store/store.go`); `kanban.trace` `0600` (`main.go`).
- **Task/thread IDs** (`backend/types/idgen.go`) draw dictionary words through `crypto/rand` (`secureIndex`), never clock-seeded `math/rand` — they surface in URLs, branch names and worktree paths; a failing system source panics rather than issuing a guessable ID.
- `/api/sessions`, `DELETE /api/sessions/{id}`, `POST /api/sessions/revoke-others` are session-authed + CSRF-guarded and **never** in `publicPaths`; the list response type has **no token / token-hash / CSRF field at all**. Tests: `backend/auth/session_test.go`, `backend/store/sessions_test.go`, `backend/server/handler_sessions_test.go`, `backend/types/idgen_test.go`, e2e `settings-sessions.spec.ts` (isolated).

### Execution Containment (host side)
Enforced by the server process itself, so it holds for **non-sandboxed** projects too:

- **The git author identity is process-wide and accessor-only.** Use `git.SetAuthorIdentity` / `git.AuthorIdentity` (`backend/git/worktree.go`); there are deliberately no exported `GitAuthorName`/`GitAuthorEmail` variables, because the add-project flow writes the identity while tasks are committing. Making it per-`WorktreeManager` is still open.
- **Tool writes are jailed to the worktree's `os.Root`.** Parent-directory creation for `write_file`/`edit_file` goes through `ensureRootDir` → `root.MkdirAll` (`backend/exec/tools.go`), **never** `os.MkdirAll(filepath.Join(worktreePath, path))`: the host-side join resolves `..` and creates `<parent-of-worktree>/x/y` *before* `root.Create` rejects the path — arbitrary directory creation as the server user, reachable from prompt injection in repo content. An escaping path must fail up front and create nothing; new dirs are `0700`.
- **Agent subprocesses get an environment allowlist.** `SafeAgentEnv` (`backend/agent/env.go`) is the only source of `cmd.Env` for an adapter's subprocess (`runner.go`) and its `--list-models` probe (`loader.go`): `PATH`/`HOME`/user/term/locale/proxy/trust-store (+ `LC_*`) plus the adapter's own extras (extras last — `os/exec` keeps the last occurrence). `os.Environ()` leaked `MIKE_LOGIN_PASSWORD` and `MIKE_ENCRYPTION_KEY` via `/proc/self/environ` to an extension-supplied binary, so **never** `cmd.Env = os.Environ()` here; add a variable only if an agent genuinely needs it.
- **Tests:** `backend/exec/tools_test.go`, `backend/agent/env_test.go`.

### Git Argument & Transport Hardening
- **Client-supplied refs are validated, then fenced.** `types.ValidateGitRef` (`backend/types/validate.go`) is deliberately tighter than `ValidateBranchName` (no `~ ^ : ? * [ \`, space, leading `-`). `GET /api/fs` validates `?ref=` **and** passes `--end-of-options` (git ≥ 2.24) before `<ref>:<path>` in `git show`, so a ref can never parse as an option (`--output=/tmp/pwn` would create/truncate a file anywhere the server user can write).
- **Clones speak only allowed transports.** `git.CloneArgs` puts `-c protocol.ext.allow=never` *before* `clone` (so the process parsing the URL gets it) and `git.CloneEnv` sets `GIT_ALLOW_PROTOCOL=http:https:git:ssh:file` + `GIT_TERMINAL_PROMPT=0` — enforced by git, independent of the user's `git config`. `git.ValidateCloneURL` mirrors it host-side: no leading `-`, no control characters, no remote-helper `::` transport (`ext::sh -c 'id'`), and a **case-sensitive** scheme check because git's own is (`HTTPS://` is refused as transport `HTTPS`). **Every** clone of a caller-supplied source goes through both halves — project creation (`managers.validateGitCloneURL`) and the extension install (`extensions.CheckoutExtension`). Git's own refusal of `ext::` is a host default, not a defence (a host with `protocol.ext.allow=always` would run the payload as the server user). Use `git.CloneProtocolArgs()` for a clone that needs a bespoke flag — never drop the fence.
- **One slugifier for every name derived from user text.** `types.Slugify` / `types.SlugifyBranchName` (`backend/types/slug.go`, `MaxBranchName` / `BranchSlugMaxLen`) back task branch names, the branch GC and Main-thread recovery, and project IDs; the matching *parser* `types.TaskBranchSlugPrefix` (with `TaskBranchSuffixLen`, the random tail `exec` appends) lives beside them — a GC that parses a different shape than the executor writes deletes live branches. Never add a second copy of either. Non-ASCII letters are dropped, not transliterated.
- **Tests:** `backend/types/validate_test.go`, `backend/types/slug_test.go`, `backend/server/handler_fs_test.go`, `backend/git/clone_test.go`, `backend/managers/project_test.go`, `backend/extensions/registry_test.go`.

### Keyboard Shortcuts / UI Preferences (Settings)
Both ride the shared Settings Save/Cancel flow. **`PUT /api/config` replaces the whole `keyboard_shortcuts` object - always send all bindings**; omitting one resets it to default. Absent = default, `""` = disabled. Capture (`normalizeKey`) rejects modifier-only presses, Escape, unmodified named keys and browser-reserved `Ctrl+W/Q/N/T`. Platform-aware defaults are `default*()` functions, **not** `DEFAULT_SHORTCUTS` entries. A key may be the pseudo-key `click` for a mouse binding - only the primary button is bindable, and an unmodified click is never a binding. UI preferences are top-level `*bool` fields (`parseBoolPtr` in `GetConfig`, `omitempty` in `configResponse`).

Detail and tests: `docs/adding-a-preference.md`.

### Keyboard Navigation (board + task view)
Arrow keys move **real DOM focus** (the global `:focus-visible` ring) - never a separate "highlighted" store. Targets come from pure resolvers (`resolveArrowTarget`, `resolveDetailColumn`, ...) - don't inline target logic in components. The project focus ring is an **inset box-shadow** (ancestors clip outlines).

Detail and tests: `docs/navigation.md`.

### Link-like Rows (projects & task cards)
Sidebar project rows and task cards are **real links**, so the browser's own link actions work (open in new tab, middle click, copy link address). **Never** re-tune these rows with `<button>`/`<div>` + a faked context menu - the link menu is DOM-level and unsynthesizable. Keep selectors tag-agnostic (`.project-item`, not `button.project-item`). A card's root stays a `div` (nested subtask `Card`s would mean anchor-in-anchor) with a full-cover `aria-hidden` overlay link; the stacking contract matters: `.card-title` is deliberately NOT lifted above it.

Detail and tests: `docs/link-rows.md`.

### Reconnection & Offline
`onConnected(true)` in `App.svelte` runs `silentRefresh()` once `initialLoadComplete`; `TaskChat` reloads history from the DB on reconnect because **turns completed while offline are never replayed** (buffer cleared at turn end). `offline` queues writes immediately; `online` flushes the queue and calls `reconnectNow()` to skip backoff.

Detail and tests: `docs/reconnection.md`.

## Extensions

Types: `lsp` (language servers in gVisor sandboxes), `ui` (frontend injection), `css` (a global stylesheet), `i18n` (translation packs - the enabled pack *is* the active language), `agent` (AI agents as subprocesses via WASM adapters), `llm` (provider request adaptation).

**Key files:**
- `backend/extensions/` — domain types, `store.go` (SQLite), `registry.go` (FetchIndex/FetchManifest/PullImage/BuildImage), `sandbox.go` (refcount lifecycle), `handler.go` (REST), `changelog.go`, `manifest_validate.go`
- `backend/lsp/extension_manager.go` — extension-driven LSP discovery (LSP availability = installed extensions)
- `frontend/src/lib/services/extension-manager.svelte.ts`, `ui-injector.svelte.ts`; `$lib/api/extensions.ts`; `ConfigModal/ExtensionsTab.svelte`

**REST:** `/api/extensions` (list/get/update/delete), `PATCH /{id}/enabled`, `GET /registry`, `GET /local`, `GET /check-updates`, `GET /{id}/logs`, `GET|PUT|DELETE /{id}/storage` (the namespaced KV store behind `extHost.storage.*`), `PUT /api/extensions/{id}/dependency-cache` (the one host directory an LSP extension reads its resolved dependencies from — see *Mount a language server's dependencies*), `POST /{id}/proxy` (the **only** extension egress path; enforces the manifest's domain allowlist and refuses redirects). The three long-running operations (`POST /install`, `POST /install-local`, `POST /{id}/update`) answer as **SSE** streams (`streamOperation`); there is **no** build-progress route — build/update output reaches the browser as `build_progress` **task WebSocket** events (`types.WSTypeBuildProgress`, from `exec/loop_helpers.go`).
- **Outbound HTTP goes through `backend/httputil/client.go`**: `NewClient` follows redirects, `NewSingleAttemptClient` refuses them. The refusal is the security control — the domain allowlist is checked against the URL the caller asked for, so following a redirect reaches a host nothing validated. Don't build an `http.Client` at a call site. Tests: `backend/httputil/client_test.go`.

**Deleting an extension deletes its directory.** `extensions/<author>/<name>` is not a cache: UI code is served from it, agent adapters' WASM and binaries load from it, and `GET /api/extensions/local` lists it as installable. A deleted row with its files still present is a deleted extension that keeps executing and returns under its own name with the manifest it was installed with. `removeExtensionFiles` (`backend/extensions/remove.go`) derives the path from the **stored record** through `extensionDir` — never from a request field — and the delete is contained twice: lexically inside the extensions root, and again with symlinks resolved (an `extensions/<author>` that links elsewhere must not delete what it points at). An emptied author folder goes too. Removal runs **after** the row delete, so an unremovable directory cannot make an extension undeletable; a failure is reported as `leftover` in the response (rendered by `ExtensionsTab`) and a `LEFTOVER FILES` log line, because invisible files are still served. Updates are unaffected - `CheckoutExtension` removes the destination first. Tests: `backend/extensions/delete_files_test.go`. Plus the preview endpoints below.

**A row that outlived its files is repaired at startup, from its own pin.** The reverse case: a missing directory with its row present is an enabled extension that silently does nothing - a UI extension's iframe 404s, so its document never boots or registers what it exists for, while `GET /api/extensions` still reports it installed and enabled. `RepairMissingFiles` (`backend/extensions/repair.go`) runs once at startup from `main.go` (off the boot path, never joined to shutdown), derives each directory through the same `extensionDir`, reads "no readable `manifest.json`" as missing (a half-written checkout has a directory), and re-checks out **the record's** pinned commit via `CheckoutExtensionContext` - never a newer one, which is an update the user asks for. A row with no `repo`+`commit` (`local` installs are materialized in place, so their files *were* the install) or a name that is not a directory name is logged, not fetched. Detection is startup-only, so files deleted while the server runs stay broken until the next start. Tests: `backend/extensions/repair_test.go`.

**The LSP sandbox borrows Sandbox Configuration mounts, read-only.** `ExtensionSandboxManager.MountPolicy()` adds the host paths every project declares in its own Sandbox Configuration (`ListProjectSandboxMountPaths`) as roots, and `MountPolicy.ForceReadOnly` pins anything under one of them to `ro` even when the stored config asks for `rw` — the extension did not decide that path exists. `ExtensionHandler.mountPolicy()` (install + `PUT /api/extensions/{id}`) deliberately does **not** get those roots, so an extension can never self-grant one. `adaptProjectMounts` mirrors the *union* over projects (global paths like `/go` for everyone, task-root-relative ones only for their owning workspace), so a new project changes the mount set once instead of on every switch.

**Mount a language server's dependencies; never discover where they live.** An extension declares `capabilities.lsp.sandbox.dependency_cache: {container_path, env}`; the host directory is a **human's** choice, configured in that extension's Edit dialog and stored beside the declaration as `sandbox_config.dependency_host_path` - two fields, because the two origins are the security argument, and a manifest carrying it is **refused**, not stripped. The rules that keep it honest:

- **The grant reaches that one extension's sandbox and nothing else** - `SandboxOpts` has **no field for it**, so a task, a sandboxed terminal and the project container never bind it. It is always `:ro`: a grant is a text field in a web form, and a read-write bind of a toolchain cache onto every sandboxed project's container is the widest write surface in the product.
- **A grant is a root for one extension only.** `MountPolicyForExtension(ext)` adds it; the policy that install and `PUT /api/extensions/{id}` validate against never contains anyone's grant, so an extension cannot self-grant.
- **Never discover where the cache lives.** A discovered path needs a container to ask it in, so the first turn of every workspace pays for a server started to answer `go env GOMODCACHE` - and it puts extension-supplied stdout on the host command line.
- **Core's container path is not borrowable** (`isReservedContainerPath`), because two `-v` specs on one path make podman refuse the whole create. Anything borrowed from a project's Sandbox Configuration is pinned read-only (`ForceReadOnly`) - the extension did not decide that path exists.
- **A grant is revoked by an empty string** on `PUT /{id}/dependency-cache`, which also stops the sandbox (the mount set is fixed at creation).
- **A declaration is optional, and its absence is loud.** The sandbox runs `--network none` with no cache, so without a grant a workspace with third-party imports cannot be resolved and every turn argues with phantom errors.

Full rules - one derivation for both the create and the `MountsCompatible` snapshot, revocation, the two declaration shapes the UI reads, and why `ro` is not a favour: `docs/extension-dependency-cache.md`.

`environment: {check, suppress_codes, note}` is **declared but not enforced** - nothing calls `ExtensionLSPManager.Environment`, so the field always answers healthy; a check must be able to fail (gopls: `GOPROXY=off go list -deps ./...`). `reload_files` **is** wired.

Tests: `backend/extensions/dependency_manifest_test.go`, `dependency_grant_test.go`, `dependency_policy_test.go`, `dependency_mount_test.go`, `bundled_manifests_test.go`, `reload_env_test.go`, `backend/sandbox/containerenv_test.go`, `backend/exec/sandbox_create_opts_test.go`, `backend/term/terminal_test.go`; UI states in `frontend/src/lib/utils/__tests__/extension-permissions.unit.ts` and `frontend/src/lib/components/ConfigModal/__tests__/extensions-dependency-cache.unit.svelte.ts`.

**LSP capability gates** (`manifest.capabilities.lsp`, both default to enabled, both toggled per extension in Settings → Extensions, and **independent** of each other): `editor_enabled` gates the *browser's* LSP features — the server only stores and serves the flag, while `extension-manager.getEditorLSPExtensions()` filters, `editor-language.resolveFile()` answers per file and `lsp-connection-manager.isLSPAvailable()` decides whether a channel opens. `tool_calls_enabled` gates the *agent's* LSP tools and is enforced server-side (`ExtensionRegistry.IsToolCallsEnabledForFile`, checked before an LSP tool runs).

**UI injection:** extensions activate per scope; components call `renderSlot(scope, context)` on mount. Scopes → components: `task-card`→Card, `file-editor`→FileEditor, `project-board`→Board, `task-chat`→TaskChat, `project-sidebar`→Sidebar, `file-browser`→dir picker modal, `create-task`→CreateTaskModal.

### Language Knowledge Lives in Extensions
The core holds **no** language names, keyword lists, comment markers or bracket tables - neither in Go nor in the frontend. `file_types[]` is the routing table *and* the source of the `didOpen` languageId; `language` routes the file to a server and `language_id` is per file type - collapsing them breaks routing or the parse. Both sides read it through exactly one helper (`extensions.LSPConfig.fileTypeFor` in Go, `editor-language.ts::resolveFile` in the browser), **never** a local map. `syntax{}` is a grammar as **data, never code** (a shipped module would run in the app's realm). A bad manifest is **refused, not stored** (`manifest_validate.go`); legacy manifests are normalized at load, not migrated. The one language table left in core is `frontend/src/lib/utils/fallback-languages.ts`, consulted only when no extension declares the type.

Detail and tests: `docs/languages.md`.

### The UI Language Is Derived From the Enabled i18n Extension
The active language **is** whichever `i18n` extension is enabled - English when none. There is **no** `language` config field, no `PUT /api/config` path for one, and nothing stored: a setting would have to be reconciled with the extension list forever, and the two would disagree. `extensions.ActiveI18N` (Go) and `resolveI18NPack` (TS) apply one rule - skip disabled packs, keep those **declaring `capabilities.i18n`** (the declaration, not the inferred `type`), first by `priority` then `name` - and the prompt locale (T14) must call the Go side rather than re-derive it. A pack is a css pack with different bytes: `resolveAssetURL` gives the catalog URL (`''` for a scheme or `//` is the external-origin fence), the existing `/extensions/<author>/<name>/` tree serves it, and there is **no new route**, no iframe, no channel, no permission. Any load failure means **whole-catalog English plus a report**, never a half-applied catalog or a UI of raw key ids; a key the pack lacks falls back per key to English. Core gains zero translated strings and zero language names (`en.ts` holds the English of the keys the UI actually renders - today `i18n.offer.*`, filled further by the string-conversion task; a locale tag is carried through verbatim). The pre-paint cache anticipates the round-trip and never outranks it: the catalog lives in IndexedDB `app-settings` and the tag in `localStorage`, and both are **deleted together** whenever the live list says English - unconditionally, because a guess about what is already cleared left a stale language behind. A non-English browser with no pack enabled is **offered** the registry pack whose projected `capabilities.i18n.locale` matches it (`lib/utils/locale-offer.ts` decides, `services/locale-offer.svelte.ts` installs over the existing git-pinned SSE path): four conditions must hold, a `dismissed`/`installed`/`failed` record in `app-settings` ends it, and **no match is silence, never an error** - so a registry that will not answer says nothing.

Detail and tests: `docs/i18n.md`.

### UI Extensions
UI extensions are **plain JavaScript modules, not Svelte components** (external bundles carry their own Svelte runtime -> `effect_orphan`). A module default-exports `init({ component, config, ...scopeContext })` and returns a cleanup function; no Svelte imports, DOM + `createElement('style')` only. **The host CSP blocks inline `<script>`** - load code as external files. **The channel is armed by the document's `exthello` echo**: `origin === 'null'` + `ev.source` identify the *frame*, not the mounted document, so post nothing before arming (except `exthello-request`) and never tear the frame down on a 2nd `load`. A hand-rolled client that doesn't answer `exthello` hangs at `extHost.ready()`.

Detail and tests: `docs/ui-extensions.md`.

### Preview capability
Host support for UI extensions that render the open file (`html-preview`: overlay + live refresh). **The token is the credential** - 128-bit from `crypto/rand`, and `newPreviewToken` is fail-hard: a guessable token is an unauthenticated read of the document plus every sibling under its asset root. `project`/`path`/`task` come from the **host's own injection context**, never from extension arguments, so an extension cannot point the server at arbitrary directories. `GET /api/preview/{token}/...` is public and the doc carries its OWN CSP that `CORSMiddleware` skips.

Detail and tests: `docs/preview.md`.

### Changelogs
An extension may ship `CHANGELOG.jsonl` next to its manifest (one JSON object per line, oldest→newest: `{"hash", "version", "description"}`), published to `<registry-base>/<author-slug>/<name>`. `GET /api/extensions/check-updates` returns entries newer than the installed commit (anchored on the first hash match); no match / no file / fetch error → empty `changelog` (best-effort, never blocks update detection). UI: `.update-changelog` in the "Updates Available" section of `ExtensionsTab.svelte`. Key files: `backend/extensions/changelog.go`, `FetchChangelog` (`registry.go`), `HandleCheckUpdates`, e2e `extension-changelog.spec.ts`.

### Agent Extensions
Agent extensions run AI agents (e.g. Pi) as subprocesses: `Backend (executor) ←→ AgentRunner (I/O bridge) ←→ WASM Adapter (protocol) ←→ Agent Process`. The backend knows **nothing** about agent protocols — all of it lives in the adapter. Adapter interface + event types, the zero-buffer WASM memory model, and the file-by-file recipe for a new adapter: **`docs/agent-extensions.md`**.

- **Persistence:** `EventTypePersist` (carrying `[]PersistEntry`) is the **only** path to the DB — the executor iterates entries generically; never add agent-specific persistence to the backend.
- **URL scheme** `agent://<name>`, and an enabled agent extension **is a provider** with that URL — `GET /api/config` merges it per request and never persists it — so the client re-reads the config on the `extension_change` broadcast (`appStore.refreshConfig()`): an agent that only shows up after a browser refresh is a bug. Key files: `backend/agent/` (interface, runner, wazero loader with `WithStartFunctions("_initialize")`, registry), `backend/exec/loop_agent.go`, `frontend/src/lib/stores/app.svelte.ts`.

## Common Patterns

### Client-Side Storage
All client-side storage goes through `frontend/src/lib/storage.ts` (async functions; single IndexedDB `kanban-storage`). Never access IndexedDB directly in components. `gc()` (stale-entry sweep, incl. offline message/diff caches for a project) is called only from `Board.svelte` after the board loads — components should NOT call it.

### Svelte 5 Reactivity
- `$state()` (deep reactive), `$derived()` / `$derived.by()`, `$props()`; Svelte 4 stores via `get` from `svelte/store`.
- Runes work inside classes in `.svelte.ts` files — no version bumpers or `$effect` mirrors; assigning a `$state` field notifies every reader.
- Directories: `$lib/services/` (plain/reactive services), `$lib/composables/` (reactive helpers), `$lib/utils/` (pure functions).

### Diff & Status Badges
Diff text is parsed in `$lib/utils/diff-parser.ts` (pure, unit-tested); the diff UI lives in `CardDetail.svelte` (task diff rows) and `DiffViewer.svelte` (staged/unstaged file browser). `getStatusBadge` for diff files is shared from `diff-parser`; `GitStatusPanel.svelte` keeps its own for git entries. Sticky section headers use `position: sticky; top: 0; z-index: 10` + an opaque background.

### Task IDs
`word-word-word` (e.g. `frost-amber-quartz`) from the dictionary in `backend/types/idgen.go`; thread IDs are 2 words via `GenerateThreadID()`; URL: `/?task=frost-amber-quartz`.

### Chat UI Rules
- Never disguise system messages as user messages.
- Streaming cursor only during an active LLM response; a `complete` event is sent when the LLM finishes (text-only or tool calls).
- User messages: right-aligned bubbles; action icons (copy/edit/delete) always visible; editing replaces the message and discards the conversation tree.
- Stats format: `X tokens · Ys · Z tok/s` (completion tokens, not total).

### Commit and Move to Done
`HandleCommitAndDone` commits **only staged** changes: untracked (never-staged) files stay in the worktree and are discarded when the task is deleted - never copied into the main checkout or merged. The conflict-resolution commit stages with `git add -u`, **NOT** `git add -A`. **A merge conflict interrupts the click, it does not cancel it** - the turn that finishes resolving merges and marks Done whatever `auto_merge` says, but only the recorded click (`ArmCommitAndDone`, in memory) authorizes that merge, and it is spent by the resolution it waited for. **Nothing is marked Done that did not reach the target branch.**

Detail and tests: `docs/commit-and-done.md`.

## Licensing

Apache-2.0 (`LICENSE`, `NOTICE`, `THIRD_PARTY_NOTICES.md`). A dependency's license is **not** changed by ours — each library keeps its own license and copyright.

- **Never remove** a dependency's license/copyright header or NOTICE. **No GPL/AGPL** dependencies (incompatible with Apache-2.0); MPL-2.0 is OK (don't modify MPL-2.0 files). Apache-2.0 deps shipping a `NOTICE` require its content in ours (currently: wazero). `lightningcss` (MPL-2.0) and `caniuse-lite` (CC-BY-4.0) are build/dev-only — keep them out of the distributed runtime artifact.
- **Extensions are independently-licensed projects:** each extension directory carries its own `LICENSE` (Apache-2.0), `NOTICE` and `THIRD_PARTY_NOTICES.md`; new extensions get all three.
- **Bundled third-party files** (e.g. an extension's IIFE library) keep **their own** notice — never put an Apache-2.0 header on them.
- **Regenerate notices whenever a dependency is added/removed/upgraded:** `go mod tidy` + `go list -m all` (or `go-licenses report ./...`), `cd frontend && npx license-checker --production --summary`, then update `THIRD_PARTY_NOTICES.md` (incl. "Last updated") and `NOTICE`.

## Debugging

- **Logs:** server stdout or `/tmp/kanban.log`. **Worktrees:** `ls .worktrees/`. **Frontend stale?** rebuild + hard refresh.
- **E2E can't connect / wrong-protocol response:** stale processes hold 3000/3001/9999 — run the cleanup block in *Testing → Test Environment*.
- **`waiting for locator('.project-item')`:** test project not created (check global-setup logs). **`waiting for locator('.card')`:** no tasks exist. **Mock LLM routing wrong:** keyword matching in `mock-llm.js`.
- **Go tests:** `unable to open database file` → use `testServer(t)`. `context deadline exceeded` → raise the timeout or optimize. Sandbox failures → `runsc --version` then `podman info` (the overlayfs failure is environmental, see *Testing → Go Unit Tests*).
- **Ad-hoc browser probes:** standalone Playwright `.mjs` scripts in `frontend/tests/` against a live `make run` server — recipe in `docs/testing.md`.

### Crash Debugging (Flight Recorder)
The Go flight recorder is always-on (~1% overhead), writing to `data/kanban.trace`, rotated every 30 s (previous file → `data/kanban.trace.old`) — on crash it holds ~30 s of pre-crash data. Analyze: `go tool trace -http=:8080 data/kanban.trace`.

## Docs Index
Read the file before editing that subsystem — each holds the detail and history this file deliberately omits.

| File | Subsystem |
|---|---|
| `docs/sandbox-bridge.md` | bridge connections, LSP session binding, per-(extension, project) open work |
| `docs/threads-checkpoints.md` | branch vs snapshot layers, checkpoint pinning/restore, fork inheritance |
| `docs/tls.md` | certs/SANs, distribution, proxy + `Host`, weak-password startup rule |
| `docs/sessions.md` | session storage shape, `/api/sessions*` responses, Settings → About UI |
| `docs/languages.md` | `language` vs `language_id`, `syntax` as data, identifier shape rules, bracket levels, dependency-set trade-offs |
| `docs/ui-extensions.md` | `/extensions` serving + CSP, channel handshake, install model |
| `docs/preview.md` | preview routes, ref rewriting, nav record, iframe isolation, known residuals |
| `docs/agent-extensions.md` | adapter interface + events, WASM memory model, new-adapter recipe, agent-as-provider live refresh |
| `docs/link-rows.md` | project-row/task-card markup, click routing, known limitation |
| `docs/navigation.md` | board + task-view arrow-key resolution |
| `docs/reconnection.md` | WS reconnect/offline queue mechanism |
| `docs/testing.md` | e2e conventions, mock-LLM fixtures/recording, `.mjs` probe scripts, failure patterns |
| `docs/extension-dependency-cache.md` | the `dependency_cache` grant, `ro` mounts, `environment` check, one derivation |
| `docs/commit-and-done.md` | what the click does, conflict arming, which files never get committed |
| `docs/adding-a-preference.md` | the 7-layer recipes for a shortcut or a `*bool` preference |
| `docs/i18n.md` | the derived-language rule (one rule, two ends), the catalog load + failure policy, the pre-paint cache vs reality, the first-run offer's four conditions |
