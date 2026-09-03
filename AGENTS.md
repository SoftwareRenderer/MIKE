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
- **Test-driven:** validate against established facts before calling a change done. Sections below name their tests; run the named ones, not just the full suite.
- **E2E runs kill your dev server** (global-setup owns ports 3000/3001/9999) — restart with `make run` afterwards.
- **Background mode** (`&`) for development.

### Workflow
- **Frontend:** edit `frontend/src/` → `cd frontend && npm run check` (svelte-check; keep the codebase **TS-clean**) → `npm run build` → hard refresh. **No server restart needed.**
- **Backend:** edit `backend/`/`pkg/` → `gofmt -w` the touched files → `go build -trimpath -o kanban .` (or `make build`) → restart. Config persists in SQLite (`data/kanban.db`).

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

Invariants to preserve (the why, and the open work, are in `docs/sandbox-bridge.md`):
- A task sandbox has exactly one connection for its lifetime; an **extension sandbox has one per LSP session**. `ExtensionSandboxManager.StartSession`/`ReleaseSession` (`backend/extensions/sandbox.go`) own it and `retire` (`backend/lsp/extension_manager.go`) hands it back — the connection *is* the sandbox reference.
- A repeated `start` of the same command+args is **idempotent**; a different one is **refused, never replaced**. The agent kills its session when the connection ends (`handler.close`) — the only thing that reaps the server — and never auto-restarts one.
- The host treats a closed `LSPNotificationCh()` as "this server is gone" (`sandboxClient.dead` → `usable()` → retire), since the connection outlives the process.
- **A client request is always answered:** `forwardRequest` (`backend/lsp/handler.go`) reports a failed send *and* answers the browser with a JSON-RPC error; a failed first `initialize` clears the workspace's pending marker and answers everyone queued behind it.
- Absent diagnostics while a file is unloaded are **unknown, not clean**: `backend/exec/diagnostics.go` refuses to read loader-state entries as findings and counts the file `unloaded`.
- Wire method IDs `0x05`-`0x08` are retired (they were `shell_open`/`shell_io`/`shell_resize`/`shell_close`) and **must not be reused** — a stale bridge binary must get a clean "unknown method" error, not a wrong handler.
- The only `-p` publishing on a sandbox container is **user-configured** forwarded ports (`forwardPortSpec` binds `127.0.0.1`), never the bridge; container ports outside 1-65535 are skipped.
- **Build:** `make bridge` — `CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o sandbox/sandbox-bridge ./pkg/bridge/agent`. The artifact is **committed**, and it is what a source checkout hands to a container, so an edit under `pkg/` that is not re-built is an edit that never runs: `make bridge-check` fails when the committed binary is not a build of this tree. **Discovery** (`pkg/sandboxruntime/runtime.go`): `MIKE_AGENT_PATH`, then `sandbox-bridge` / `sandbox/sandbox-bridge` next to the executable, then the same paths in the CWD; container creation fails if not found. **Protocol:** binary framing — 9-byte header (MsgID + Type + Length) + typed payload, Chunk frames stream before the final Result/Error, `MethodLSP` (0x0A) is the JSON-RPC passthrough.

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
- **The TLS-vs-plain first-byte sniff must not run inside `hybridListener.Accept()`** (`backend/server/server.go`): `net/http` accepts **serially**, so sniffing there lets one peer that connects and sends nothing stall every later handshake, and returning that peer's EOF/reset reads as a dead listener, ends `Serve` and kills the process. `Accept()` waits on a queue that a per-connection sniff goroutine fills — no worker pool, since N workers still block everyone at N silent peers. Sniffed-out peers are closed and logged per host, never handed to `net/http`; only the listener's own accept error may end `Serve`. Tests: `backend/server/listener_test.go`.
- **Never `log.Fatalf` on a `Serve()` error** (`main.go`): run the signal cleanup path (flight recorder, `srv.Shutdown`) and then exit 1 — `log.Fatalf` skips that cleanup and makes any listener error an abrupt exit.
- **SIGTERM must end the process.** `srv.Shutdown` runs under `server.RunShutdownWithDeadline` (`backend/server/shutdown.go`, 15s): every step waits on something that cannot be interrupted (a container that is not answering podman, an LSP bridge whose peer died), and a shutdown that hangs leaves the process **holding the data directory open** — it re-creates `kanban.db`/`-wal` after whatever deleted them, so the next run boots on the previous run's database. A bounded *context* bounds nothing here; the deadline is enforced by leaving anyway. Each step logs `shutdown: <name>` before running, so the last line names what wedged. Tests: `backend/server/shutdown_test.go`.
- **A certificate is not the gate** — reachability is `MIKE_LISTEN` + Host pinning (`MIKE_ALLOWED_HOSTS`), not the SAN set. Weak-password/non-loopback startup rules: `docs/tls.md`.
- Tests: `backend/tls/tls_test.go`, `backend/server/handler_tls_test.go` (the CA download offered by Settings → About).

### Required Environment Variables
Every `MIKE_*` below also has a `--flag` twin named after it (`--port`/`MIKE_PORT`, `--listen`, `--data-dir`/`MIKE_DATA_DIR`, `--no-tls`, `--tls-cert`/`--tls-key`, `--allowed-hosts`, `--worktrees-dir`); **flags take priority over env vars**. The one non-obvious flag: `--worktrees-dir` (`MIKE_WORKTREES_DIR`) is a runtime override that is **not** persisted to the DB `workspace_dir`.

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

Test repos live at `/tmp/kanban-test-repo` (+ `-2`), worktrees at `/tmp/kanban-test-worktrees/`. The registry is pinned to a local fixture (`frontend/tests/registry-fixture.json`) so registry e2e never hits the hosted registry.

Test servers are spawned via `go run .` and **inherit your shell environment** — export `MIKE_ENCRYPTION_KEY` before running e2e, or global-setup fails with "Server on port 3000 did not start within 15000ms".

**A run must leave nothing behind**, and that is an enforced property, not a nicety: `globalTeardown` is `tests/global-teardown.ts`, whose default export is *only* teardown (it used to name `global-setup.ts`, whose default export is `main()` — so every run ended by running **setup again**, spawning a fresh server pair + lock + database that nothing would ever stop; the next run then booted on a *used* DB with the previous run's extensions, providers and tasks). Verify after any change to setup/teardown: `pgrep -xc kanban` → 0, nothing listening on 3000/3001/9999, no `/tmp/kanban-test-data*`. teardown stops servers by **identity** (`--data-dir <dir>`), because they are `setsid go run .` grandchildren — the port owner is not the process that should stop, and `lsof -ti:PORT` also answers with clients.

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
- A few tests shell out to a real `git commit` without `-c user.*` flags (e.g. the `backend/managers` remote-fixture tests), so the machine needs a git identity (`git config --global user.email …`) or they fail with `Committer identity unknown`. Do **not** also set `init.defaultBranch` to `main` globally: fixture repos then collide with their own `git worktree add -B main` and a batch of managers tests fails in setup, which looks exactly like a regression.
- Sandbox tests need `runsc` **and** a working podman storage: under a nested/overlayfs root they all fail with `'overlay' is not supported over overlayfs, a mount_program is required` (podman exit status 125). That is environmental, not a regression — verify with `podman info` before reading anything into it.
- The Pi agent adapter's unit tests live in `../MIKE-extensions/agent/pi` and are run there — they are **not** part of `./backend/... ./pkg/...`.

### E2E Tests
Specs in `frontend/tests/e2e/` (helpers in `helpers.ts`); **mock LLM by default** (`frontend/tests/mock-llm.js`, port 9999, fixtures in `frontend/tests/fixtures/`). Conventions that bite:
- **Config-modifying specs** (settings, models, shortcuts, mode select, …) must run in the `isolated` project — add the spec name to `ISOLATED_SPECS` in `frontend/playwright.config.ts` (one array; the chromium lane's `testMatch` is derived from it, so the two lanes cannot drift).
- **Heavy fixtures get a dedicated repo** — never write big files into the shared `/tmp/kanban-test-repo`, where another spec's `git add -A && git commit` races with them.
- `test.describe.serial()` + `Date.now()` identifiers for shared state; wait for selectors, not delays (drag-and-drop is the known exception).
- **Every spec that touches the board owns its project** via `isolatedProject()` + `gotoProject()` (`helpers.ts`) — the lane used to share global-setup's one project across ~9 concurrent specs, and two runs of identical code then differed by 15 failures. It is keyed at the *worker*, never the file, because `fullyParallel` spreads one file across workers. Specs asserting on the **global** view (sidebar project list, file browser, add-project flow) must not use it, and must never do `.project-item` + `.first()` — use `selectProject(page, id)`. `serial` amplifies any leftover coupling: one failure skips its whole block as "did not run".
- **`workers: 4` is measured** (see `playwright.config.ts`): 6 is ~10% faster and buys 1-4 rotating turn-queue failures per run.
- **A spec that runs an executor TURN owns its own project** — turns are queued per project, so on the shared project a fork's answer waits behind every other worker's task and the spec times out waiting for the task to go idle (`.btn-send` never returns). Same for anything that mutates the project: `git add`/`commit`, or its **sandbox configuration**. Concurrent git in the shared repo makes *other* specs lose their file listing (the listing is a live `git status` that never retries after an error), and flipping `sandbox_enabled` makes the project explorer seed the **container** mount path and ask the host for it — 400 `path outside allowed project directory`, panel stuck on the error.
- **Pin the mock LLM's fixture with `llm-fixture:<name>` in the prompt.** Keyword routing is unreachable from an ordinary task prompt — the plan-first skill is in every system prompt, so plan routing answers first and a spec asking for "edit" silently gets `create_task`. Convention + the two measured chat behaviours (`.awaiting-llm` = "request sent, nothing back yet", absent while streaming; a task auto-starts, so wait for idle and assert on the entry that appears *after* your send): `docs/testing.md`.
- **`[role="dialog"]` is never a unique locator** (BaseModal, both explorer views, `TerminalPanel`, `CommitDiffOverlay`, and every ChatMessage's `<dialog>` all have it) — filter by the content that identifies the dialog you mean. Selector-by-component and the commit/review-bar rules: `docs/testing.md`.
- **A spec exists only if it needs a browser or the real backend** (DnD, real focus/keyboard, scroll/CSS geometry, iframes + CSP, git/worktrees, WS reconnect). Component state belongs in vitest — 1.4k assertions in ~20 s versus a page-load tax per spec. And **never write a `readFileSync('../Foo.svelte')` + `toContain(...)` "test"**: it cannot fail a real bug and breaks on renames. The suite is budgeted at a few minutes for a full run; keep it there.

Fixture format and recording, the dedicated-repo recipe, the local-git-identity requirement, mock-LLM keyword routing, ad-hoc `.mjs` probe scripts: **`docs/testing.md`**.

## Architecture

### Client-Server Boundary
- **The frontend ONLY talks to the Go server** (`/api/...`) — it never calls an LLM endpoint directly; all LLM traffic (chat, model listing, `POST /api/config/fetch-models`) is proxied through Go.
- All realtime is WebSocket through Go. Inbound frames are `stream.AppStreamMessage` (`Data` is `json.RawMessage`); **outbound** frames are flat maps built where they're broadcast (no shared envelope struct in Go) — a `task_event` carries `task_id`/`event` beside `data`, so the client reads `msg.task_id`, **not** `msg.data.task_id`. Use optimistic UI for user messages (add immediately, skip the echo).
- Model/provider changes ride a `user_message` with optional `model_name`/`llama_server_url`.
- **Provider routing:** `PUT /api/config` is a partial update. Two required pairs: `worker_provider`/`worker_model` (chat; resolved frontend-side by `resolveWorker` in `$lib/utils/providers.ts`) and `utility_provider`/`utility_model` (titles, summaries, commit messages, checkpoint labels — `utilityTarget()` in `backend/exec/executor.go`, falling back to the task's own URL/model).

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

- **Tool writes are jailed to the worktree's `os.Root`.** Parent-directory creation for `write_file`/`edit_file` goes through `ensureRootDir` → `root.MkdirAll` (`backend/exec/tools.go`), **never** `os.MkdirAll(filepath.Join(worktreePath, path))`: the host-side join resolves `..` and creates `<parent-of-worktree>/x/y` *before* `root.Create` rejects the path — arbitrary directory creation as the server user, reachable from prompt injection in repo content. An escaping path must fail up front and create nothing; new dirs are `0700`.
- **Agent subprocesses get an environment allowlist.** `SafeAgentEnv` (`backend/agent/env.go`) is the only source of `cmd.Env` for an adapter's subprocess (`runner.go`) and its `--list-models` probe (`loader.go`): `PATH`/`HOME`/user/term/locale/proxy/trust-store (+ `LC_*`) plus the adapter's own extras (extras last — `os/exec` keeps the last occurrence). `os.Environ()` leaked `MIKE_LOGIN_PASSWORD` and `MIKE_ENCRYPTION_KEY` via `/proc/self/environ` to an extension-supplied binary, so **never** `cmd.Env = os.Environ()` here; add a variable only if an agent genuinely needs it.
- **Tests:** `backend/exec/tools_test.go`, `backend/agent/env_test.go`.

### Git Argument & Transport Hardening
- **Client-supplied refs are validated, then fenced.** `types.ValidateGitRef` (`backend/types/validate.go`) is deliberately tighter than `ValidateBranchName` (no `~ ^ : ? * [ \`, space, leading `-`). `GET /api/fs` validates `?ref=` **and** passes `--end-of-options` (git ≥ 2.24) before `<ref>:<path>` in `git show`, so a ref can never parse as an option (`--output=/tmp/pwn` would create/truncate a file anywhere the server user can write).
- **Clones speak only allowed transports.** `git.CloneArgs` puts `-c protocol.ext.allow=never` *before* `clone` (so the process parsing the URL gets it) and `git.CloneEnv` sets `GIT_ALLOW_PROTOCOL=http:https:git:ssh:file` + `GIT_TERMINAL_PROMPT=0` — enforced by git, independent of the user's `git config`. `git.ValidateCloneURL` mirrors it host-side: no leading `-`, no control characters, no remote-helper `::` transport (`ext::sh -c 'id'`), and a **case-sensitive** scheme check because git's own is (`HTTPS://` is refused as transport `HTTPS`). **Every** clone of a caller-supplied source goes through both halves — project creation (`managers.validateGitCloneURL`) and the extension install (`extensions.CheckoutExtension`). Git's own refusal of `ext::` is a host default, not a defence (a host with `protocol.ext.allow=always` would run the payload as the server user). Use `git.CloneProtocolArgs()` for a clone that needs a bespoke flag — never drop the fence.
- **One slugifier for every name derived from user text.** `types.Slugify` / `types.SlugifyBranchName` (`backend/types/slug.go`, `MaxBranchName` / `BranchSlugMaxLen`) back task branch names, the branch GC and Main-thread recovery, and project IDs; the matching *parser* `types.TaskBranchSlugPrefix` (with `TaskBranchSuffixLen`, the random tail `exec` appends) lives beside them — a GC that parses a different shape than the executor writes deletes live branches. Never add a second copy of either. Non-ASCII letters are dropped, not transliterated.
- **Tests:** `backend/types/validate_test.go`, `backend/types/slug_test.go`, `backend/server/handler_fs_test.go`, `backend/git/clone_test.go`, `backend/managers/project_test.go`, `backend/extensions/registry_test.go`.

### Keyboard Shortcuts (Settings → Preferences)
Persisted as `keyboard_shortcuts` JSON in the `config` table through the shared Settings Save/Cancel flow. Adding one is a 7-layer change — recipe: **`docs/adding-a-preference.md`**.

- Defaults + helpers in `$lib/utils/keyboard.ts`: `create_task` `C`, `create_task_submit`/`start_task` platform Ctrl/Cmd+Enter, `open_terminal` Ctrl+`, `toggle_fullscreen` Ctrl+Up, `multi_select_click` platform Ctrl/Cmd+Click. Platform-aware defaults are `default*()` functions, **not** `DEFAULT_SHORTCUTS` entries.
- **Format:** normalized `control`+`meta`+`shift`+`alt`+key; absent = default, `""` = disabled. The key may be the pseudo-key `click` for a **mouse binding** (`"shift+click"`; `normalizeClick` / `matchesClickBinding` / `effectiveMultiSelectClick` / `isBrowserLinkClick`) — only the primary button is bindable and an unmodified click is never a binding.
- `PUT /api/config` **replaces the whole object** — always send **all** bindings; omitting one resets it to default.
- **Capture** (`normalizeKey`) rejects modifier-only presses, Escape, unmodified named keys and browser-reserved `Ctrl+W/Q/N/T`. **Activation:** global in `use-keyboard-shortcuts.svelte.ts` (wired in `App.svelte`), dialog/panel-scoped in the owning component.
- **Tests:** `backend/config/config_test.go`, `utils/__tests__/keyboard.unit.ts`, `preferences-tab.unit.svelte.ts`, e2e `preferences-keyboard.spec.ts` (isolated).

### UI Preferences (Settings → Preferences → UI)
Top-level `*bool` config fields (`parseBoolPtr` in `GetConfig`, `omitempty` in `configResponse`) with the same Save/Cancel plumbing as shortcuts. Example: `hide_model_badge` gates `.model-badge` in `Card.svelte`. Full recipe + tests: **`docs/adding-a-preference.md`**.

### Keyboard Navigation (board + task view)
Arrow keys move **real DOM focus** (the global `:focus-visible` ring) — never a separate "highlighted" store. Board: a 5-column ring `[Projects][Backlog][In Progress][Review][Done]`, Left/Right skip empty columns and never wrap, Down/Up wrap within a column. Task view: `[Board][Chat][Diff]` cycle Left/Right; plain arrows in editable focus keep native text behavior, **Ctrl+Alt+Left/Right** switches from anywhere. Handlers dispatch through the pure resolvers `resolveArrowTarget`/`resolveOverviewArrowTarget` (`composables/use-board-arrow-nav.ts`) and `resolveDetailColumn`/`resolveDiffFileTarget`/`canSwitchDetailColumn` (`composables/use-task-detail-column-nav.ts`) — don't inline target logic in components. The project focus ring is an **inset box-shadow** (ancestors clip outlines). Detail + tests: `docs/navigation.md`.

### Link-like Rows (projects & task cards)
Sidebar project rows and task cards are real links, so the browser's own link actions work on them (open in new tab, middle click, copy link address, status-bar URL) using the same deep links the app writes (`?project=`, `?project=&task=`).

- **Project row** = `<a class="project-item" href="/?project=<id>" role="button" tabindex="0">`; unmodified click `preventDefault()`s, **any modifier click is left uncancelled**. A cloning project gets **no href**. `role="button"` means Space is handled explicitly.
- **Task card** root stays `<div class="card" role="button" tabindex="0" draggable>` with a full-cover `<a class="card-open-link" aria-hidden tabindex="-1" draggable="false">` overlay (`inset:0; z-index:1`) as last child — a root anchor is impossible (nested subtask `Card`s → anchor-in-anchor).
- **Stacking contract** (`Card.svelte`): `.card-title` is deliberately NOT lifted so the overlay still covers the title; instead `.card-actions`, `.subtask-progress-bar`, `.subtask-list` carry `z-index: 2`, and `.card.queued > *` is scoped `> :not(.card-open-link)`.
- **Never** re-tune these rows with `<button>`/`<div>` + a faked context menu — the link menu is DOM-level and unsynthesizable. Keep selectors tag-agnostic (`.project-item`, not `button.project-item`).
- Detail, click routing, known limitation, tests: `docs/link-rows.md`.

### Reconnection & Offline
`onConnected(true)` in `App.svelte` runs `silentRefresh()` once `initialLoadComplete`; `TaskChat` reloads history from the DB on reconnect because **turns completed while offline are never replayed** (buffer cleared at turn end). `offline` queues writes immediately; `online` flushes the queue and calls `reconnectNow()` to skip backoff; `visibilitychange` refreshes the board (phone-sleep). Project re-subscription forces a re-send of `task_diff_stats`. Mechanism + test approach: `docs/reconnection.md`.

## Extensions

Types: `lsp` (language servers in gVisor sandboxes), `ui` (frontend injection), `agent` (AI agents as subprocesses via WASM adapters).

**Key files:**
- `backend/extensions/` — domain types, `store.go` (SQLite), `registry.go` (FetchIndex/FetchManifest/PullImage/BuildImage), `sandbox.go` (refcount lifecycle), `handler.go` (REST), `changelog.go`, `manifest_validate.go`
- `backend/lsp/extension_manager.go` — extension-driven LSP discovery (LSP availability = installed extensions)
- `frontend/src/lib/services/extension-manager.svelte.ts`, `ui-injector.svelte.ts`; `$lib/api/extensions.ts`; `ConfigModal/ExtensionsTab.svelte`

**REST:** `/api/extensions` (list/get/update/delete), `PATCH /{id}/enabled`, `GET /registry`, `GET /local`, `GET /check-updates`, `GET /{id}/logs`, `GET|PUT|DELETE /{id}/storage` (the namespaced KV store behind `extHost.storage.*`), `PUT /api/extensions/{id}/dependency-cache` (the one host directory an LSP extension reads its resolved dependencies from — see *Mount a language server's dependencies*), `POST /{id}/proxy` (the **only** extension egress path; enforces the manifest's domain allowlist). The three long-running operations (`POST /install`, `POST /install-local`, `POST /{id}/update`) answer as **SSE** streams (`streamOperation`); there is **no** build-progress route — build/update output reaches the browser as `build_progress` **task WebSocket** events (`types.WSTypeBuildProgress`, from `exec/loop_helpers.go`).

**Deleting an extension deletes its directory.** `extensions/<author>/<name>` is not a cache: UI extension code is served out of it, agent adapters' WASM and binaries are loaded from it, and `GET /api/extensions/local` lists it as installable — so a row that is gone with files still present is a deleted extension that keeps executing and comes back under its own name carrying the manifest it was installed with (that is how a stale 0.7.0 gopls manifest outlived its reinstall). `removeExtensionFiles` (`backend/extensions/remove.go`) derives the path from the **stored record** through `extensionDir` — never from a request field — and the path is partly a manifest's own words, so the recursive delete is contained twice over: lexically inside the extensions root and again with symlinks resolved (`extensions/<author>` being a link elsewhere must not become a delete of the place it points at). An author folder that empties out goes too. Removal runs **after** the row is deleted so an unremovable directory cannot make an extension undeletable, and it is then reported as `leftover` in the response (rendered by `ExtensionsTab`) plus a `LEFTOVER FILES` log line, because files nobody in the UI can see any more are still being served. Updates never had this problem: `CheckoutExtension` removes the destination before writing the new commit. Tests: `backend/extensions/delete_files_test.go`. Plus the preview endpoints below.

**A row that outlived its files is repaired at startup, from its own pin.** The same fact has the other direction: a missing `extensions/<author>/<name>` with its row still present is an **enabled extension that does nothing, invisibly** — a UI extension's iframe 404s, so its document never boots and never registers the footer button or overlay it exists for, while `GET /api/extensions` still reports it installed and enabled. `RepairMissingFiles` (`backend/extensions/repair.go`) walks every row once when the app starts — from `main.go`, off the boot path, never joined to shutdown — derives each directory through the same `extensionDir` the delete path uses, reads "no readable `manifest.json`" as missing (a half-written checkout has a directory), and re-checks out the **record's** pinned commit with `CheckoutExtensionContext`: never a newer one, which is an update the user asks for. A row with no `repo`+`commit` (`local` installs are materialized in place, so their files *were* the install) or a name that is not a directory name is logged, never fetched. Detection runs only at startup, so files deleted while the server runs stay broken until the next start — that is what the `[extensions] repair:` lines are for. Tests: `backend/extensions/repair_test.go`.


**The LSP sandbox borrows Sandbox Configuration mounts, read-only.** `ExtensionSandboxManager.MountPolicy()` adds the host paths every project declares in its own Sandbox Configuration (`ListProjectSandboxMountPaths`) as roots, and `MountPolicy.ForceReadOnly` pins anything under one of them to `ro` even when the stored config asks for `rw` — the extension did not decide that path exists. `ExtensionHandler.mountPolicy()` (install + `PUT /api/extensions/{id}`) deliberately does **not** get those roots, so an extension can never self-grant one. `adaptProjectMounts` mirrors the *union* over projects (global paths like `/go` for everyone, task-root-relative ones only for their owning workspace) so a new project changes the mount set once instead of on every switch.


**`environment` is declared, but core does not enforce it yet.** `capabilities.lsp.sandbox.environment: {check, suppress_codes, note}` lets an extension say how to tell whether it can resolve a workspace, and which diagnostic codes mean "could not load" rather than "this code is wrong". Nothing calls `ExtensionLSPManager.Environment` (`backend/lsp/environment.go`), so no verdict is ever recorded: no finding is dropped, and the `environment` field of `GET /api/extensions` — rendered by `ExtensionsTab.svelte` — always answers healthy. What exists is sound when it runs: `check` is a bridge `bash` exec in the extension's own sandbox with cwd = that workspace's container path (`envCheckCommand` quotes the argv through the lexer rules, so a manifest cannot become shell syntax) and verdicts cache 60 s per workspace. **A check must be able to fail** — `go list -e -deps ./...` exits 0 on a package graph it could not load, so it reported "healthy" from a container with an empty module cache and a full one alike; the bundled gopls check is `GOPROXY=off go list -deps ./...` (measured: 0 with the cache mounted, 1 without). Either call it from `backend/exec/diagnostics.go` or delete the chain, but do not describe it as live. `reload_files` (e.g. `go.mod`) **is** wired: a turn that changes one recycles **that workspace's** connection only (a language server has one root, so it already owns a connection of its own), because a server over a bind mount gets no file-watch events.

**Mount a language server's dependencies; never discover where they live.** An extension declares `capabilities.lsp.sandbox.dependency_cache: {container_path, env}` — where *it* looks inside its own container, and the variable it reads that location from. There is **no `read_only` knob**: the mount is read-only by definition, and a stale manifest or stored record that still carries the key decodes and is ignored. The host directory is a **human's** choice, one per extension, configured in that extension's **Edit** dialog (an enabled extension that declares a cache with nothing granted carries a yellow dot on Edit and a "Needs setup" badge, and the field rides the dialog's **Save** — Cancel must mean nothing changed; a **Browse** card for such an extension badged "Needs setup" plus a note that setup follows installation, because a not-yet-installed extension has no grant to be missing and the step is worth naming before the click), stored beside the declaration as `sandbox_config.dependency_host_path` (two fields because the two origins are the security argument; a manifest carrying it is **refused**, not stripped). That is what replaced "ask the toolchain where its cache is": a discovered path needs a container to ask it in, so the first turn of every workspace pays for a server started to answer `go env GOMODCACHE` — and it puts extension-supplied stdout on the host command line, which is why `detect`/`sandbox_probe` and the `LSPParams.Env` wire field were removed. The rules that keep it honest:
- **One derivation, both sides.** `dependencyBind` (`backend/extensions/sandbox.go`) produces the `-v` spec and the `-e` assignment for a container create *and* the snapshot a later `MountsCompatible` compares against (`withDependencyMount`/`withDependencyEnv`, and `DesiredDependencyMount` for the LSP manager's desired list). Two derivations would drift, and a container that predates a grant would be called compatible with it — the grant doing nothing, silently, forever.
- **The grant reaches that one extension's sandbox and nothing else.** `dependencyBind` mounts it `ro` and `SandboxOpts` (`backend/sandbox/sandbox.go`) has **no field for it**: a task, a sandboxed terminal and the project container never bind it. They used to, read-write, which covered the project image's own toolchain cache at the same container path (a `golang:1.25` image keeps its prewarved modules at `/go/pkg/mod`) and made one install-wide text field a host write surface for every project's arbitrary code. A project that wants a task to fill a dependency directory says so in its own Sandbox Configuration — Custom Mounts plus Environment Variables — and both dialogs say that out loud.
- **The assignment is a container `-e`, never a per-connection one.** The extension's own `environment.check` is an exec in that container and must be told the same thing the server is; a per-start env would leave the check reporting failure with the mount sitting right there, turning an honest gate into a false one. A language server's environment is therefore decided once, at container creation, in one place.
- **A grant is a root for one extension only.** `MountPolicyForExtension(ext)` adds it; `MountPolicy()` — the one install and `PUT /api/extensions/{id}` validate against — never contains anyone's grant, so an extension still cannot self-grant a thing. `ValidateDependencyGrant` is deliberately *not* `MountPolicy.ValidateHostPath` (the directory lives exactly where that allowlist does not reach): absolute, no `..`, symlinks resolved before storing, not `/`, and existing as a directory — re-checked at bind time, because a directory deleted after the grant would otherwise be recreated by podman root-owned.
- **Core's container path is not borrowable, and `ro` is not a favour.** `adaptProjectMounts` drops a project mount that wants it (`isReservedContainerPath`) and `buildExtensionCreateArgs` drops a manifest mount that wants it: two `-v` specs on one path make podman refuse the whole create, which reads as a broken extension. Anything borrowed from a project's Sandbox Configuration is pinned read-only whatever the stored mode says, because the extension did not decide that path exists. `extensions.ShadowsToolState` is the single rule for "that is the server's own writable state", used by borrowed mounts and by the declaration's validation — a language server with a read-only cache publishes nothing about a file it cannot load, so a clean file is reported for a broken one.
- **A grant is revoked by an empty string** on `PUT /api/extensions/{id}/dependency-cache`, which also stops the sandbox (the mount set is fixed at creation) — an empty field on the sandbox form would mean "unchanged", which is the opposite of a revoke. Applying the same path twice writes nothing and broadcasts nothing. The Edit dialog sends the grant **before** `PUT /api/extensions/{id}`, so a refused path cannot silently save the rest of the dialog.
- **Setup is declared in two shapes, read by one helper.** The registry index *projects* the declaration up to `capabilities.lsp.dependency_cache` (`scripts/gen-registry.py`), while `GET /api/extensions/local` answers with the manifest as written (`capabilities.lsp.sandbox.dependency_cache`). `dependencyCacheDeclaration()` / `requiresSetupAfterInstall()` (`frontend/src/lib/utils/extension-permissions.ts`) read both, and they are the only readers: the Browse card's badge and note and the install review dialog all answer from them, so a card cannot be silent about a local extension that declares one. `needsDependencyGrant(ext)` is the *installed* card's state (it has a stored record to be missing a grant); Browse has no such record, so its badge is a static span, never a button that opens nothing.
- **A dependency cache is optional by schema, mandatory by consequence.** The declaration is `omitempty`, and a grant is never required nor auto-detected — but an extension sandbox runs `--network none` and ships no cache, so without a grant a workspace with third-party imports cannot be resolved (gopls: `BrokenImport`, `UndeclaredImportedName`, `Unavailable`) and every turn argues with phantom errors. The alternative is that the project **image** carries the dependencies, which is what an `environment.check` is for (gopls: `GOPROXY=off go list -deps ./...`, which cannot fetch); `environment` lives beside `dependency_cache`, under `capabilities.lsp.sandbox`.

Tests: `backend/extensions/dependency_manifest_test.go`, `dependency_grant_test.go`, `dependency_policy_test.go`, `dependency_mount_test.go`, `bundled_manifests_test.go` (cross-repo: every bundled manifest validates, declares an actionable note, and can fail), `reload_env_test.go` (reload only — the environment half is unwired), `backend/sandbox/containerenv_test.go`, `backend/exec/sandbox_create_opts_test.go` and `backend/term/terminal_test.go` (both pin that a task/shell container carries **only** the project's own configuration), plus the card states in `frontend/src/lib/utils/__tests__/extension-permissions.unit.ts` and `frontend/src/lib/components/ConfigModal/__tests__/extensions-dependency-cache.unit.svelte.ts`.

**LSP capability gates** (`manifest.capabilities.lsp`, both default to enabled, both toggled per extension in Settings → Extensions, and **independent** of each other): `editor_enabled` gates the *browser's* LSP features — the server only stores and serves the flag, while `extension-manager.getEditorLSPExtensions()` filters, `editor-language.resolveFile()` answers per file and `lsp-connection-manager.isLSPAvailable()` decides whether a channel opens. `tool_calls_enabled` gates the *agent's* LSP tools and is enforced server-side (`ExtensionRegistry.IsToolCallsEnabledForFile`, checked before an LSP tool runs).

**UI injection:** extensions activate per scope; components call `renderSlot(scope, context)` on mount. Scopes → components: `task-card`→Card, `file-editor`→FileEditor, `project-board`→Board, `task-chat`→TaskChat, `project-sidebar`→Sidebar, `file-browser`→dir picker modal, `create-task`→CreateTaskModal.

### Language Knowledge Lives in Extensions
The core holds **no** language names, keyword lists, comment markers or bracket tables — neither in Go nor in the frontend. Why the design is shaped this way, and its tests: `docs/languages.md`.

- **`file_types[]`** (`{extension, language_id, label, syntax}`) is the routing table *and* the source of the `didOpen` languageId and the status-bar label. `language` is the **routing** key (which server owns the file); `language_id` is **per file type** — collapsing them breaks routing or the parse. Both sides read it through exactly one helper: `extensions.LSPConfig.fileTypeFor` (`MatchesFile`/`LanguageIDFor`, `ServerConfig.LanguageIDFor`) in Go, `editor-language.ts::resolveFile` in the browser — **never** a local map, and the browser's LSP channel takes its routing key from that same call.
- **`syntax{}`** is a grammar as **data, never code** (a shipped module would run in the app's realm). `frontend/src/lib/syntax/spec-language.ts` compiles a profile into a `StreamLanguage` + bracket/region `foldService` + `indentService` + `commentTokens`/`closeBrackets` + bracket pair colorization (from `pairs`). A name the word lists do not cover gets its colour by **shape** — `name(` is a function, an upper-case-first name is a type, everything else the default — which is why `types.X` and `DoThing()` are coloured at all; both rules default on and each profile declares them (`call_as_function`, `capitalized_types`, `*bool` in Go so an explicit `false` survives the round trip). Bracket colour is the pair's nesting level (VS Code's rule, `syntax/bracket-pairs.ts`), themed as `.cm-bracket-l0|1|2`. **Every scan that reads characters shares one reader** for "this range is not code" (`syntax/stream-tokens.ts`) — fold, indent, brackets — or a `"{"` becomes a bracket in one place and not another. A spec-built grammar is named `mike-declared` — deliberately not a valid language id, so a call site that forgets an explicit id gets a loud "no provider" instead of silently wrong diagnostics. Tests: `frontend/src/lib/syntax/__tests__/`, `backend/extensions/manifest_validate_test.go`, `bundled_manifests_test.go`.
- **A bad manifest is refused, not stored** (`backend/extensions/manifest_validate.go`, on install, update and `PUT /api/extensions/{id}); **legacy manifests are normalized at load**, not migrated (`LSPConfig.NormalizeFileTypes()` in `scanExtension`), so runtime code sees one shape.
- **The one language table left in core** is `frontend/src/lib/utils/fallback-languages.ts` (json, markdown, html, css/scss/less, yaml, sql, `tmpl`/`gohtml`) — consulted only when no extension declares the file type. `highlighter.ts` (diffs) resolves through the same `getLanguageSupport`, so a diff and an editor can never disagree.

### UI Extensions
UI extensions are **plain JavaScript modules — not Svelte components** (external bundles carry their own Svelte runtime → `effect_orphan`). A module default-exports `init({ component, config, ...scopeContext })` and returns a cleanup `() => void`; no Svelte imports, DOM + `createElement('style')` only. Serving, CSP, listing, install model, bundling and tests: `docs/ui-extensions.md`.

- **Source lives in `../MIKE-extensions/<type>/<name>/`;** `registry.json` is **generated** — `python3 scripts/gen-registry.py` after editing a manifest or bumping a commit (`--check` in CI).
- **The host CSP blocks inline `<script>`** in an extension's HTML — load code as external files.
- **Channel handshake (document identity):** the host mints a per-mount token, passes it as `?extch=`, and arms the channel only on the document's `exthello` echo — `origin === 'null'` + `ev.source` identify the *frame*, not the mounted document. Post nothing before arming (except `exthello-request`); a `call` from an un-armed frame is refused **without a response**; a 2nd+ `load` **disarms** and re-requests hello (never tears the frame down); **nested** iframe loads never count. `frontend/public/host-client.js` answers `exthello` on every load — a hand-rolled client that doesn't hangs at `extHost.ready()`. Test: e2e `extension-frame-channel.spec.ts` (isolated; fixture must use external scripts).

### Preview capability
Host support for UI extensions that render the open file (`html-preview`: overlay + live refresh, root-relative refs rewritten server-side to the token's worktree root). Full mechanism, CSP tiers and nav-record semantics: `docs/preview.md`.

- **The token is the credential** — 128-bit from `crypto/rand`, and `newPreviewToken` is **fail-hard**: a serve request is rejected on error rather than falling back, because a guessable token is an unauthenticated read of the document plus every sibling under its asset root. Limits: 10-min idle TTL, 32 docs, 10 MB/doc.
- **`project`/`path`/`task` come from the host's own injection context** (FileEditor passes `filePath`/`projectId`/`taskId`), **never** from extension arguments — an extension cannot point the server at arbitrary directories.
- **`GET /api/preview/{token}/…` is public**; the doc carries its OWN CSP that `CORSMiddleware` skips — strict zero-egress by default, permissive only when the serve request sets `allow_network`.
- **The doc stays in a nested bare `<iframe sandbox>`** inside the extension's sandboxed iframe (scripts on only via the extension's `allow_scripts`); no `frame-ancestors` (it must be frameable).
- **In-tree allow-list:** the extension can't read the nested frame's URL, so a pending-load counter consults `GET /api/preview/{token}/nav` — a *fresh* `ok` record (`seq > lastNavSeq`) means the server served an in-tree file → allowed; stale record, error or host-call failure → **contained** (frame reset, token released, warning modal).

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

### Commit and Move to Done — unstaged/untracked files
- "Commit and move to done" (`HandleCommitAndDone`, `backend/managers/task.go`) commits **only staged** changes. Untracked (never-staged) files must never be copied into the project's main checkout or merged — they stay in the worktree and are discarded when the task is deleted.
- The merge-conflict resolution commit (`commitConflictResolution`, `backend/exec/merge_conflicts.go`) stages with `git add -u` (tracked only), **NOT** `git add -A`.
- **A merge conflict interrupts the click; it does not cancel it.** The click asked for commit + merge + Done, so the turn that finishes resolving merges into the target branch **whatever `auto_merge` says** (`auto_merge=false` is why the user clicked it) and marks the task Done.
- **Only the click knows it was the click.** A conflict found at task start means "keep working on the task", so `ArmCommitAndDone` / `takeCommitAndDone` (`backend/exec/commit_and_done.go`) records which conflict the click produced — **in memory**, so a merge nobody just asked for never comes back. It is spent by the resolution it was waiting on, dropped when the user takes over (`HandleSend`, the `ask_question` stop — the merge-conflict prompt invites that question), and kept across a resolution that is *still* conflicted. **Nothing is marked Done that did not reach the target branch:** `conflictResolution.finishesTask()` is the only road to Done from a resolution, so a refused or failed merge is Review with `Merge failed: …`.
- When the worktrees dir lives **inside** the project, `CreateWorktree` (`backend/git/worktree.go`, `ensureWorktreesExcluded`) idempotently appends it to the repo's local `.git/info/exclude` so worktree content never shows in project `git status` or gets swept into a project-level `git stash -u`. Local-only, never committed.
- **Tests:** `task_test.go` (`TestHandleCommitAndDone_ExcludesUnstagedFiles`, `TestHandleCommitAndDone_ArmsClickForConflictResolution`), `merge_conflict_resolution_test.go` (`TestCommitConflictResolution_DoesNotSweepUntrackedFiles`, `…_CommitAndDoneClickMergesWithAutoMergeOff`, `…_NoClickNoMergeWithAutoMergeOff`, `…_StillConflictedKeepsTheClick`), `commit_and_done_test.go`, `backend/git/worktree_test.go` (`TestCreateWorktree_ExcludesWorktreesDirFromProjectStatus`, `…_NoExcludeWhenWorktreesOutsideProject`), e2e `commit-and-done-unstaged.spec.ts`.

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
| `docs/adding-a-preference.md` | the 7-layer recipes for a shortcut or a `*bool` preference |
