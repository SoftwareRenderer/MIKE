# AGENTS.md

## Required Dependencies

### gVisor

gVisor (`runsc`) is required for project sandboxing. When a project has sandboxing enabled, task tool calls and terminal sessions run inside gVisor-isolated containers.

**Notes:**
- gVisor must run in rootless mode
- Podman sandbox commands run via `podman --runtime runsc`
- Without gVisor, projects with sandboxing enabled will refuse to start tasks (hard error, no fallback)

### Sandbox Bridge

The sandbox bridge (`sandbox-bridge`) is a lightweight binary that runs inside each sandbox container and handles tool calls (read, write, edit, bash, shell) over TCP. This eliminates the overhead of spawning `podman exec` per tool call.

**Build:**
```bash
# Build for Linux (CGO_ENABLED=0 for static build)
CGO_ENABLED=0 GOOS=linux go build -trimpath -o sandbox/sandbox-bridge -ldflags="-s -w" ./pkg/bridge/agent
```

**Runtime discovery:** The server finds `sandbox-bridge` via (in order):
1. `MIKE_AGENT_PATH` environment variable
2. Next to the running executable — `sandbox-bridge` and `sandbox/sandbox-bridge`
3. Current directory — `sandbox-bridge` and `sandbox/sandbox-bridge`

**Required:** The sandbox-bridge binary is required for sandbox mode. Container creation fails if the binary is not found.

**Architecture:**
```
Host (kanban server)                    Container (sandbox)
─────────────────                       ─────────────────
                                        ┌──────────────┐
SandboxManager ──TCP──► 127.0.0.1:PORT ─► sandbox-bridge  │
                     (mapped via          (listens on    │
                      podman -p)          0.0.0.0:9999)  │
                                        └──────────────┘
```

**Protocol:** Custom binary framing — 9-byte header (MsgID + Type + Length) + typed payload. Supports streaming via Chunk frames before a final Result/Error frame.

## Server Management

The agent is responsible for starting/stopping the server.

```bash
# Start (background)
./kanban &

# Stop
pkill -f "./kanban"

# Verify running
curl -s http://localhost:3000/api/config
```

### HTTPS / TLS

TLS is **enabled by default** with auto-generated self-signed certificates.

| Mode | Flag / Env Var | Description |
|------|----------------|-------------|
| **Default (HTTPS)** | (none) | Auto-generates self-signed cert in `data/tls/` on first startup |
| **Custom certs** | `--tls-cert=path --tls-key=path` or `MIKE_TLS_CERT` + `MIKE_TLS_KEY` | Uses user-provided certificate and key files |
| **Disable TLS** | `--no-tls` or `MIKE_TLS=0` | Plain HTTP, no TLS |

### Required Environment Variables

The server requires an encryption key to secure sensitive data (like API keys) in the database. If this is not set, the server will refuse to start.

| Env Var | Description |
|---------|-------------|
| `MIKE_ENCRYPTION_KEY` | A non-blank string used to derive the AES-256-GCM encryption key. |
| `MIKE_LOGIN_PASSWORD` | The master password used for server authentication and session creation. |
| `MIKE_REGISTRY_URL` | The extension registry index URL. Sourced exclusively from this env var (not DB-backed), falling back to the hosted online registry `https://getmike.dev/extensions/registry.json`. Bare filesystem paths (no `http(s)://` or `file://` scheme) are treated as local files, resolved against the working directory. |

### CLI Flags

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--port` | `MIKE_PORT` | `3000` | Server listen port |
| `--data-dir` | `MIKE_DATA_DIR` | `./data` | Data directory for DB, TLS certs, trace files |
| `--worktrees-dir` | `MIKE_WORKTREES_DIR` | `./worktrees` | Directory for git worktrees |
| `--no-tls` | `MIKE_TLS=0` | *(disabled)* | Disable TLS (plain HTTP) |
| `--tls-cert` | `MIKE_TLS_CERT` | *(auto-generated)* | Path to TLS certificate file |
| `--tls-key` | `MIKE_TLS_KEY` | *(auto-generated)* | Path to TLS private key file |

CLI flags take priority over environment variables.

- Self-signed certs are valid for `localhost`, `127.0.0.1`, and `::1` (1 year expiry)
- Certs are reused across restarts (only generated once)
- WebSocket connections automatically upgrade to `wss://` when TLS is active
- Session cookies get the `Secure` flag auto-enabled when TLS is active

### Rules
- **Restarting** Backend changes require a restart. Frontend changes do not require restarts.
- **Never leave running** after a crash or panic
- **E2E tests manage their own server** — global-setup starts/stops a test server automatically
- **Restart manually after tests** — your dev server is killed during test runs
- **Use background mode** (`&`) for development
- **Test Driven Development** Validate functionality by testing for established facts

## Development Workflow (TDD)

### Feature Development Process
1. **Write tests first** — Create tests that describe the expected behavior
2. **Run tests** — Confirm tests fail (red phase)
3. **Implement feature** — Write minimal code to make tests pass
4. **Run tests** — Confirm tests pass (green phase)
5. **Refactor** — Clean up code while keeping tests passing
6. **Update documentation** — Update AGENTS.md if new patterns are introduced

### Test-First Guidelines
- **Every new feature** must have corresponding tests (Go unit + e2e)
- **Go unit tests** go in `backend/` alongside the code they test
- **E2E tests** go in `frontend/tests/e2e/` with descriptive filenames
- **Use `test.describe.serial()`** when tests share state (same task/messages)
- **Use unique identifiers** (`Date.now()`) to avoid test interference
- **Graceful failures** — Tests should pass even if LLM is slow/unavailable
- **Test the UX** — Verify UI elements, not just API responses
- **Mock LLM is default** — tests use pre-recorded fixtures, never the real LLM endpoint
- **Wait for selectors** for async operations; Do not use arbitrary timeouts or fixed delays
- **Config-modifying tests** (model switch, settings, reasoning effort, mode select) run in the `isolated` project on port 3001

### Test Structure
```typescript
test.describe.serial('Feature Name', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should do something', async ({ page }) => {
    // 1. Setup - navigate to relevant state
    // 2. Action - perform user interaction
    // 3. Assertion - verify expected outcome
  });
});
```

### Running Tests
```bash
# All e2e tests (auto-starts test server + mock LLM)
cd frontend && npx playwright test

# Specific test file
npx playwright test task-message-actions

# Specific test by name
npx playwright test --grep "should edit message"

# With UI for debugging
npx playwright test --ui

# Frontend unit tests (vitest, ~1s)
cd frontend && npm test

# All Go unit tests (~60s, parallel)
go test ./backend/... -count=1 -parallel 8
```

**Playwright config**: `frontend/playwright.config.ts` (parallel, 2 workers, 120s timeout).
**Two Playwright projects**:
- `chromium` (port 3000) — all tests except config-modifying suites
- `isolated` (port 3001, serialized) — config-modifying tests: `model-switch`, `debug-cards`, `settings-model-delete`, `reasoning-effort`, `settings-power`, `mode-select`

**Before running e2e tests**: If a prior run was interrupted, clean stale state:
```bash
lsof -ti:3000,3001,9999 | xargs kill 2>/dev/null
rm -f /tmp/kanban-test-setup.lock
rm -rf /tmp/kanban-test-data /tmp/kanban-test-data-isolated /tmp/kanban-test-worktrees
```
Stale processes from interrupted runs hold ports open, and a stale lock file causes global-setup to run teardown instead of setup.

### Test Environment
E2E tests run against **two dedicated test servers** with isolated databases, fully managed by `global-setup.ts`:

| Server | Port | Data Dir | Purpose |
|--------|------|----------|----------|
| Main | 3000 | `/tmp/kanban-test-data` | Default test server (chromium project) |
| Isolated | 3001 | `/tmp/kanban-test-data-isolated` | Config-modifying tests (isolated project) |

Both servers share a mock LLM on port 9999. Test data dirs, worktrees (`/tmp/kanban-test-worktrees/`), and servers are fully torn down and rebuilt each run.

**Important**: Your development server is **killed** during test runs. Restart it manually after:
```bash
./kanban &
```

### Frontend Unit Tests
- Run: `cd frontend && npm test` (vitest, jsdom, ~1s, ~235 tests)
- Test files: `src/**/*.unit.ts`, `src/**/*.unit.svelte.ts`, `src/**/*.unit.spec.ts` — colocated in `__tests__/` directories under `src/lib/`
- Framework: `vitest` + `@testing-library/svelte`
- Config: `frontend/vitest.config.ts` (jsdom environment, `$lib` alias)
- Test modules:
  - `src/lib/api/__tests__/` — API cache utilities, extensions API
  - `src/lib/components/__tests__/` — component unit tests
  - `src/lib/services/` — reactive services (DiffCache, StreamState, StreamHandler, extension-manager, ui-injector)
  - `src/lib/composables/__tests__/` — reactive composables (multi-select, sw-update, virtual-scroll)
  - `src/lib/services/__tests__/` — reactive services (`DiffCache`, `StreamState`, `StreamHandler`)
  - `src/lib/stores/__tests__/` — app store tests
  - `src/lib/utils/__tests__/` — pure utility functions (`format-time.ts`, `diff-parser.ts`, `sort-tasks.ts`, `chat-entries.ts`)
  - `src/lib/__tests__/` — setup/alias verification
- Use `.svelte.ts` extension for any test file importing modules that use Svelte 5 compiler macros (`$state`, `$derived`, `$effect`)
- Tests are deterministic — no external dependencies (LLM, network, server)

### Go Unit Tests
- Run: `go test ./backend/... ./pkg/... -count=1 -parallel 8` (~60s, ~790 tests)
- Test packages:
  - `backend/server/` — API handlers (~175 tests)
  - `backend/extensions/` — extensions system (~66 tests)
  - `backend/store/` — database operations (~90 tests)
  - `backend/exec/` — tool execution (~315 tests)
  - `backend/git/` — git operations (~55 tests)
  - `backend/sandbox/` — gVisor sandboxing (~30 tests)
  - `backend/stream/` — git polling (~25 tests)
  - `backend/managers/` — board/task/git/project managers (~5 tests)
  - `backend/types/` — domain types and ID generation (~5 tests)
  - `backend/prompts/` — prompt templates (~5 tests)
  - `backend/crypto/` — encryption (~10 tests)
  - `backend/config/` — config loading and LLM helpers (~5 tests)
  - `backend/llama/` — LLM client (~10 tests)
  - `backend/auth/` — auth/session (~10 tests)
  - `backend/tls/` — TLS config (~10 tests)
  - `backend/agent/` — agent runner and registry (~17 tests)
  - `extensions/agent/pi/` — Pi adapter (~16 tests)
  - `backend/term/` — terminal (~2 tests)
  - `pkg/bridge/` — sandbox bridge protocol and client (~30 tests)
  - `backend/httputil/` — HTTP utilities (no tests)
- Use `testServer(t)` helper for isolated test server with temp DB
- Use `t.TempDir()` for temporary files/directories (auto-cleaned)
- Tests are deterministic — no external dependencies (LLM, network)

### Mock LLM Server
- Tests rely on a **mock LLM server** (NOT the real Neptune endpoint) for deterministic, fast results
- Auto-managed by `frontend/tests/global-setup.ts` — started before tests, stopped after
- Mock server: `frontend/tests/mock-llm.js` (Node.js HTTP server on port 9999)
- Fixtures: `frontend/tests/fixtures/` — pre-recorded SSE streaming responses
- Response routing: mock selects fixture based on request body (tools present + message keywords)
- Disable mock: `MIKE_USE_MOCK_LLM=0 npx playwright test` (uses real Neptune endpoint)

#### Recording New Fixtures
When the LLM behavior changes or new response types are needed:
1. Set `MIKE_USE_MOCK_LLM=0` to use the real Neptune endpoint
2. Run the relevant test(s) to capture real LLM responses
3. Record the SSE stream output (use browser DevTools Network tab or server logs)
4. Save as JSON array in `frontend/tests/fixtures/` — each entry is one SSE `data:` frame
5. Update `mock-llm.js` routing if new fixture needs keyword matching

#### Fixture Format
Each fixture is a JSON array of SSE chunks matching the OpenAI streaming API:
```json
[
  {"choices": [{"delta": {"content": "Hello"}, "finish_reason": null}]},
  {"choices": [{"delta": {"content": " world"}, "finish_reason": null}]},
  {"choices": [{"delta": {}, "finish_reason": "stop"}, "usage": {"prompt_tokens": 45, "completion_tokens": 2, "total_tokens": 47, "prompt_ms": 320, "predicted_ms": 100}]}
]
```

For tool calls, use `delta.tool_calls` instead of `delta.content`:
```json
{"choices": [{"delta": {"tool_calls": [{"id": "call_abc", "function": {"name": "read_file", "arguments": "{\"path\":\"README.md\"}"}, "type": "function"}]}, "finish_reason": "tool_calls"}]}
```

## Applying Changes

### Frontend
1. Edit Svelte components in `frontend/src/lib/components/`
2. Check types: `cd frontend && npm run check` (svelte-check, catches errors Vite ignores)
3. Build: `cd frontend && npm run build`
4. Server serves from `frontend/dist/` — no dev server needed
5. Browser hard refresh to pick up changes
6. **No server restart needed** — frontend-only changes are picked up immediately after build

**TypeScript errors**: Keep the codebase TS-clean. Fix all errors from `npm run check` before finishing a task.

### Backend
1. Edit Go files in `backend/`
2. Build: `go build -trimpath -o kanban .`
3. Restart server to pick up changes
4. Config persists in SQLite (`data/kanban.db`)

### Default Config
- Partial updates via PUT `/api/config` (only sends changed fields)
- Provider-based config model loaded from SQLite — no hardcoded defaults
- **Primary Providers**: config stores two required provider/model pairs — `worker_provider`/`worker_model` (chat responses) and `utility_provider`/`utility_model` (utility-type requests: title generation, summarization, commit messages, checkpoint labels). Both are required selections in Settings → Providers and may reference LLM or `agent://` providers.
- **Worker routing**: the frontend defaults chat/task model selection to the configured Worker pair (`resolveWorker` in `$lib/utils/providers.ts`).
- **Utility routing**: utility-type requests (title generation, summarization, commit messages, checkpoint labels) route through the configured Utility provider/model via `utilityTarget()` in `backend/exec/executor.go`, falling back to the task's own URL/model if unset.
- Test fixtures use `http://neptune:8010/v1` as example endpoint
- Workspace: `<project-root>/worktrees` (absolute path)

## Architecture

### Client-Server Boundary
- **The frontend client ONLY communicates through the Go server** (`localhost:3000/api/...`). It never calls the LLM endpoint directly.
- All LLM traffic (chat completions, model listing, etc.) flows: Browser → Go server → LLM server.
- This means features like "fetch models" must be proxied through a Go endpoint (e.g., `POST /api/config/fetch-models`), not called from the browser.
- All real-time communication uses WebSocket through the Go server.
- Model/provider changes are sent via WebSocket `user_message` with optional `model_name`/`llama_server_url` fields.

### Extensions System
The Extensions system replaces hardcoded LSP configurations with user-defined and downloadable extensions.

**Key files:**
- `backend/extensions/extensions.go` — Domain types (Extension, ExtensionManifest, LSPConfig, UIConfig, SandboxConfig)
- `backend/extensions/store.go` — SQLite CRUD (ExtensionStore)
- `backend/extensions/registry.go` — Registry client (FetchIndex, FetchManifest, PullImage, BuildImage)
- `backend/extensions/sandbox.go` — Sandbox manager (Create/Start/Stop/Destroy, refcount lifecycle)
- `backend/extensions/handler.go` — REST API handlers (10 endpoints)
- `backend/lsp/extension_manager.go` — Extension-driven LSP discovery (replaces hardcoded registry)
- `frontend/src/lib/services/extension-manager.svelte.ts` — Reactive singleton service
- `frontend/src/lib/services/ui-injector.svelte.ts` — UI injection framework (6 named slots)
- `frontend/src/lib/components/ConfigModal/ExtensionsTab.svelte` — Settings UI
- `frontend/src/lib/api/extensions.ts` — API client functions
- `extensions/lsp/gopls/` — gopls extension package (Dockerfile, manifest.json)
- `extensions/ui/github-issue-importer/` — GitHub issue importer UI extension
- `extensions/css/mike-light/` — Light mode CSS theme
- `extensions/agent/pi/` — Pi Coding Agent adapter (Go source → WASM)
- `extensions/registry.json` — Default registry index

**API endpoints:**
- `GET /api/extensions` — List all extensions
- `GET /api/extensions/{id}` — Get single extension
- `PUT /api/extensions/{id}` — Update extension
- `DELETE /api/extensions/{id}` — Delete extension
- `PATCH /api/extensions/{id}/enabled` — Toggle enabled
- `GET /api/extensions/registry` — Fetch registry index
- `POST /api/extensions/install` — Install from registry
- `GET /api/extensions/check-updates` — Check for updates
- `GET /api/extensions/{id}/logs` — Get sandbox logs
- `GET /api/extensions/build-progress?name=` — SSE build progress stream

**Extension types:**
- `lsp` — Language Server Protocol servers (run in gVisor sandboxes)
- `ui` — UI plugins (inject into existing components via slots)
- `agent` — AI coding agents (run as subprocesses, protocol via WASM adapter)

**LSP Discovery:** `ExtensionRegistry.Lookup()` replaces `lsp.Registry.Lookup()` to dynamically resolve language servers from installed extensions. LSP availability is determined by installed extensions, not a global toggle.

**UI Injection:** Extensions are activated based on the current scope. When a component mounts, it calls `renderSlot(scope, context)` to activate extensions registered for that scope.

| Scope | Component | Purpose |
|---|---|---|
| `task-card` | `Card.svelte` | Enhance individual task cards (e.g. status badges, custom actions) |
| `file-editor` | `FileEditor.svelte` | Enhance the code editor (e.g. toolbars, status bars) |
| `project-board` | `Board.svelte` | Enhance the project board view (e.g. board-level utilities) |
| `task-chat` | `TaskChat.svelte` | Enhance the chat interface (e.g. input actions, header tools) |
| `project-sidebar` | `Sidebar.svelte` | Enhance the project sidebar (e.g. project-specific tools) |
| `file-browser` | `FileBrowser.svelte` | Enhance the project directory selector modal |
| `create-task` | `CreateTaskModal.svelte` | Enhance the task creation flow (e.g. GitHub issue import) |

### UI Extensions

UI extensions are **plain JavaScript modules** — not Svelte components. Svelte components cannot be dynamically loaded from external bundles (each bundle carries its own Svelte runtime, causing `effect_orphan` errors).

**Creating a UI extension:**

1. Create a directory under `extensions/<name>/` with two files:

```text
extensions/my-extension/
├── manifest.json
└── MyExtension.js
```

2. **`manifest.json`** — Metadata and injection config:
```json
{
    "display_name": "My Extension",
    "description": "Does something useful",
    "author": "Author",
    "deprecated": false,
    "priority": 0,
    "ui": {
        "module_url": "/extensions/my-extension/MyExtension.js",
        "injection_points": ["create-task"]
    },
    "config_schema": {
        "type": "object",
        "properties": {
            "api_key": { "type": "string", "title": "API Key" }
        }
    }
}
```

**Priority** governs extension loading order. Lower values load first (default `0`). An extension with `priority: 10` loads after one with `priority: 0`. Extensions with the same priority are sorted by name.

3. **`MyExtension.js`** — ES module exporting a default `init` function:
```js
export default function init({ component, config }) {
    // Set up DOM elements, event listeners, etc.
    // The extension manages its own DOM — no Svelte rendering.

    // Return a cleanup function (called when extension is disabled/unloaded)
    return () => {
        // teardown
    };
}
```

The context object passed to `init` contains `component` (component name, e.g. `'CreateTaskModal'`) and `config` (extension config from user settings). Depending on the scope, additional fields like `taskId`, `projectId`, or `filePath` may be provided.

4. **Register in `extensions/registry.json`:**
```json
{
    "name": "my-extension",
    "version": "1.0.0",
    "type": "ui",
    "display_name": "My Extension",
    "description": "Does something useful",
    "author": "Author"
}
```

**Updating:** Edit the `.js` file and `manifest.json` directly. The Go server serves `extensions/` as static files — no build step needed. Browser hard refresh to pick up changes.

**Registry generation:** the published extensions live in the sibling repo
`../MIKE-extensions/`, and `registry.json` there is **generated** from the extension
manifests — do not edit it by hand. After adding/editing an extension manifest or
bumping a published commit, regenerate it:

```bash
python3 scripts/gen-registry.py            # regenerate ../MIKE-extensions/registry.json
python3 scripts/gen-registry.py --check    # verify it's up to date (CI)
```

The generator scans `../MIKE-extensions/<type>/<name>/manifest.json` for every
extension and emits a minimal git-pinned registry entry: the repo URL (`repo`),
the current HEAD commit of the extensions repo (`commit`, a full SHA-1 or SHA-256
hash), and the extension's subdirectory within the repo (`subdir`). On install the
verified content is materialized into `./extensions/<author>/<name>` (slugified
manifest `author`). It also keeps the fields the Browse Registry UI renders
(e.g. `ui` injection points/permissions and `proxy` allowed domains); the full
capability config stays in the manifest.

**Deploying:** Install from registry via Settings → Extensions → "Install from Registry".

**Key constraints:**
- Module must export a `default` function accepting `{ component, config }`
- Function returns a cleanup `() => void` function
- No Svelte imports — plain DOM manipulation only
- CSS is injected via `document.createElement('style')` or inline styles
- `module_url` is a path served by the Go server's static file handler (`/extensions/...`)

**Bundling a runtime library (isolation):** A UI extension that needs a runtime
library (e.g. Wordgard for the `markdown-preview` WYSIWYG editor) must keep it
**inside its own folder** — the core codebase only ships generic capabilities.
The sandboxed extension iframe loads plain `<script>` tags (CSP `script-src
'self'`), so an ESM npm package is bundled to a single IIFE committed in the
extension dir (e.g. `extensions/ui/markdown-preview/lib/wordgard.js`). The
extension has its own `package.json` declaring that dependency and its own
build under `build/` (`npm run build` → `node build/build-wordgard.mjs`);
`node_modules/` and the transient `build/.dist/` are git-ignored. The core
`frontend/` workspace must NOT gain the extension's runtime dependencies — only
generic extension capabilities (e.g. `editor.setContent`) belong there.
Extension-specific tests are colocated under `extensions/<type>/<name>/tests/`
(loading Playwright from the frontend workspace), not in `frontend/tests/`. Shared
extension logic (e.g. the markdown↔Wordgard serializer in `serializer.js`) is a
plain script loaded by both the extension and its tests — never copy it.

**Registry:** URL sourced exclusively from the `MIKE_REGISTRY_URL` env var (no longer DB-backed; default `https://getmike.dev/extensions/registry.json`). Bare filesystem paths (no `http(s)://`/`file://` scheme) are treated as local files resolved against the working directory (e.g. `MIKE_REGISTRY_URL=../MIKE-extensions/registry.json`). Extensions are git-pinned: each entry carries `author`/`repo`/`commit`/`subdir`, and the server clones the repo, checks out exactly `commit` (a full 40-char SHA-1 or 64-char SHA-256 hash), verifies `git rev-parse HEAD` matches, and only then installs the `subdir` content into `./extensions/<author>/<name>`.

**Sandbox:** Each LSP extension runs in a dedicated gVisor sandbox. Bridge protocol uses `MethodLSP` (0x0A) for JSON-RPC passthrough.

### Agent Extensions

Agent extensions run AI coding agents (e.g., Pi) as subprocesses. The backend knows **nothing** about agent protocols — all protocol logic lives in the extension via a WASM adapter.

**Flow:** `Backend (executor) ←→ AgentRunner (I/O bridge) ←→ WASM Adapter (protocol) ←→ Agent Process`

**Key files:** `backend/agent/` (interface, runner, loader, registry), `backend/exec/loop_agent.go` (event loop), `extensions/agent/pi/` (Pi adapter)

**Adapter interface** (`AgentAdapter`): `Init(config)`, `SubprocessConfig()`, `EncodeCommand(cmd, data)`, `ParseOutput(bytes) → []Event`, `HandleUIRequest(id, method)`, `Close()`

**Event types:** `token`, `reasoning`, `tool_call`, `tool_result`, `status`, `stats`, `error`, `persist`, `turn_complete`

**Persistence:** Adapter buffers turn state and emits `EventTypePersist` with `[]PersistEntry` (pre-parsed assistant text, reasoning, tool calls/results). Executor iterates entries generically — zero agent-specific persistence in backend.

**URL scheme:** `agent://<name>` (e.g., `agent://pi`). Legacy `pi://agent` supported.

**Creating an agent extension:**

```
extensions/agent/my-agent/
├── adapter.go          # Core logic (zero backend/ imports — compiles to WASM)
├── adapter_test.go     # Unit tests (run natively)
├── wasm/main.go        # WASI reactor exports (package main, //go:wasmexport directives)
├── manifest.json       # {"agent": {"wasm_module": "my-agent.wasm"}}
└── build.sh            # GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared -o ../my-agent.wasm ./wasm
```

Adapter core must have **zero `backend/` imports** (WASM compatibility). The `wasm/main.go` wraps the adapter and exports functions via `//go:wasmexport` for the [WASI reactor](https://go.dev/blog/wasmexport) model (`-buildmode=c-shared`, Go 1.24+).

**WASM memory model (zero buffers):**
- Input: host writes data to WASM linear memory, passes as `string` param
  (Go runtime maps `string` → `(i32 ptr, i32 len)` automatically)
- Output: WASM module calls `//go:wasmimport env store_result(string)` host callback
- The host reads result data from linear memory and delivers it to Go
- `store_result` fires synchronously during `fn.Call` — before it returns — so
  a buffered channel (size 1) safely captures the result
- No shared result buffers, no `malloc`/`lastResultLen`
- The host calls `_initialize` first (runtime init), then exported functions
- All user data is JSON-serialized before passing to WASM memory
- `EventTypePersist` is the only path to DB persistence

**WASM export rules:**
- `string` params are supported (maps to `(i32, i32)` — ptr + len in linear memory)
- `string` may be used as a **parameter** only, not as a return value
- Use `string` params and `//go:wasmimport` callbacks for all data passing — no manual memory management needed
- `//go:wasmimport` can import host functions (e.g. callbacks for results)

**Example `wasm/main.go`:**
```go
package main

import (
    "encoding/json"
    "kanban/extensions/agent/my-agent"
)

var adapter *myagent.Adapter

//go:wasmimport env store_result
//go:noescape
func storeResult(data string)

//go:wasmexport Init
func Init(config string) int32 {
    adapter = myagent.NewAdapter()
    if len(config) == 0 {
        return 1
    }
    if err := adapter.Init([]byte(config)); err != nil {
        return 1
    }
    return 0
}

//go:wasmexport SubprocessConfig
func SubprocessConfig() {
    if adapter == nil {
        storeResult("")
        return
    }
    cfg := adapter.SubprocessConfig()
    data, _ := json.Marshal(cfg)
    storeResult(string(data))
}

//go:wasmexport Close
func Close() {
    if adapter != nil {
        adapter.Close()
        adapter = nil
    }
}

func main() {} // not called in reactor mode
```

The loader (`backend/agent/loader.go`) uses wazero with `WithStartFunctions("_initialize")` and `api.EncodeI32`/`api.DecodeI32` for parameter encoding.

## Common Patterns

### Client-Side Storage (`storage.ts`)
All client-side storage goes through `frontend/src/lib/storage.ts`. Never access IndexedDB directly in components.
All functions are **async** (return `Promise`).

**Single IndexedDB database:** `kanban-storage` (v3) with these object stores:
- `drafts` — draft chat text per task (key: taskId)
- `editor-drafts` — draft file content per file path (key: path)
- `editor-history` — undo/redo history per file (key: path)
- `editor-tabs` — open files, active file, pinned files per project (key: projectId)
- `collapsed-tasks` — collapsed parent task IDs (single key)
- `collapsed-sections` — collapsed board section IDs (single key)
- `collapsed-diff-files` — collapsed diff file paths (single key)
- `unsaved-files` — unsaved file paths per project (key: projectId)
- `offline-queue` — queued write actions (single key)
- `explorer-layout` — explorer column widths (single key)
- `app-settings` — generic key-value for component settings (terminal height, panel ratios, sidebar)
- `attachments` — draft image attachments per task (key: taskId)
- `board-cache` — offline mirror of server state (projects, board, config)

**Key functions:**
- `getDraftText(taskId)` / `setDraftText(taskId, text)` / `clearDraftText(taskId)` — per-task chat input drafts
- `getEditorDraft(path)` / `setEditorDraft(path, text)` / `clearEditorDraft(path)` — per-file editor drafts
- `loadEditorHistory(path)` / `saveEditorHistory(path, base, deltas)` / `clearEditorHistory(path)` — editor undo/redo
- `loadCollapsedTasks()` / `saveCollapsedTasks(ids: Set<string>)` — collapsed parent task IDs (returns `Set<string>`)
- `loadCollapsedSections()` / `saveCollapsedSections(ids: Set<string>)` — collapsed board sections (returns `Set<string>`)
- `loadCollapsedDiffFiles()` / `saveCollapsedDiffFiles(ids: Set<string>)` — collapsed diff file paths (returns `Set<string>`)
- `loadUnsavedFiles(projectId)` / `saveUnsavedFiles(projectId, paths)` — unsaved file paths
- `loadQueue()` / `saveQueue(actions)` — offline action queue
- `loadExplorerLayout()` / `saveExplorerLayout(layout)` — explorer column widths
- `loadAppSetting(key, default)` / `saveAppSetting(key, value)` — generic key-value settings
- `saveDraftAttachments(taskId, atts)` / `loadDraftAttachments(taskId)` / `clearDraftAttachments(taskId)` — per-task image drafts
- `setBoardCache(key, value)` / `getBoardCache<T>(key)` / `boardCacheKey(projectId)` — offline board state cache
- `gc(allTaskIds: Set<string>, allProjectPaths?: Set<string>, allProjectIds?: Set<string>)` — garbage-collect ALL stale entries

**GC trigger:** Called in `Board.svelte` after the board is loaded. Components should NOT call `gc()` themselves.

### Svelte 5 Reactivity
- Use `$state()` for reactive variables
- Use `$derived()` for simple computed expressions
- Use `$derived.by(() => { ... })` for complex multi-line computations (used extensively in Card.svelte, Board.svelte, etc.)
- Use `$props()` for component props
- Svelte 4 stores: import `get` from `svelte/store` for `.get()` calls

### Service Reactivity (`.svelte.ts`)
Svelte 5 runes work inside classes in `.svelte.ts` files — no version bumper or `$effect` sync needed.

**Pattern:**
```ts
// $lib/services/my-cache.svelte.ts
class MyCache {
    state = $state({ items: [] as Item[], loading: false });
    addItem(item: Item) { this.state.items = [...this.state.items, item]; }
}
```
```svelte
<!-- Component -->
<script lang="ts">
    const cache = new MyCache();
    // Read cache.state directly — Svelte tracks deep changes automatically
</script>
{#each cache.state.items as item}...
```

**Why:** `$state()` provides deep proxy-based reactivity. Assigning `this.state.items = [...newItems]` notifies any component reading `cache.state.items`. No manual `version` counter, `_bump()`, or `$effect` mirror needed.

**Directory conventions:** `$lib/services/` for plain TS classes/factories, `$lib/composables/` for reactive helpers, `$lib/utils/` for pure functions.

### WebSocket Protocol
- Client sends: `{"type": "user_message", "data": {"content": "..."}}`
- Server processes via `stream.UserMessage` → broadcasts echo as `WSTypeUserMessage` event
- `WSMessage.Data` is `json.RawMessage` in Go — arrives as a parsed JS value (object or string) in the browser
- Use optimistic UI for user messages (add immediately, skip echo)

### Task IDs
- Format: `word-word-word` (e.g., `frost-amber-quartz`)
- Generated from 263-word dictionary in `backend/types/idgen.go`
- Thread IDs use 2-word format (e.g., `voice-ash`) via `GenerateThreadID()`
- URL: `/?task=frost-amber-quartz`

### Chat UI Rules
- Never disguise system messages as user messages
- Streaming cursor only shown during active LLM response
- `complete` event sent when LLM finishes (text-only or tool calls)
- User messages: right-aligned with rounded background bubble
- Action icons (copy/edit/delete) always visible
- Edit mode: replace message + discard conversation tree
- Stats format: `X tokens · Ys · Z tok/s` (completion tokens, not total)

### Diff Viewer
- Raw unified diff from `getTaskDiff()` is parsed into structured rows in `CardDetail.svelte`
- Row types: `file-header` (`diff --git`, `---`, `+++`), `index`, `hunk-header` (`@@`), `added` (`+`), `removed` (`-`), `context` (` `)
- Each code row renders as CSS grid: `2rem` line number gutter + content
- Color coding: `rgba(63,185,80,0.15)` for additions, `rgba(248,81,73,0.15)` for deletions
- Diff rendering lives in `CardDetail.svelte` (parsed rows) and `DiffViewer.svelte` (staged/unstaged file browser)

### Reactive Status Badge
- Use `$derived.by(() => { ... })` for task status badges (see `Card.svelte` for the pattern)
- `getStatusBadge()` is a component-local function (defined in `DiffViewer.svelte` for diff files, `GitStatusPanel.svelte` for git entries) — not a shared utility
- This ensures the badge updates automatically when `task.execution_state` changes (e.g., idle → running → completed)

### Sticky Section Headers
- Use `position: sticky; top: 0; z-index: 10;` on section headers within scrollable containers (diff file headers use z-index: 10)
- Add matching `background` color so scrolling content doesn't show through
- Add `border-bottom` for visual separation from content below

## Licensing

This project is licensed under **Apache 2.0** (`LICENSE`, `NOTICE`). Dependency
licenses are **not** changed by our license — each library keeps its own license
and copyright, which must be preserved. The project's dependency inventory and
license obligations live in `THIRD_PARTY_NOTICES.md`.

### Files
- `LICENSE` — Full Apache-2.0 text (download from https://www.apache.org/licenses/LICENSE-2.0.txt)
- `NOTICE` — Project copyright + reproduced NOTICE files from Apache-2.0 deps
- `THIRD_PARTY_NOTICES.md` — Inventory of Go + frontend deps and their licenses

### Extensions (`./extensions`) — independently-licensed projects
`./extensions` organizes several **separate projects**, each hosted independently.
Each extension is therefore licensed on its own and carries its own declarations:

- Every extension directory has its own `LICENSE` (Apache-2.0), `NOTICE`, and
  `THIRD_PARTY_NOTICES.md`.

**When adding a new extension:** give it its own `LICENSE`, `NOTICE`, and
`THIRD_PARTY_NOTICES.md`. If it bundles a third-party library, keep that library's
license notice intact and never label it Apache-2.0. When adding a new dependency
to an existing extension, update that extension's `THIRD_PARTY_NOTICES.md`.

### Rules
- **Never remove** a dependency's license/copyright header or NOTICE file.
- Do **not** introduce **GPL/AGPL** dependencies — they are incompatible with
  Apache-2.0 and would force the whole project toward GPL. (MPL-2.0 is OK; do
  not modify MPL-2.0 files unless you can release the changes under MPL-2.0.)
- Apache-2.0 deps that ship a `NOTICE` file require its content to be preserved
  in our `NOTICE` file (currently: `wazero`).
- `lightningcss` (MPL-2.0) and `caniuse-lite` (CC-BY-4.0) are build/dev-only;
  keep them out of the distributed runtime artifact.

### Keeping Third-Party Notices Updated
Regenerate the notices **whenever** a dependency is added, removed, or upgraded.

```bash
# Go — verify every module's license is permissive & update the Go table
go mod tidy
go-licenses report ./... --template notices.tmpl   # or inspect `go list -m all`

# Frontend — inspect the production dependency license distribution
cd frontend && npx license-checker --production --summary
cd frontend && npx license-checker --production --csv
```

Then update `THIRD_PARTY_NOTICES.md` to match, and re-check:

1. Any new **Apache-2.0** dep with a `NOTICE` file → append its content to `NOTICE`.
2. Any new **MPL / CC / copyleft** dep → review compatibility and note it.
3. Any new **GPL/AGPL** dep → reject it (see Rules above).
4. Bump the "Last updated" date in `THIRD_PARTY_NOTICES.md`.

- **Third-party bundled files** (e.g. `extensions/ui/markdown-preview/lib/wordgard.js`)
  must keep their **own** license notice and must **not** get an Apache-2.0 header.
- After editing Go files, run `gofmt -w` on them.

## Debugging Checklist
1. Server running? `ps aux | grep kanban`
2. Config correct? `curl /api/config`
3. Worktrees dir exists? `ls .worktrees/`
4. Frontend built? `cd frontend && npm run build`
5. Browser cache cleared? Hard refresh
6. Check logs: server stdout or `/tmp/kanban.log`
7. E2E tests passing? `cd frontend && npx playwright test`
8. Go tests passing? `go test ./backend/... -count=1 -parallel 8`
9. Stale test state? `lsof -ti:3000,3001,9999 | xargs kill 2>/dev/null && rm -f /tmp/kanban-test-setup.lock && rm -rf /tmp/kanban-test-data /tmp/kanban-test-data-isolated`

### E2E Test Failures
- **`ERR_CONNECTION_REFUSED`**: Stale processes from interrupted test runs hold ports. Run cleanup above. Check `lsof -i :3000` for conflicts.
- **`waiting for locator('.project-item')`**: Test project not created — check global-setup logs
- **`waiting for locator('.card')`**: No tasks exist — test expects existing tasks
- **`waiting for locator` (drag-and-drop)**: Headless mode can be slow — add `waitForTimeout` after drag, increase predicate timeout
- **Mock LLM routing wrong**: Check `mock-llm.js` keyword matching and fixture files
- **Sandbox toggle blocked by running task**: Test runs in parallel with other tests — move to `isolated` project in `playwright.config.ts`

### Go Test Failures
- **`unable to open database file`**: Use `testServer(t)` helper which creates temp DB
- **`context deadline exceeded`**: Increase test timeout or optimize slow operations
- **Sandbox tests fail**: Ensure gVisor is installed (`runsc --version`)

### Diagnostic Tests (Standalone Playwright Scripts)
For ad-hoc investigation (e.g., tracking API request patterns, debugging race conditions), write standalone Playwright scripts in `frontend/tests/` as `.mjs` files. These run against a live dev server, bypassing the e2e test harness.

**Pattern:**
```javascript
import { chromium } from 'playwright';

const requests = [];
const t0 = Date.now();

// Intercept specific endpoints
await page.route('**/api/projects/*/tasks/*/diff/file*', async (route) => {
    const url = new URL(route.request().url());
    const response = await route.fetch();
    const body = await response.json().catch(() => ({}));
    requests.push({ time: Date.now() - t0, path: url.searchParams.get('path'), keys: Object.keys(body) });
    await route.fulfill({ response, status: response.status(), headers: response.headers(), body: await response.body() });
});
```

**Key points:**
- Start server with `MIKE_LOGIN_PASSWORD=test setsid ./kanban &` + `disown` to keep it alive after the script runs
- Use `page.evaluate()` with CSRF token from `/api/auth/session` for API calls (POST/PUT/DELETE)
- Use `route.fulfill()` (not `route.respond()`) — the old API was removed in newer Playwright
- Track timing with `Date.now() - t0` offsets to correlate requests with server events
- Use `page.screenshot({ path: '/tmp/debug.png' })` for visual inspection
- Run: `node frontend/tests/debug-script.mjs`

### Crash Debugging (Flight Recorder)
- Go flight recorder runs **always-on** (~1% overhead), writing to `data/kanban.trace`
- Trace is rotated every 30s — on crash, the file contains ~30s of pre-crash data
- Analyze a crash trace: `go tool trace -http=:8080 data/kanban.trace` (opens browser UI)
- Rotated traces kept as `data/kanban.trace.old` — overwritten each rotation
- Key views: goroutine scheduling, heap allocations, GC activity, syscalls
