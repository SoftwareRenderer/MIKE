# AGENTS.md

## Repo Map
- `backend/` — Go server: API handlers, exec loop, LLM client, git/worktrees, sandbox, extensions.
- `frontend/` — Svelte 5 SPA (Vite). Built to `frontend/dist/`, served as static files by the Go server. No dev server in the normal workflow.
- `pkg/bridge/` — sandbox-bridge protocol/client. `pkg/sandboxruntime/` — podman/gVisor runtime + bridge binary discovery.
- `extensions/` — **installed** extensions (`<author>/<name>`), materialized by the registry installer. Don't hand-edit.
- `../MIKE-extensions/` — source repo for published extensions (`<type>/<name>/`). Its `registry.json` is **generated** — never edit by hand.
- `scripts/gen-registry.py` — regenerates `../MIKE-extensions/registry.json` from extension manifests.
- `Makefile` — build/run/test entry points. `data/` — SQLite DB, TLS certs, trace files. `prompts/` — LLM prompt templates.

## Required Dependencies

### gVisor
gVisor (`runsc`) is required for project sandboxing — task tool calls and terminal sessions run inside gVisor-isolated containers. Rootless only; podman sandbox commands run via `podman --runtime runsc`. Without it, sandboxed projects refuse to start tasks (hard error, no fallback). Sandbox unit tests require `runsc` and a working podman (see *Testing → Go Unit Tests*).

- **One availability probe, one stale-container sweep.** `sandboxruntime.GVisorAvailable(tag)` (runsc present + `runsc --version` OK + podman present) backs both `SandboxManager.checkAvailable` and the extension manager's probe — they must agree, or a task sandbox and an extension sandbox disagree about whether the host can run gVisor at all. `Runtime.CleanupStaleContainers(tag, nameFilter)` is the shared best-effort, 15 s-bounded startup sweep (prefixes `kanban-sandbox-` / `kanban-ext-`); neither manager should re-implement the list/parse/remove loop. Tests: `pkg/sandboxruntime/runtime_test.go`.
- **Reaching the host shell is opt-in:** a task/terminal runs on the host only for a project with sandboxing disabled, and such a project can be created (or switched out of the sandbox) only with `DANGER_ZONE=1` in the server's environment — otherwise the API returns 403 (`cannot create project with sandbox disabled — set DANGER_ZONE=1 to allow`). See `backend/managers/project.go`.
- **The terminal WebSocket verifies `Origin`** (`backend/term/terminal.go`, left on like the LSP endpoint): the browser must connect from the same host, so a third-party page cannot ride the session's `SameSite=Lax` cookie into an endpoint that hands out a shell. SameSite blocks the cross-site request first; this is the second lock, not the only one.
- **Extension sandboxes are held to a mount/capability allowlist** (`backend/extensions/mount_policy.go`): host mounts must resolve inside the policy's roots (workspace dir, registered project paths, the extensions dir, the system temp dir, plus `MIKE_EXT_MOUNT_ROOTS`), container paths must match `^/[A-Za-z0-9._/-]+$` (no `:` or `,`, which would rewrite the podman mount spec), and capabilities that reach past the container (`SYS_ADMIN`, `SYS_PTRACE`, `DAC_OVERRIDE`, `NET_ADMIN`, `MKNOD`, `SETUID`, …) are refused even when requested. Enforced both on `PUT /api/extensions/{id}` and at install time (a manifest that self-grants a bad sandbox is rejected, not persisted).
- **Plan mode's bash restriction is advisory by design** (prompt text in `prompts/plan-mode.tmpl`, not an executor-enforced allowlist): a read-only worktree mount + the sandbox is what actually holds a planning turn, so the wording guides the model rather than pretending to gate it.

### Sandbox Bridge
`sandbox-bridge` is a static Go binary that runs inside each sandbox container and services tool calls (read, write, edit, bash — plus a `bash_cancel` notification and the LSP JSON-RPC passthrough) over **the stdin/stdout of one `podman exec -i … /sandbox-bridge` per sandbox** — the process (and its connection) lives for the sandbox's lifetime, so tool calls don't pay for a `podman exec` each. There is **no listener and no port**: the agent (`pkg/bridge/agent/main.go`) wraps `os.Stdin`/`os.Stdout` in a `net.Conn` shim (`stdioConn`), the host side (`backend/sandbox/sandbox.go` → `sandbox.NewClient(stdin, stdout)`) speaks the same framing into those pipes, and nothing in the repo calls `net.Dial`. Logging goes to stderr only.

Interactive terminals do **not** go through the bridge: `sandbox.SandboxManager.startShell` runs `podman exec -it <container> <shell>` with a host-side PTY (`backend/term/terminal.go`), which is why the bridge protocol has no shell methods. Wire method IDs `0x05`-`0x08` are retired (they were `shell_open`/`shell_io`/`shell_resize`/`shell_close`) and must not be reused, so a stale bridge binary gets a clean "unknown method" error instead of a wrong handler.

The only `-p` publishing on a sandbox container is for **user-configured** forwarded ports (`project.ForwardPorts` / an extension's `forward_ports`), never for the bridge; `forwardPortSpec` binds the host side to `127.0.0.1` so a container's listener is never exposed on the LAN. Container ports outside 1-65535 are skipped.

- **Build:** `CGO_ENABLED=0 GOOS=linux go build -trimpath -o sandbox/sandbox-bridge -ldflags="-s -w" ./pkg/bridge/agent`
- **Discovery** (`pkg/sandboxruntime/runtime.go`), in order: 1. `MIKE_AGENT_PATH` env var, 2. `sandbox-bridge` / `sandbox/sandbox-bridge` next to the executable, 3. same relative paths in the CWD. Container creation fails if not found (required for sandbox mode).
- **Protocol:** binary framing over stdio — 9-byte header (MsgID + Type + Length) + typed payload; streaming via Chunk frames before the final Result/Error. `MethodLSP` (0x0A) is the JSON-RPC passthrough; each LSP extension runs in its own gVisor sandbox.

## Server Management

```bash
make run    # background dev server, no TLS (dev defaults: password=test, dev-only MIKE_ENCRYPTION_KEY)
make stop   # pkill the server
curl -sk https://localhost:3000/api/config   # verify (401 JSON = up); use http:// with --no-tls
```
Or manually: `./kanban &` (requires `MIKE_ENCRYPTION_KEY` and `MIKE_LOGIN_PASSWORD` exported). `make run`'s defaults are overridable by exporting your own vars — export your real `MIKE_ENCRYPTION_KEY` if stored secrets in `data/kanban.db` must decrypt.

### HTTPS / TLS
TLS is **enabled by default**, signed by a local CA created on first start in `data/tls/`: `mike-ca.crt`/`mike-ca.key` (self-signed root, `O=MIKE, CN=MIKE Local CA`, 10 years) signs the server certificate `mike.crt`/`mike.key` (`O=MIKE, CN=localhost`, 1 year). The leaf's SANs cover `localhost`/127.0.0.1/::1 **plus the machine hostname, `<hostname>.local` and every non-loopback address of the host**, so other devices can reach it by name or LAN IP. Import `data/tls/mike-ca.crt` into a device's trust store to make the certificate valid there — distribution is file-only (never served over HTTP; the CA *key* never leaves the data dir, keys are `0600`). The CA is reused across restarts and never rotated automatically; the leaf is re-issued only when the host's names change (network or hostname change). There is **no expiry-based renewal** — refresh an expired leaf by deleting `mike.crt`/`mike.key` and restarting (keep the CA). The server sends the leaf alone: the CA is a root, so an importing client needs no intermediate. Plain HTTP on a TLS-enabled server gets a 301 → https. Custom certs: `--tls-cert/--tls-key` or `MIKE_TLS_CERT`/`MIKE_TLS_KEY` (no CA involved). Disable: `--no-tls` or `MIKE_TLS=0`. With TLS active, WebSockets upgrade to `wss://` and session cookies get `Secure` automatically.
- **The TLS-vs-plain first-byte sniff must not run inside `hybridListener.Accept()`** (`backend/server/server.go`): `net/http` accepts **serially**, so sniffing there lets one peer that connects and sends nothing stall every later handshake, and returning that peer's EOF/reset reads as a dead listener, ends `Serve` and kills the process. `Accept()` waits on a queue that a per-connection sniff goroutine fills — no worker pool, since N workers still block everyone at N silent peers. Sniffed-out peers are closed and logged per host, never handed to `net/http`; only the listener's own accept error may end `Serve`. Tests: `backend/server/listener_test.go`.
- **Never `log.Fatalf` on a `Serve()` error** (`main.go`): run the signal cleanup path (flight recorder, `srv.Shutdown`) and then exit 1 — `log.Fatalf` skips that cleanup and makes any listener error an abrupt exit.
- **A certificate is not the gate:** reaching the server at all is governed by `MIKE_LISTEN` (loopback by default) and by Host pinning — see `MIKE_ALLOWED_HOSTS` / `--allowed-hosts`. A SAN covering the LAN does *not* make the LAN usable: a request whose `Host` names that hostname/IP is rejected with 421 until it is also listed there (or the server is explicitly listening on that address). A reverse proxy that forwards the public domain (`proxy_set_header Host $host`) hits exactly this — nginx's default (`$proxy_host`, i.e. the upstream address) passes unlisted. Behind a proxy also set `MIKE_TRUST_PROXY=1`, or `X-Forwarded-Proto` (HTTPS redirect) and `X-Forwarded-For` (rate-limit IP) are ignored.
- **Weak master password + exposed listen address is fatal:** a `MIKE_LOGIN_PASSWORD` shorter than 8 characters, or one of the known defaults (`test`, `dev`, `admin`, `password`, `pass`, `changeme`), refuses to start on any non-loopback listen address. Loopback + weak stays allowed so `make run` (password `test`) keeps working.
- **Tests:** `backend/tls/tls_test.go` (CA is a real CA, leaf verifies against it, SAN set + re-issue rules, CA/leaf reuse).

### Required Environment Variables

| Env Var | Description |
|---------|-------------|
| `MIKE_ENCRYPTION_KEY` | Non-blank; derives the AES-256-GCM key for sensitive data. Server refuses to start without it. |
| `MIKE_LOGIN_PASSWORD` | Master password for auth/sessions. Server refuses to start without it. Only its `sha256` digest is kept in memory; a weak one (< 8 chars, or a known default) additionally refuses a non-loopback listen address. |
| `MIKE_REGISTRY_URL` | Extension registry index URL. Sourced **exclusively** from this env var (not DB-backed); falls back to `https://getmike.dev/extensions/registry.json`. A bare path (no `http(s)://`/`file://` scheme) is a local file resolved against the CWD. |
| `MIKE_TRUST_PROXY` | Set to `1` only behind a trusted reverse proxy that strips forwarding headers. Only then are `X-Forwarded-For` (rate-limit IP) and `X-Forwarded-Proto` (HTTPS redirect) honored; unset = ignored so they can't be spoofed. |
| `MIKE_LISTEN` | Server **listen address** — which socket the API is reachable on. It never authorizes a `Host` header; that is `MIKE_ALLOWED_HOSTS`. **Defaults to `127.0.0.1` (loopback only)** when unset/empty — the API is RCE-complete (terminal WS, git ops, file writes), so it is never public by accident. `0.0.0.0` / `::` are the explicit "all interfaces" values (container / reverse-proxy use); any other address listens on that one interface only. **Renamed from `--bind`/`MIKE_BIND`; the old names do nothing** — `--bind` is an unknown flag (startup error) and a leftover `MIKE_BIND` is ignored, so a stale `MIKE_BIND=0.0.0.0` comes up loopback-only. |
| `MIKE_ALLOWED_HOSTS` | Comma-separated extra hostnames accepted in the `Host` header (DNS-rebinding pinning, `HostPinMiddleware`). Accepted by default without listing: `localhost`, `127.0.0.1`, `::1` and the configured listen host (a wildcard bind names no host, so it adds nothing). Any other authority — a proxy's public domain, a `hostname.local`, a LAN IP — is rejected with **HTTP 421** until listed here, e.g. `MIKE_ALLOWED_HOSTS=kanban.example.com,192.168.1.20`. A missing/empty `Host` is rejected too. Ports are stripped on both sides, so an `example.com` entry answers `example.com:443`. Overridden (not extended) by `--allowed-hosts`; `Serve()` logs the effective value at startup and every rejection names both. |
| `MIKE_LOGIN_RATE_LIMIT` | Login budget per client IP per minute (default `5`). **Cannot be disabled:** `0`, negative or unparsable values fall back to `5` with a startup warning, so a typo can't silently drop brute-force protection. |
| `MIKE_EXT_MOUNT_ROOTS` | `os.PathList`-separated (`:` on Linux) list of **extra** host roots an extension sandbox may bind-mount from, beyond the implicit ones (workspace dir, registered project paths, extensions dir, system temp dir). For deployments keeping extension data elsewhere, e.g. `/srv/ext-data:/opt/models`. Still an allowlist — anything outside it is rejected. |
| `DANGER_ZONE` | Set to `1` to allow projects with sandboxing disabled (host-shell execution). Unset = such projects cannot be created or switched to unsandboxed (403). |

### CLI Flags
CLI flags take priority over environment variables.

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--port` | `MIKE_PORT` | `3000` | listen port |
| `--listen` | `MIKE_LISTEN` | `127.0.0.1` | listen address; loopback by default. `0.0.0.0`/`::` = all interfaces |
| `--data-dir` | `MIKE_DATA_DIR` | `./data` | DB, TLS certs, trace files |
| `--worktrees-dir` | `MIKE_WORKTREES_DIR` | DB `workspace_dir` (default `./.worktrees`) | runtime override, not persisted |
| `--no-tls` | `MIKE_TLS=0` | *(TLS on)* | plain HTTP |
| `--tls-cert` / `--tls-key` | `MIKE_TLS_CERT` / `MIKE_TLS_KEY` | *(auto-generated)* | custom cert/key files |
| `--allowed-hosts` | `MIKE_ALLOWED_HOSTS` | *(loopback + listen host only)* | extra accepted `Host` header values, comma-separated — set this for a reverse proxy that forwards a public domain, else every request 421s |

### Rules
- **Restarting:** backend changes require a server restart; frontend changes don't.
- **Never leave running** after a crash or panic — stop the server first.
- **E2E tests manage their own server** (global-setup starts/stops test servers). Your dev server is **killed** during test runs — restart it with `make run` afterwards.
- **Use background mode** (`&`) for development.
- **Test-driven:** validate functionality by testing for established facts before calling a change done.

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
| Isolated | 3001 | `/tmp/kanban-test-data-isolated` | `isolated` project (serialized, 1 worker) — config-modifying suites; see `testMatch` in `frontend/playwright.config.ts` |
| Mock LLM | 9999 | — | shared; `frontend/tests/mock-llm.js` |

Test repos live at `/tmp/kanban-test-repo` (+ `-2`), worktrees at `/tmp/kanban-test-worktrees/`. The registry is pinned to a local fixture (`frontend/tests/registry-fixture.json`) so registry e2e never hits the hosted registry.

Test servers are spawned via `go run .` and **inherit your shell environment** — export `MIKE_ENCRYPTION_KEY` before running e2e, or global-setup fails with "Server on port 3000 did not start within 15000ms".

If a prior run was interrupted, clean stale state first:
```bash
lsof -ti:3000,3001,9999 | xargs kill 2>/dev/null
rm -f /tmp/kanban-test-setup.lock
rm -rf /tmp/kanban-test-data /tmp/kanban-test-data-isolated /tmp/kanban-test-worktrees
```
Stale processes hold the ports (symptom: `ERR_CONNECTION_REFUSED`, or a stale TLS server answering a `--no-tls` test), and a stale lock file makes global-setup run teardown instead of setup.

### Frontend Unit Tests
- vitest + @testing-library/svelte, jsdom. Files: `src/**/*.unit.ts` / `src/**/*.unit.svelte.ts`, colocated in `__tests__/` dirs.
- Any test file importing a module that uses Svelte 5 compiler macros (`$state`, `$derived`, `$effect`) **must** use the `.svelte.ts` extension.
- Deterministic — no external dependencies (LLM, network, server).

### Go Unit Tests
- Use the `testServer(t)` helper for an isolated server with a temp DB; `t.TempDir()` for files. Deterministic — no external dependencies.
- A few tests shell out to a real `git commit` without `-c user.*` flags (e.g. the `backend/managers` remote-fixture tests), so the machine needs a git identity (`git config --global user.email …`) or they fail with `Committer identity unknown`.
- Sandbox tests need `runsc` **and** a working podman storage: under a nested/overlayfs root they all fail with `'overlay' is not supported over overlayfs, a mount_program is required` (podman exit status 125). That is environmental, not a regression — verify with `podman info` before reading anything into it.
- The Pi agent adapter's unit tests live in `../MIKE-extensions/agent/pi` and are run there — they are **not** part of `./backend/... ./pkg/...`.

### E2E Conventions
- Descriptive spec filenames in `frontend/tests/e2e/`; shared helpers in `helpers.ts`.
- `test.describe.serial()` when tests share state; unique identifiers (`Date.now()`) to avoid interference; wait for selectors, no arbitrary delays (drag-and-drop is the known exception — headless is slow).
- **Mock LLM is default** — tests use pre-recorded fixtures, never the real endpoint. Graceful failures when the LLM is slow/unavailable.
- **Config-modifying tests** (settings, models, shortcuts, mode select, …) must run in the `isolated` project — add them to `testMatch` in `playwright.config.ts`.
- **Heavy fixtures get a dedicated repo** — specs that create large files or mutate the working tree must NOT write into the shared `/tmp/kanban-test-repo` (other specs' `git add -A && git commit` races with them). Init a dedicated repo (e.g. `/tmp/kanban-test-repo-<name>`), create its project in `beforeAll` (use `browser.newContext({ storageState })` — `page` is not available in `beforeAll`), delete both in `afterAll`, and wrap the describe in `.serial`. See `editor-wrap-highlight.spec.ts`.
- global-setup sets a **local git identity** on the test repos — don't remove it: backend commit-and-done commits don't pass `-c` author flags, and without it those e2e tests fail silently (commit skipped, task still moves to Done).

### Mock LLM Server
- `frontend/tests/mock-llm.js` (Node, port 9999). Selects a fixture by request body (tools present + message keywords). Fixtures: `frontend/tests/fixtures/` — JSON arrays of OpenAI streaming SSE chunks.
- Disable: `MIKE_USE_MOCK_LLM=0 npx playwright test` (real LLM endpoint).
- Fixture format — one entry per SSE `data:` frame:
  ```json
  {"choices": [{"delta": {"content": "Hello"}, "finish_reason": null}]}
  ```
  Tool calls use `delta.tool_calls` with `finish_reason: "tool_calls"`; the final frame has `finish_reason: "stop"` plus `usage` (incl. `prompt_ms`/`predicted_ms`).
- **Recording a new fixture:** run with `MIKE_USE_MOCK_LLM=0`, capture the real SSE stream (browser DevTools Network tab or server logs), save it as a JSON array in `frontend/tests/fixtures/`, and add keyword routing in `mock-llm.js` if needed.

## Applying Changes

### Frontend
1. Edit Svelte/TS under `frontend/src/`.
2. `cd frontend && npm run check` (svelte-check) — **keep the codebase TS-clean**; fix all errors before finishing.
3. `cd frontend && npm run build` — server serves `frontend/dist/`; hard refresh the browser. **No server restart needed.**

### Backend
1. Edit Go under `backend/` (or `pkg/`).
2. `gofmt -w` the touched files.
3. `go build -trimpath -o kanban .` (or `make build`) and restart the server. Config persists in SQLite (`data/kanban.db`).

### Provider Routing
- Provider-based config in SQLite; `PUT /api/config` is a partial update (only changed fields). Two required pairs: `worker_provider`/`worker_model` (chat) and `utility_provider`/`utility_model` (titles, summaries, commit messages, checkpoint labels).
- Worker: the frontend defaults chat/model selection via `resolveWorker` in `$lib/utils/providers.ts`.
- Utility: backend `utilityTarget()` (`backend/exec/executor.go`) routes utility requests through the utility pair, falling back to the task's own URL/model.

## Architecture

### Client-Server Boundary
- **The frontend ONLY communicates through the Go server** (`localhost:3000/api/...`). It never calls the LLM endpoint directly — all LLM traffic (chat, model listing, e.g. `POST /api/config/fetch-models`) is proxied through Go.
- All real-time communication is WebSocket through the Go server. Client sends `{"type": "user_message", "data": {"content": "..."}}`; the inbound envelope is `stream.AppStreamMessage` (`Data` is `json.RawMessage`) → parsed JS value in the browser. **Outbound** frames are flat maps built where they are broadcast (no shared envelope struct in Go) — a `task_event` carries `task_id`/`event` beside `data`, so the client reads `msg.task_id`, not `msg.data.task_id`. Use optimistic UI for user messages (add immediately, skip the echo).
- Model/provider changes ride a `user_message` with optional `model_name`/`llama_server_url` fields.

### Sessions & Auth
One master password (`MIKE_LOGIN_PASSWORD`). `SessionManager` (`backend/auth/session.go`) keeps **only `sha256(password)`** and compares digests with `subtle.ConstantTimeCompare` — the plaintext never outlives the login request, and both sides are always 32 bytes (a length mismatch would make the constant-time compare short-circuit and leak).

- **Tokens:** 32 bytes from `crypto/rand`, hex-encoded, 30-day TTL. The raw token exists only in the response/cookie; the DB stores `hex(sha256(token))` (indexed, in both `token_hash` and the legacy `token` column), so a copy of `kanban.db` cannot be replayed as a live session. `BackfillSessionTokens` migrates pre-hash rows once at startup (their legacy column gets a `revoked-<id>` placeholder).
- **Per-row metadata:** `ip` + `user_agent` are recorded at login; `last_seen` is stamped during validation but throttled to one write per 60 s (`lastSeenInterval`) — the store serialises on a single connection, so an unthrottled write would turn every authenticated request into a DB write.
- **DB file is owner-only:** the data dir is `0700`, `kanban.db` and its `-wal`/`-shm` sidecars `0600` (`backend/store/store.go`). Trace files (`kanban.trace`) are `0600` too (`main.go`).
- **Task/thread IDs** (`backend/types/idgen.go`) draw dictionary words through `crypto/rand` (`secureIndex`), never clock-seeded `math/rand` — they surface in URLs, git branch names and worktree paths. A failing system source panics rather than issuing a guessable ID.

**Session endpoints** (`backend/server/handler_sessions.go`, routes in `routes.go`) — every one is session-authenticated (`SessionMiddleware`) and every non-GET is CSRF-guarded; no `/api/sessions` path is ever in `publicPaths`:
- `GET /api/sessions` → all live sessions, newest first: `{id, created_at, last_seen, expires_at, ip, user_agent, current}` (RFC3339 UTC; `""` for a never-seen or pre-migration row). The response type has **no token / token-hash / CSRF field at all**, so a credential cannot leak by accident.
- `DELETE /api/sessions/{id}` → `204` (`404` when unknown). Revoking your *own* session is allowed: the next request 401s and the SPA falls back to the login modal.
- `POST /api/sessions/revoke-others` → `{"revoked": n}`; deletes every session except the caller's.

**UI:** the **Sessions** section of Settings → **About** (`ConfigModal/SessionsSection.svelte`, API in `$lib/api/sessions.ts`) lists the rows (own session pinned on top, relative timestamps with absolute-time tooltips), with per-row **Revoke** and a **Sign out all other sessions** action. It mutates immediately — deliberately **outside** the modal's dirty/Save/Cancel flow, like the PWA install button above it — and fetches only when the About tab mounts. Revoking the current session hands off to the modal's `onSignOut` prop.
- **Tests:** `backend/store/sessions_test.go`, `backend/auth/session_test.go`, `backend/server/handler_sessions_test.go`, `backend/types/idgen_test.go`, `frontend/src/lib/api/__tests__/sessions.unit.ts`, e2e `settings-sessions.spec.ts` (isolated project).

### Execution Containment (host side)
Two boundaries are enforced by the server process itself, so they hold for non-sandboxed projects too:

- **Tool writes are jailed to the worktree's `os.Root`.** Parent-directory creation for `write_file`/`edit_file` goes through `ensureRootDir` → `root.MkdirAll` (`backend/exec/tools.go`), never `os.MkdirAll(filepath.Join(worktreePath, path))` — the host-side join resolves `..` and creates `<parent-of-worktree>/x/y` *before* `root.Create` rejects the path, i.e. arbitrary directory creation as the server user from prompt injection lifted out of repo content. An escaping path fails up front and creates nothing; new dirs are `0700`.
- **Agent subprocesses get an environment allowlist.** `SafeAgentEnv` (`backend/agent/env.go`) is the only source of `cmd.Env` for an adapter's subprocess (`runner.go`) and its `--list-models` probe (`loader.go`): `PATH`/`HOME`/user/term/locale/proxy/trust-store vars (+ `LC_*`) plus the adapter's own extras (extras last, since `os/exec` keeps the last occurrence). Handing over `os.Environ()` leaked `MIKE_LOGIN_PASSWORD` and `MIKE_ENCRYPTION_KEY` via `/proc/self/environ` to an extension-supplied binary — so **never** add `cmd.Env = os.Environ()` here, and add a variable to the allowlist only if an agent genuinely needs it.
- **Tests:** `backend/exec/tools_test.go`, `backend/agent/env_test.go`.

### Git Argument & Transport Hardening
- **Client-supplied refs are validated, then fenced.** `types.ValidateGitRef` (`backend/types/validate.go`) is deliberately tighter than `ValidateBranchName` (no `~ ^ : ? * [ \`, space, or leading `-`). `GET /api/fs` runs it on `?ref=` and additionally passes `--end-of-options` (git ≥ 2.24) before `<ref>:<path>` in `git show`, so a ref can never be parsed as an option (`--output=/tmp/pwn` creates/truncates a file anywhere the server user can write).
- **Clones speak only allowed transports.** `git.CloneArgs` puts `-c protocol.ext.allow=never` *before* `clone` (so the process parsing the URL gets it) and `git.CloneEnv` sets `GIT_ALLOW_PROTOCOL=http:https:git:ssh:file` + `GIT_TERMINAL_PROMPT=0` — enforced by git itself, independent of the user's `git config`. `managers.validateGitCloneURL` mirrors it host-side: no leading `-`, no control characters, no remote-helper `::` transport (`ext::sh -c 'id'`), and a **case-sensitive** scheme check because git's own is (`HTTPS://` is refused as transport `HTTPS`).
- **One slugifier for every name derived from user text.** `types.Slugify` / `types.SlugifyBranchName` (`backend/types/slug.go`, with `MaxBranchName` / `BranchSlugMaxLen`) back task branch names (`exec`), the branch GC and Main-thread recovery that match live branches back to their task by slug (`git`), and project IDs (`managers`). The matching *parser* lives beside them — `types.TaskBranchSlugPrefix` (with `TaskBranchSuffixLen`, the random tail `exec` appends) — because a GC that parses a different shape than the executor writes deletes branches that are still in use. Do not add a second copy of either. Non-ASCII letters are dropped, not transliterated.
- **Tests:** `backend/types/validate_test.go`, `backend/types/slug_test.go`, `backend/server/handler_fs_test.go`, `backend/git/clone_test.go`, `backend/managers/project_test.go`.

### Keyboard Shortcuts (Settings → Preferences)
User-customizable, persisted as `keyboard_shortcuts` JSON in the `config` table via the shared Settings Save/Cancel flow. Defaults in `DEFAULT_SHORTCUTS` + helpers in `$lib/utils/keyboard.ts`: `create_task` `C`, `create_task_submit`/`start_task` platform Ctrl/Cmd+Enter, `open_terminal` Ctrl+`, `toggle_fullscreen` Ctrl+Up, `multi_select_click` platform Ctrl/Cmd+Click (platform-aware defaults are `default*()` functions, not `DEFAULT_SHORTCUTS` entries).

- **Format & semantics:** normalized `control`+`meta`+`shift`+`alt`+key. Absent = default, `""` = disabled, otherwise = custom. The key may also be the pseudo-key `click` for a **mouse binding** (`"shift+click"`) — helpers `normalizeClick` / `matchesClickBinding` / `effectiveMultiSelectClick` / `isBrowserLinkClick` in `$lib/utils/keyboard.ts`; only the primary button is bindable and an unmodified click is never a binding. `PUT /api/config` replaces the whole object — the Settings UI always sends **all** bindings, so omitting one resets it to default.
- **Capture** (`normalizeKey`): rejects modifier-only presses, Escape (cancels capture), named keys without a modifier, and browser-reserved `Ctrl+W/Q/N/T`. A click binding captures a `mousedown` instead (`PreferencesTab.svelte` adds a window capture listener while that row is capturing, refuses a bare click and any keypress, and skips its own `.key-chip`/`.shortcut-action` so click-again-to-cancel, Reset and Clear keep working).
- **Activation:** board/global bindings in `use-keyboard-shortcuts.svelte.ts` (wired in `App.svelte`); dialog/panel-scoped bindings in the owning component (`CreateTaskModal`, `Chat/TaskChat`, `TerminalPanel`, `CardDetail`, `ExplorerView`, `TaskExplorerView`).
- **Hints:** text buttons show a `<kbd class="key-hint">` + the shortcut in the `title` tooltip; hidden by `hide_hints` (tooltips keep the text) and below 768px; `aria-label`s stay stable for tests.
- **Adding a shortcut:** `*string` field in `types.KeyboardShortcuts` (`backend/types/domain.go`) + all-nil check in `normalizeKeyboardShortcuts` (`backend/config/config.go`) + field in `KeyboardShortcuts` (`$lib/types.ts`) + default in `DEFAULT_SHORTCUTS` (or a platform-aware `defaultXxx()`) + row in `ConfigModal/PreferencesTab.svelte` + field in the `ConfigModal.svelte` save payload (always send all bindings) + dispatch (global: `use-keyboard-shortcuts.svelte.ts`; scoped: owning component).
- **Tests:** `backend/config/config_test.go`, `utils/__tests__/keyboard.unit.ts`, `preferences-tab.unit.svelte.ts`, e2e `preferences-keyboard.spec.ts` (isolated), `create-task-submit-shortcut` / `terminal-shortcut` / `toggle-fullscreen-shortcut` specs.

### Link-like Rows (projects & task cards)
Sidebar project rows and task cards are reachable by real URLs, so the browser's own link actions work on them: right-click → *Open Link in New Tab/Window*, middle click, *Copy Link Address*, status-bar URL. Both use the same deep links the app itself writes (`?project=`, `?project=&task=`, canonical order from `updateUrl` in `App.svelte`), which `loadAppData` + `restoreTaskFromUrl` already restore — no backend route is involved (`/` is the only SPA document).

- **Project row:** `App/Sidebar.svelte` renders `<a class="project-item" href="/?project=<id>" role="button" tabindex="0">` — no nested interactive content, so the button→anchor swap is total. Unmodified click → `preventDefault()` + `onSelectProject`; **any modifier click is deliberately left uncancelled** so the browser opens a tab/window. A cloning project gets **no href** (still focusable, still inert). `role="button"` is kept, so Space is handled explicitly (a native anchor only activates on Enter).
- **Task card:** the card root stays `<div class="card" role="button" tabindex="0" draggable>` and gets a **full-cover overlay link** — `<a class="card-open-link" aria-hidden="true" tabindex="-1" draggable="false">` (`position:absolute; inset:0; z-index:1`) as its last child. A root anchor is not an option: cards nest subtask `Card`s and their own buttons, which would be anchor-in-anchor markup and would need `preventDefault()` in every early-return branch to stop full-document navigations. `draggable="false"` keeps the **card** (not the URL) as the HTML5 drag source; `aria-hidden` keeps the card root as the only accessible name.
- **Stacking contract** (`Card.svelte`): `.card-title` is deliberately NOT lifted, so the overlay still covers the title text (that's where right-clicks happen); instead `.card-actions`, `.subtask-progress-bar` and `.subtask-list` carry `z-index: 2`, and `.card.queued > *` is scoped to `> :not(.card-open-link)` — otherwise a queued card lifts the overlay to `.card-title`'s level and the DOM order lets it eat the action buttons.
- **Click routing:** the overlay's own handler always `stopPropagation()`s (so the card root doesn't handle one click twice) and cancels navigation *except* when `isBrowserLinkClick()` says the browser owns it. The card's multi-select toggle fires only for the bound modifier (`multi_select_click`, default platform Ctrl/Cmd+Click) — unbound modifier clicks reach the browser instead. Leftover: on a **queued** (offline-pending) card the lifted content covers most of the overlay, so the link menu is only available over the card's padding.
- **Never** re-tune these rows with `<button>`/`<div>` + a faked context menu: the link menu is a DOM-level behavior an app can't synthesize. Keep `role="button"` and the selectors tag-agnostic — `use-board-arrow-nav.ts` selects `.project-item` (not `button.project-item`).
- **Tests:** `components/__tests__/link-rows.unit.svelte.ts` (hrefs, click/modifier routing, Space, cloning, overlay attributes), `utils/__tests__/keyboard.unit.ts` (click bindings), e2e `link-like-rows.spec.ts` (middle-click opens a real tab, no document navigation on left click, drag source is the card not the link, second tab lands on the linked project/task), e2e `preferences-keyboard.spec.ts` (isolated; rebinding frees Ctrl+Click).

### UI Preferences (Settings → Preferences → UI)
Top-level `*bool` config fields with the same save/load plumbing as the shortcuts (`parseBoolPtr` in `GetConfig`, `omitempty` in `configResponse`). Example: `hide_model_badge` gates `.model-badge` in `Card.svelte` (`appStore.config?.hide_model_badge === true`).
- **Adding:** `*bool` in `types.Config` + read in `GetConfig` / write in `UpdateConfig` + field in `configResponse` + PUT merge in `HandleConfig` (all in `backend/config/config.go`) + field in `Config` (`$lib/types.ts`) + state/hydrate/save payload in `ConfigModal.svelte` + toggle row in `ConfigModal/PreferencesTab.svelte` (UI section) + consume from `appStore.config` in the target component.
- **Tests:** `backend/config/config_test.go` (round-trip + omitted-when-nil), `preferences-tab.unit.svelte.ts`, `card-model-badge.unit.svelte.ts`, e2e `preferences-hide-model-badge.spec.ts` (isolated).

### Board Arrow-Key Navigation
Arrow keys move **real DOM focus** (the global `:focus-visible` ring) — no separate "highlighted" store. A 5-column ring `[Projects][Backlog][In Progress][Review][Done]`: the project list is the leftmost column (always lands on the active project), board Left/Right skip empty columns and never wrap, Down/Up wrap within a column.
- Files: pure resolvers `resolveArrowTarget`/`resolveOverviewArrowTarget` in `composables/use-board-arrow-nav.ts`, wired in `App.svelte`; project-list keydown in `App/Sidebar.svelte` (focus ring is an **inset box-shadow** on `.project-item:focus` — ancestors clip outlines, and `click()+focus()` defeats `:focus-visible`).
- **Tests:** `composables/__tests__/board-arrow-nav.unit.ts`, e2e `board-arrow-nav.spec.ts`.

### Task View Column Navigation
In the Task View (`CardDetail.svelte`) the three columns `[Board][Chat][Diff]` are focusable; arrow keys move real DOM focus. All handlers live in `CardDetail.svelte`'s window keydown listener, using pure resolvers from `composables/use-task-detail-column-nav.ts` (`resolveDetailColumn`, `resolveDiffFileTarget`, `canSwitchDetailColumn`).
- Left/Right cycle Board→Chat→Diff→Board. Plain arrows in any editable focus (incl. the chat input) keep native text behavior; **Ctrl+Alt+Left/Right** switches columns from any focus (except while editing a file). Up/Down dispatch on the focused column (overview cards / page-scroll chat / diff file headers).
- **Tests:** `composables/__tests__/task-detail-column-nav.unit.ts`, e2e `task-view-column-nav.spec.ts`.

### Reconnection Refresh
When the WebSocket drops and reconnects, the current view (board, task view) updates automatically:
- `onConnected(true)` in `App.svelte` runs `silentRefresh()` if `initialLoadComplete` is true.
- Project re-subscription forces the server to re-send `task_diff_stats`, refreshing the diff panel.
- `TaskChat` subscribes to the `wsConnection` store and reloads history on reconnect. Turns completed while offline aren't replayed (buffer cleared at turn end), so DB reload is required.
- Browser `online`/`offline` events complement the WS backoff: `offline` queues writes immediately; `online` flushes the queue and calls `reconnectNow()` to skip backoff.
- Board refresh on `visibilitychange` handles phone-sleep cases.
- **Tests:** e2e `reconnect-refresh.spec.ts`. Since `page.route` doesn't intercept WebSockets in Playwright, the spec wraps the `WebSocket` constructor via `addInitScript` and uses `serverApi()` to advance state.

## Extensions

Types: `lsp` (language servers in gVisor sandboxes), `ui` (frontend injection), `agent` (AI agents as subprocesses via WASM adapters).

NEVER commit. The user will commit manually.

**Key files:**
- `backend/extensions/` — domain types, `store.go` (SQLite), `registry.go` (FetchIndex/FetchManifest/PullImage/BuildImage), `sandbox.go` (refcount lifecycle), `handler.go` (REST), `changelog.go`
- `backend/lsp/extension_manager.go` — extension-driven LSP discovery (replaces the hardcoded registry; LSP availability = installed extensions)
- `frontend/src/lib/services/extension-manager.svelte.ts`, `ui-injector.svelte.ts`; `$lib/api/extensions.ts`; `ConfigModal/ExtensionsTab.svelte`

**REST:** `/api/extensions` (list/get/update/delete), `PATCH /{id}/enabled`, `GET /registry`, `GET /local`, `GET /check-updates`, `GET /{id}/logs`, `GET|PUT|DELETE /{id}/storage` (the namespaced key-value store behind `extHost.storage.*`), `POST /{id}/proxy` (the only extension egress path; enforces the manifest's domain allowlist). The three long-running operations — `POST /install`, `POST /install-local`, `POST /{id}/update` — answer as **SSE** streams (`streamOperation`). There is **no** build-progress route: image build/update output reaches the browser as `build_progress` **task WebSocket** events (`types.WSTypeBuildProgress`, sent from `exec/loop_helpers.go`). Plus the preview endpoints below.

**LSP capability gates** (`manifest.capabilities.lsp`, both default to enabled when unset, both toggled per extension in Settings → Extensions): `editor_enabled` gates the *browser's* LSP features (diagnostics, completions, go-to-definition) — the server only stores and serves the flag, and `extension-manager.getEditorLSPExtensions()` is what actually filters (`getLSPForFile` → the editor's LSP connection). `tool_calls_enabled` gates the *agent's* LSP tools and is enforced server-side (`ExtensionRegistry.IsToolCallsEnabledForFile`, checked by the executor before an LSP tool runs). The two are independent: an extension can serve the editor without exposing tools to the model, or the reverse.

**UI injection:** extensions activate per scope; components call `renderSlot(scope, context)` on mount. Scopes → components: `task-card`→Card, `file-editor`→FileEditor, `project-board`→Board, `task-chat`→TaskChat, `project-sidebar`→Sidebar, `file-browser`→dir picker modal, `create-task`→CreateTaskModal.

### UI Extensions
UI extensions are **plain JavaScript modules — not Svelte components** (external bundles carry their own Svelte runtime → `effect_orphan` errors).

- A module default-exports `init({ component, config, ...scopeContext })` and returns a cleanup `() => void`. No Svelte imports; DOM manipulation + `document.createElement('style')` only. `ui.module_url` is served by the Go static handler under `/extensions/...`. That tree goes through `noListingFS` (`backend/server/routes.go`), so `/extensions/` and `/extensions/<author>/` never render a directory index (they 404) — the installed-extension tree is not an unauthenticated listing; only directories that actually hold an `index.html` open, which keeps the extension entry contract (`/extensions/<author>/<name>/`) intact. `manifest.json` carries metadata, `ui.injection_points`, optional `config_schema`, and `priority` (lower loads first; ties by name).
- **Source lives in `../MIKE-extensions/<type>/<name>/`.** `../MIKE-extensions/registry.json` is **generated** — regenerate after editing a manifest or bumping a commit:
  ```bash
  python3 scripts/gen-registry.py            # regenerate
  python3 scripts/gen-registry.py --check    # verify up to date (CI)
  ```
- **Install model:** entries are git-pinned (`author`/`repo`/`commit`/`subdir`); the server clones the repo, checks out exactly `commit` (full SHA-1/SHA-256), verifies `git rev-parse HEAD` matches, then materializes the subdir into `./extensions/<author>/<name>`. Deploy via Settings → Extensions → "Install from Registry".
- **Bundling a runtime library (isolation):** keep it **inside the extension folder** (bundle an ESM npm package to a single IIFE committed in the extension dir, with its own `package.json` + build). The core `frontend/` workspace must NOT gain an extension's runtime dependencies.
- **Extension tests:** colocated under the extension dir's `tests/` (loading Playwright from the frontend workspace), not in `frontend/tests/`. Shared extension logic is a plain script loaded by both the extension and its tests — never copy it.
- **The extension document is served under the host CSP:** `/extensions/...` goes through `CORSMiddleware`, so `script-src 'self' 'nonce-…'` applies — **inline `<script>` in an extension's HTML is blocked**; load code as external files (`<script src="app.js">`). `<style>`/inline styles are fine (`style-src 'unsafe-inline'`). The entry also lands on its **directory** URL (Go's `FileServer` 301s `/index.html` → `./`), so match a mounted frame by directory path, not by `index.html`.
- **Channel handshake (document identity):** the host mints a per-mount token, passes it as `?extch=` in the iframe URL and arms the channel only when the document echoes it back (`exthello`), because `ev.origin === 'null'` + `ev.source` identify the *frame*, not the document the host mounted — any document that lands in a sandboxed frame would otherwise receive `init` (config/grants/context), pushed events (`editor.contentChange`) and be able to call the granted capabilities. Nothing is posted to the frame before arming (`exthello-request` excepted); a `call` from an un-armed frame is refused **without a response** (a silent channel leaks nothing). On the 2nd+ iframe `load` the channel warns and **disarms** (event listeners torn down, calls refused) then re-requests a hello, so a legitimate reload re-handshakes and keeps working — the watchdog never tears the iframe down. Only the mounted frame's own `load` counts: an extension's **nested** iframes (html-preview renders the previewed document in one) may navigate freely, because a nested load belongs to the nested browsing context — counting it would disarm the channel on every preview refresh. Cross-origin self-navigation is additionally blocked in flight by the host `frame-src 'self'`; the same-origin case (a sibling file taking the frame over) is what the handshake covers. `frontend/public/host-client.js` reads `extch` and answers `exthello` on every load — a hand-rolled client that never does hangs at `extHost.ready()` (fails closed). Regression-tested by `frontend/tests/e2e/extension-frame-channel.spec.ts` (isolated; its fixture must use external scripts, see above).

### Preview capability
Generic host support for UI extensions that render the open file (e.g. the `html-preview` extension — overlay + live refresh for `.html`/`.xhtml`/`.svg`, root-relative refs rewritten **server-side** at serve time to the token's root — the worktree root, so multi-page in-tree navigation works).

- **Host API:** `preview.serve(content)` → `{token, url}`, `preview.release(token)`, `preview.navState(token)` → `{seq, path, ok, isDoc}` (the token's last committed document navigation — the server-side truth behind the extension's in-tree allow-list) — `frontend/src/lib/extensions/handlers/preview.ts`, exposed as `extHost.preview.*` in `frontend/public/host-client.js`. The `project`/`path`/`task` body fields are filled by the host from its **own** injection context (FileEditor passes `filePath`/`projectId`/`taskId`), never from extension arguments — an extension can't point the server at arbitrary directories. A re-serve from the same editor implicitly releases its previous doc.
- **Go** (`backend/server/handler_preview.go`): in-memory; 128-bit token from `crypto/rand` — **fail-hard**: `newPreviewToken` returns an error and the serve request is rejected rather than falling back, because a guessable token is an unauthenticated read of the document plus every sibling under its asset root; 10-min idle TTL (refreshed per read); 32-doc cap (oldest evicted); 10 MB/doc; 30 s sweep. `POST /api/preview` (session+CSRF) stores the doc: it generates the token FIRST, rewrites root-relative refs (`src/href/action="..."`) for HTML-family content to `/api/preview/<token>/...` (RE2 — no backrefs; patterns never consume the closing quote/paren; unquoted `url(...)` supported), re-checks the size cap post-rewrite, and resolves the doc's **worktree root** + `dirRel` (workspace-only paths fall back to the file's own dir). The doc serves at its own directory: `GET /api/preview/{token}/[dirRel]/` (bare token 301s there; the token root is the worktree root, no listing; a link to the doc's own file name — the self-reference — serves the STORED content, not the disk file). `GET /api/preview/{token}/[path...]` is public (token is the credential; content type from the extension; `Cache-Control: no-store`; the doc — and any HTML/SVG/XHTML sibling document — carries the doc's OWN CSP that `CORSMiddleware` skips, so a page navigated to by the previewed doc keeps the same egress posture: strict zero-egress by default (`previewDocCSPStrict`: no `*` in any load-type directive, `connect-src 'none'`), permissive (`previewDocCSPNetwork`) only when the serve request sets `allow_network`; multiple CSP headers union to the most restrictive; `navigate-to` is deliberately absent — unimplemented in all browsers, top-frame navigation is governed by the host CSP's `frame-src 'self'` + the watchdog); `GET /api/preview/{token}/nav` (session-authed; exact route wins over `{path...}`, so a worktree-root file literally named `nav` is shadowed — documented limitation) returns the nav record; `DELETE /api/preview/{token}` releases early. **Nav record:** a committed HTML-family document response to a real frame navigation (Chromium sends `Sec-Fetch-Dest: document` for top frames and `iframe` for subframes — the preview frame IS a subframe) bumps the token's monotonic `seq` and stores `{path, ok, isDoc}`; 404s, redirects, and non-HTML subresources (css/js/img — `Sec-Fetch-Dest: image/style/...`) never record, so a navigation OUT of the tree — which no preview handler can serve — leaves the record stale. Sibling files are worktree-root-aware with `filepath.Clean`/`Rel` containment + `EvalSymlinks` re-containment — traversal and symlink escapes are 404.
- **Security model:** the doc loads in a nested `<iframe sandbox>` (opaque origin — bare `sandbox` = scripts OFF by default; the extension sets `allow-scripts` only when its `allow_scripts` setting is on) inside the extension's already-sandboxed iframe — no host cookies/DOM/Web Storage access, no top-window navigation, no popups; `/api` mutations still need the CSRF header; no `frame-ancestors` (the doc must be frameable). Scripts-off previews are fully static (exfil impossible — every requested URL was author-written) so they get the permissive CSP; scripts-on get the strict zero-egress CSP unless `allow_network` opts back into the permissive one. **Self-navigation — in-tree allow-list:** the extension arms a load counter at every `frame.src` set; the first load is the doc it installed (it then syncs its baseline `lastNavSeq` from `/nav` — the `load` event fires after subresources, so nested-iframe loads at install are absorbed into the baseline), and any load with nothing pending is a self-navigation. The extension can't read the nested frame's URL (opaque origin), so it asks `/nav`: a FRESH `ok` record (`seq > lastNavSeq`) means the server actually served an in-tree file → ALLOWED (the doc may be a multi-page site: `index.html` → `page2.html` → `sub/page3.html`); anything else (stale record, error, host call failure) → CONTAINED (frame reset to about:blank, token released, warning modal, overlay off). Scripts-off has no script-driven nav at all — the bare sandbox also refuses `<meta http-equiv="refresh">` redirects (spec-gated on the `allow-scripts` keyword); scripts-on external navs are blocked in flight by the host `frame-src 'self'` (the blocked nav's error load then hits the stale record → contained); same-origin OUT-of-tree navs (incl. `<meta refresh>`) commit, leave the record stale → contained. While the preview is on another in-tree page, live refresh is paused (re-serving would kick the user back to the doc); returning to the doc (its dir URL or self-reference → `isDoc`) re-enables it. An edit arriving while an allow-list decision is in flight (the frame rendered, but the outcome isn't known yet) is HELD and re-evaluated on resolution — refreshed if it lands on the doc (incl. the return-to-doc window, where the frame still came from a sibling), superseded by the skip if it lands on a sibling — so a refresh can never yank the frame back mid-navigation. Known residual: a scripts-on document that DYNAMICALLY loads an in-tree nested iframe after install (invisibly bumping `seq`), then escapes same-origin, can false-allow — bounded (cross-origin still blocked in flight; permissive posture opted into open network). Isolation is regression-tested by probes in `frontend/tests/e2e/html-preview.spec.ts` (egress listener + scripts-off/scripts-on mode tests, incl. meta-refresh and in-tree navigation probes) and by the extension's colocated iframe harness (`MIKE-extensions/ui/html-preview/tests/`).

### Changelogs
An extension may ship `CHANGELOG.jsonl` next to its manifest (one JSON object per line, oldest→newest: `{"hash", "version", "description"}`), published to `<registry-base>/<author-slug>/<name>`. `GET /api/extensions/check-updates` returns entries newer than the installed commit (anchored on the first hash match); no match / no file / fetch error → empty `changelog` (best-effort, never blocks update detection). UI: `.update-changelog` in the "Updates Available" section of `ExtensionsTab.svelte`. Key files: `backend/extensions/changelog.go`, `FetchChangelog` (`registry.go`), `HandleCheckUpdates`, e2e `extension-changelog.spec.ts`.

### Agent Extensions
Agent extensions run AI agents (e.g. Pi) as subprocesses. The backend knows **nothing** about agent protocols — all protocol logic lives in the extension's WASM adapter.

`Backend (executor) ←→ AgentRunner (I/O bridge) ←→ WASM Adapter (protocol) ←→ Agent Process`

- **Adapter interface** (`AgentAdapter`): `Init(config)`, `SubprocessConfig()`, `EncodeCommand(cmd, data)`, `ParseOutput(bytes) → []Event`, `HandleUIRequest(id, method)`, `Close()`. Event types: `token`, `reasoning`, `tool_call`, `tool_result`, `status`, `stats`, `error`, `persist`, `turn_complete`.
- **Persistence:** `EventTypePersist` carries `[]PersistEntry` (pre-parsed assistant text/reasoning/tool calls); it is the **only** path to DB persistence — the executor iterates entries generically, with zero agent-specific persistence in the backend.
- **URL scheme:** `agent://<name>` (legacy `pi://agent`). Key files: `backend/agent/` (interface, runner, wazero loader with `WithStartFunctions("_initialize")`, registry), `backend/exec/loop_agent.go`.
- **WASM memory model (zero buffers):** input = `string` params (the runtime maps them to ptr+len in linear memory); output = a `//go:wasmimport env store_result(string)` host callback that fires synchronously during `fn.Call` (a size-1 buffered channel safely captures it). The host calls `_initialize` first, then exported functions. No shared result buffers, no `malloc`/length bookkeeping.
- **Creating an agent extension** in `../MIKE-extensions/agent/<name>/`: `adapter.go` (core — **zero `backend/` imports**, unit tests run natively there), `wasm/main.go` (WASI reactor via `//go:wasmexport`, wrapping the adapter; `string` is allowed as a param only, never a return), `build.sh` (`GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared`), `manifest.json` (`agent.wasm_module`), plus its own `LICENSE`/`NOTICE`/`THIRD_PARTY_NOTICES.md`. Reference implementation: `../MIKE-extensions/agent/pi/`.

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
- The merge-conflict resolution commit (`commitConflictResolution`, `backend/exec/merge_conflicts.go`) stages with `git add -u` (tracked only), NOT `git add -A`.
- When the worktrees dir lives **inside** the project, `CreateWorktree` (`backend/git/worktree.go`, `ensureWorktreesExcluded`) idempotently appends it to the repo's local `.git/info/exclude` so worktree content never shows in project `git status` or gets swept into project-level `git stash -u`. Local-only, never committed.
- **Tests:** `backend/managers/task_test.go` (`TestHandleCommitAndDone_ExcludesUnstagedFiles`), `backend/exec/merge_conflict_resolution_test.go` (`TestCommitConflictResolution_DoesNotSweepUntrackedFiles`), `backend/git/worktree_test.go` (`TestCreateWorktree_ExcludesWorktreesDirFromProjectStatus`, `..._NoExcludeWhenWorktreesOutsideProject`), e2e `commit-and-done-unstaged.spec.ts`.

## Licensing

Apache-2.0 (`LICENSE`, `NOTICE`, `THIRD_PARTY_NOTICES.md`). Dependency licenses are **not** changed by our license — each library keeps its own license and copyright.

- **Extensions are independently-licensed projects:** every extension directory carries its own `LICENSE` (Apache-2.0), `NOTICE`, and `THIRD_PARTY_NOTICES.md`. New extensions get all three; new dependencies update that extension's file.
- **Never remove** a dependency's license/copyright header or NOTICE. Do **not** introduce **GPL/AGPL** dependencies (incompatible with Apache-2.0). MPL-2.0 is OK (don't modify MPL-2.0 files). Apache-2.0 deps that ship a `NOTICE` require its content in our `NOTICE` (currently: wazero). `lightningcss` (MPL-2.0) and `caniuse-lite` (CC-BY-4.0) are build/dev-only — keep them out of the distributed runtime artifact.
- **Bundled third-party files** (e.g. extension IIFE libraries) keep **their own** license notice — never slap an Apache-2.0 header on them.
- **Regenerate notices whenever a dependency is added/removed/upgraded:** `go mod tidy` + `go list -m all` (or `go-licenses report ./...`), `cd frontend && npx license-checker --production --summary`, then update `THIRD_PARTY_NOTICES.md` (incl. "Last updated") and `NOTICE`.

## Debugging

1. Server running? `ps aux | grep kanban` · 2. Config correct? `curl -sk https://localhost:3000/api/config`
3. Worktrees dir? `ls .worktrees/` · 4. Frontend built? `cd frontend && npm run build` · 5. Hard refresh (browser cache)
6. Logs: server stdout or `/tmp/kanban.log`
7. E2E: `cd frontend && npx playwright test` · 8. Go: `go test ./backend/... ./pkg/... -count=1 -parallel 8`
9. Stale test state? `lsof -ti:3000,3001,9999 | xargs kill 2>/dev/null && rm -f /tmp/kanban-test-setup.lock && rm -rf /tmp/kanban-test-data /tmp/kanban-test-data-isolated /tmp/kanban-test-worktrees`

### E2E Test Failures
- **`ERR_CONNECTION_REFUSED`** / wrong-protocol responses: stale processes from an interrupted run hold the ports — run the cleanup above.
- **`waiting for locator('.project-item')`**: test project not created — check global-setup logs. **`waiting for locator('.card')`**: no tasks exist.
- **Drag-and-drop flakes:** headless is slow — add `waitForTimeout` after the drag, increase predicate timeout.
- **Mock LLM routing wrong:** check keyword matching in `mock-llm.js` and the fixture files.

### Go Test Failures
- **`unable to open database file`**: use the `testServer(t)` helper (temp DB).
- **Sandbox tests fail**: ensure gVisor is installed (`runsc --version`).
- **`context deadline exceeded`**: raise the timeout or optimize.

### Diagnostic Tests (Standalone Playwright Scripts)
For ad-hoc investigation (request patterns, races), write standalone Playwright scripts in `frontend/tests/` as `.mjs` files run against a live dev server: `make run` then `node frontend/tests/<script>.mjs`. Use `page.evaluate()` for API calls (CSRF token from `/api/auth/session` for POST/PUT/DELETE), `route.fetch()` + `route.fulfill()` for interception, `Date.now()` offsets for timing, and `page.screenshot({ path: '/tmp/debug.png' })`.

### Crash Debugging (Flight Recorder)
The Go flight recorder is always-on (~1% overhead), writing to `data/kanban.trace`, rotated every 30 s (previous file → `data/kanban.trace.old`) — on crash it holds ~30 s of pre-crash data. Analyze: `go tool trace -http=:8080 data/kanban.trace`.
