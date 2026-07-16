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
CGO_ENABLED=0 GOOS=linux go build -o sandbox/sandbox-bridge -ldflags="-s -w" ./pkg/bridge/agent
```

**Runtime discovery:** The server finds `sandbox-bridge` via:
1. `KANBAN_AGENT_PATH` environment variable
2. Next to the `kanban` binary (`<kanban-dir>/sandbox/sandbox-bridge`)
3. Current directory (`./sandbox/sandbox-bridge`)

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
| **Custom certs** | `--tls-cert=path --tls-key=path` or `KANBAN_TLS_CERT` + `KANBAN_TLS_KEY` | Uses user-provided certificate and key files |
| **Disable TLS** | `--no-tls` or `KANBAN_TLS=0` | Plain HTTP, no TLS |

### CLI Flags

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--port` | `PORT` | `3000` | Server listen port |
| `--data-dir` | `DATA_DIR` | `./data` | Data directory for DB, TLS certs, trace files |
| `--worktrees-dir` | `WORKTREES_DIR` | `./worktrees` | Directory for git worktrees |
| `--no-tls` | `KANBAN_TLS=0` | *(disabled)* | Disable TLS (plain HTTP) |
| `--tls-cert` | `KANBAN_TLS_CERT` | *(auto-generated)* | Path to TLS certificate file |
| `--tls-key` | `KANBAN_TLS_KEY` | *(auto-generated)* | Path to TLS private key file |

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
- **Config-modifying tests** (sandbox toggle, model selector, settings) run in the `isolated` project on port 3001

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
- `isolated` (port 3001, serialized) — config-modifying tests: `sandbox-toggle`, `sandbox-tool-calls`, `model-selector`, `settings-*`, `reasoning-effort`, `debug-cards`

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
- Run: `cd frontend && npm test` (vitest, jsdom, ~1s, ~49 tests)
- Test files: `src/**/*.unit.ts` — colocated in `__tests__/` directories under `src/lib/`
- Framework: `vitest` + `@testing-library/svelte`
- Config: `frontend/vitest.config.ts` (jsdom environment, `$lib` alias)
- Test modules:
  - `src/lib/services/__tests__/` — reactive services (`DiffCache`, `StreamState`, `StreamHandler`)
  - `src/lib/utils/__tests__/` — pure utility functions (`format-time.ts`)
- Use `.svelte.ts` extension for any test file importing modules that use Svelte 5 compiler macros (`$state`, `$derived`, `$effect`)
- Tests are deterministic — no external dependencies (LLM, network, server)

### Go Unit Tests
- Run: `go test ./backend/... -count=1 -parallel 8` (~60s, ~560 tests)
- Test packages:
  - `backend/server/` — API handlers (149 tests)
  - `backend/store/` — database operations (89 tests)
  - `backend/exec/` — tool execution (184 tests)
  - `backend/git/` — git operations (46 tests)
  - `backend/sandbox/` — gVisor sandboxing (22 tests)
  - `backend/stream/` — git polling (15 tests)
  - `backend/crypto/` — encryption (17 tests)
  - `backend/pi/` — pi agent integration (13 tests)
  - `backend/auth/` — auth/session (9 tests)
  - `backend/tls/` — TLS config (9 tests)
  - `backend/config/` — config loading (2 tests)
  - `backend/llama/` — LLM client (2 tests)
  - `backend/term/` — terminal (2 tests)
- Use `testServer(t)` helper for isolated test server with temp DB
- Use `t.TempDir()` for temporary files/directories (auto-cleaned)
- Tests are deterministic — no external dependencies (LLM, network)

### Mock LLM Server
- Tests rely on a **mock LLM server** (NOT the real Neptune endpoint) for deterministic, fast results
- Auto-managed by `frontend/tests/global-setup.ts` — started before tests, stopped after
- Mock server: `frontend/tests/mock-llm.js` (Node.js HTTP server on port 9999)
- Fixtures: `frontend/tests/fixtures/` — pre-recorded SSE streaming responses
- Response routing: mock selects fixture based on request body (tools present + message keywords)
- Disable mock: `USE_MOCK_LLM=0 npx playwright test` (uses real Neptune endpoint)

#### Recording New Fixtures
When the LLM behavior changes or new response types are needed:
1. Set `USE_MOCK_LLM=0` to use the real Neptune endpoint
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
2. Build: `go build -o kanban .`
3. Restart server to pick up changes
4. Config persists in SQLite (`data/kanban.db`)

### Default Config
- Partial updates via PUT `/api/config` (only sends changed fields)
- Required fields: `llama_server_url`, `model_name`, `workspace_dir`
- LLM endpoint: `http://neptune:8010/v1`
- Model: `Qwen3.6-35B-A3B` (NOT `-Q8_0.gguf`)
- Workspace: `<project-root>/worktrees` (absolute path)

## Architecture

### Client-Server Boundary
- **The frontend client ONLY communicates through the Go server** (`localhost:3000/api/...`). It never calls the LLM endpoint directly.
- All LLM traffic (chat completions, model listing, etc.) flows: Browser → Go server → LLM server.
- This means features like "fetch models" must be proxied through a Go endpoint (e.g., `POST /api/config/fetch-models`), not called from the browser.
- All real-time communication uses WebSocket through the Go server.
- Model/provider changes are sent via WebSocket `user_message` with optional `model_name`/`llama_server_url` fields.

## Common Patterns

### Client-Side Storage (`storage.ts`)
All client-side storage goes through `frontend/src/lib/storage.ts`. Never access IndexedDB directly in components.
All functions are **async** (return `Promise`).

**Single IndexedDB database:** `kanban-storage` (v2) with these object stores:
- `drafts` — draft chat text per task (key: taskId)
- `editor-drafts` — draft file content per file path (key: path)
- `editor-history` — undo/redo history per file (key: path)
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
- `loadCollapsedTasks()` / `saveCollapsedTasks(ids)` — collapsed parent task IDs
- `loadCollapsedSections()` / `saveCollapsedSections(ids)` — collapsed board sections
- `loadCollapsedDiffFiles()` / `saveCollapsedDiffFiles(ids)` — collapsed diff file paths
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
- Use `$derived()` for computed values (NOT `by()`)
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
- Server echoes: `{"type": "user_message", "data": {"role": "user", "content": "..."}}`
- `json.RawMessage` in Go = already-parsed object in JS (check `typeof` before parsing)
- Use optimistic UI for user messages (add immediately, skip echo)

### Task IDs
- Format: `word-word-word` (e.g., `frost-amber-quartz`)
- Generated from 128-word dictionary in `backend/types/idgen.go`
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
- Reference design: `examples/kanban/web-ui/src/components/shared/diff-renderer.tsx`

### Reactive Status Badge
- Use `$derived(getStatusBadge(task))` instead of `$state()` + manual `onMount` assignment
- This ensures the badge updates automatically when `task.status_state` changes (e.g., idle → running → completed)

### Sticky Section Headers
- Use `position: sticky; top: 0; z-index: 5;` on section headers within scrollable containers
- Add matching `background` color so scrolling content doesn't show through
- Add `border-bottom` for visual separation from content below

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
- Start server with `KANBAN_PASSWORD=test setsid ./kanban &` + `disown` to keep it alive after the script runs
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
