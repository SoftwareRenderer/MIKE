# AGENTS.md

## Required Dependencies

### gVisor

gVisor (`runsc`) is required for project sandboxing. When a project has sandboxing enabled, task tool calls and terminal sessions run inside gVisor-isolated containers.

**Install:**
```bash
sudo bash scripts/install-gvisor.sh
```

**Verify:**
```bash
runsc --version
podman run --rm --runtime runsc --runtime-flag ignore-cgroups=true --security-opt label=disable alpine echo hello
```

**Notes:**
- gVisor must be installed system-wide (`/usr/local/bin/runsc`) — it needs root-level cgroup access
- Podman sandbox commands run via `podman --runtime runsc`
- Without gVisor, projects with sandboxing enabled will refuse to start tasks (hard error, no fallback)

## Server Management

The agent is responsible for starting/stopping the server.

```bash
# Start (background)
cd /home/mwren/projects/kanban && ./kanban &

# Stop
pkill -f "./kanban"

# Verify running
curl -s http://localhost:3000/api/config
```

### Rules
- **Always restart** after backend changes
- **Never leave running** after a crash or panic
- **E2E tests manage their own server** — global-setup starts/stops a test server automatically
- **Restart manually after tests** — your dev server is killed during test runs
- **Use background mode** (`&`) for development
- **Test Driven Development** to validate functionality

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
- **Use `test.describe.serial()`** when e2e tests share state (same task/messages)
- **Use unique identifiers** (`Date.now()`) to avoid test interference
- **Graceful failures** — Tests should pass even if LLM is slow/unavailable
- **Test the UX** — Verify UI elements, not just API responses

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

# All Go unit tests (~33s, parallel)
go test ./backend/... -count=1 -parallel 8
```

**Before running e2e tests**: If a prior run was interrupted, clean stale state:
```bash
lsof -ti:3000,3001,9999 | xargs kill 2>/dev/null
rm -f /tmp/kanban-test-setup.lock /tmp/kanban-test*.db
rm -rf /tmp/kanban-test-worktrees
```
Stale processes from interrupted runs hold ports open, and a stale lock file causes global-setup to run teardown instead of setup.

### Test Server Lifecycle
E2E tests run against **two dedicated test servers** with isolated databases, fully managed by `global-setup.ts`:

| Server | Port | DB | Purpose |
|--------|------|----|----------|
| Main | 3000 | `/tmp/kanban-test.db` | Default test server (chromium project) |
| Isolated | 3001 | `/tmp/kanban-test-isolated.db` | Config-modifying tests (isolated project) |

Both servers share a mock LLM on port 9999. Test worktrees: `/tmp/kanban-test-worktrees/`.

1. **Setup** (before tests): Starts both kanban servers with test DBs, creates test projects + tasks, starts mock LLM
2. **Teardown** (after tests): Stops all servers, deletes test DBs + worktrees

**Important**: Your development server is **killed** during test runs. Restart it manually after:
```bash
cd /home/mwren/projects/kanban && ./kanban &
```

### Test Environment
- **Test DBs**: `/tmp/kanban-test.db`, `/tmp/kanban-test-isolated.db` (deleted after each run)
- **Test worktrees**: `/tmp/kanban-test-worktrees/` (deleted after each run)
- **Test git repo**: `/tmp/kanban-test-repo/` (persistent, recreated if missing)
- **Lock file**: `/tmp/kanban-test-setup.lock` (coordinates setup/teardown phases)

### Testing Rules
- **Servers are auto-managed** — global-setup starts/stops test servers
- **Mock LLM is enabled by default** — tests use pre-recorded responses, not real Neptune endpoint
- **Tests must NOT depend on real LLM** — use mock fixtures for deterministic results
- **Use timeouts** for async operations (LLM responses, network calls)
- **Wait for selectors** instead of fixed delays where possible
- **Full teardown/rebuild** — test DBs are deleted and recreated each run
- **Serial tests** when modifying shared state (messages, tasks)
- **Config-modifying tests** (sandbox toggle, model selector, settings) run in the `isolated` project on port 3001 to avoid interfering with other tests

## Testing Workflow

### E2E Tests (Playwright)
- Run: `cd frontend && npx playwright test`
- Tests in `frontend/tests/e2e/` using Playwright with Chromium
- Servers auto-managed by `global-setup.ts` (ports 3000 + 3001, mock LLM on 9999)
- Config: `frontend/playwright.config.ts` (parallel, 2 workers, 120s timeout)
- **Two Playwright projects**:
  - `chromium` (port 3000) — all tests except config-modifying suites
  - `isolated` (port 3001, serialized) — config-modifying tests: `sandbox-toggle`, `sandbox-tool-calls`, `model-selector`, `settings-*`, `reasoning-effort`, `debug-cards`

### Go Unit Tests
- Run: `go test ./backend/... -count=1 -parallel 8` (~33s, ~196 tests)
- Test packages:
  - `backend/server/` — API handlers (102 tests)
  - `backend/store/` — database operations (90+ tests)
  - `backend/sandbox/` — gVisor sandboxing (6 tests)
  - `backend/stream/` — git polling (1 test)
  - `backend/exec/` — command execution (3 tests)
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

### Manual Testing
1. Restart server: `pkill -f "./kanban" && ./kanban`
2. Verify config: `curl -s http://localhost:3000/api/config`
3. Test WebSocket: use Python `websocket` library or browser console
4. Check logs: `tail -f /tmp/kanban.log` (if redirected)

## Applying Changes

### Frontend
1. Edit Svelte components in `frontend/src/lib/components/`
2. Build: `cd frontend && npm run build`
3. Server serves from `frontend/dist/` — no dev server needed
4. Browser hard refresh to pick up changes
5. **No server restart needed** — frontend-only changes are picked up immediately after build

### Backend
1. Edit Go files in `backend/`
2. Build: `go build -o kanban .`
3. Restart server to pick up changes
4. Config persists in SQLite (`data/kanban.db`)

### Config Updates
- Partial updates via PUT `/api/config` (only sends changed fields)
- Required fields: `llama_server_url`, `model_name`, `workspace_dir`
- LLM endpoint: `http://neptune:8010/v1`
- Model: `Qwen3.6-35B-A3B` (NOT `-Q8_0.gguf`)
- Workspace: `/home/mwren/projects/kanban/worktrees` (absolute path)

## Architecture

### Client-Server Boundary
- **The frontend client ONLY communicates through the Go server** (`localhost:3000/api/...`). It never calls the LLM endpoint directly.
- All LLM traffic (chat completions, model listing, etc.) flows: Browser → Go server → LLM server.
- This means features like "fetch models" must be proxied through a Go endpoint (e.g., `POST /api/config/fetch-models`), not called from the browser.

### WebSocket Protocol
- All real-time communication uses WebSocket through the Go server
- Model/provider changes are sent via WebSocket `user_message` with optional `model_name`/`llama_server_url` fields

## Common Patterns

### Svelte 5 Reactivity
- Use `$state()` for reactive variables
- Use `$derived()` for computed values (NOT `by()`)
- Use `$props()` for component props
- Svelte 4 stores: import `get` from `svelte/store` for `.get()` calls

### WebSocket Protocol
- Client sends: `{"type": "user_message", "data": {"content": "..."}}`
- Server echoes: `{"type": "user_message", "data": {"role": "user", "content": "..."}}`
- `json.RawMessage` in Go = already-parsed object in JS (check `typeof` before parsing)
- Use optimistic UI for user messages (add immediately, skip echo)

### Task IDs
- Format: `word-word-word` (e.g., `frost-amber-quartz`)
- Generated from 96-word dictionary in `backend/managers.go`
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
9. Stale test state? `lsof -ti:3000,3001,9999 | xargs kill 2>/dev/null && rm -f /tmp/kanban-test-setup.lock /tmp/kanban-test*.db`

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

### Crash Debugging (Flight Recorder)
- Go flight recorder runs **always-on** (~1% overhead), writing to `data/kanban.trace`
- Trace is rotated every 30s — on crash, the file contains ~30s of pre-crash data
- Analyze a crash trace: `go tool trace -http=:8080 data/kanban.trace` (opens browser UI)
- Rotated traces kept as `data/kanban.trace.old` — overwritten each rotation
- Key views: goroutine scheduling, heap allocations, GC activity, syscalls
