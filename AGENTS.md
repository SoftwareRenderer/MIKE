# AGENTS.md

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
- **Check server is running** before e2e tests
- **Use background mode** (`&`) for development
- **Test Driven Development** to validate functionality

## Development Workflow (TDD)

### Feature Development Process
1. **Write tests first** — Create e2e tests that describe the expected behavior
2. **Run tests** — Confirm tests fail (red phase)
3. **Implement feature** — Write minimal code to make tests pass
4. **Run tests** — Confirm tests pass (green phase)
5. **Refactor** — Clean up code while keeping tests passing
6. **Update documentation** — Update AGENTS.md if new patterns are introduced

### Test-First Guidelines
- **Every new feature** must have corresponding e2e tests
- **Tests go in** `frontend/tests/e2e/` with descriptive filenames
- **Use `test.describe.serial()`** when tests share state (same task/messages)
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
# All tests
cd frontend && npx playwright test

# Specific test file
npx playwright test task-message-actions

# Specific test by name
npx playwright test --grep "should edit message"

# With UI for debugging
npx playwright test --ui
```

### Testing Rules
- **Server must be running** on `:3000` before tests
- **Mock LLM is enabled by default** — tests use pre-recorded responses, not real Neptune endpoint
- **Tests must NOT depend on real LLM** — use mock fixtures for deterministic results
- **Use timeouts** for async operations (LLM responses, network calls)
- **Wait for selectors** instead of fixed delays where possible
- **Clean up after tests** — Don't leave server in broken state
- **Serial tests** when modifying shared state (messages, tasks)

## Testing Workflow

### E2E Tests
- Run: `cd frontend && npx playwright test`
- Tests in `frontend/tests/e2e/` using Playwright with Chromium
- Server must be running on `:3000` before tests
- Config: `frontend/playwright.config.ts` (sequential, no retries, baseURL: localhost:3000)
- Test suites:
  - `task-basic.spec.ts` — board, task creation, navigation
  - `task-chat.spec.ts` — chat UI, messaging, layout
  - `task-actions.spec.ts` — start, delete, confirm dialogs
  - `task-url.spec.ts` — URL navigation, `?task=` parameter
  - `task-message-actions.spec.ts` — message actions, editing, stats
  - `llm-connection.spec.ts` — LLM integration, tool calls

### Mock LLM Server
- Tests rely on a **mock LLM server** (NOT the real Neptune endpoint) for deterministic, fast results
- Managed by `frontend/tests/global-setup.ts` — auto-starts before tests, restores config after
- Mock server: `frontend/tests/mock-llm.js` (Node.js HTTP server on port 9999)
- Fixtures: `frontend/tests/fixtures/` — pre-recorded SSE streaming responses
- Response routing: mock selects fixture based on request body (tools present + message keywords)
- Disable mock: `USE_MOCK_LLM=0 cd frontend && npx playwright test` (uses real LLM)

#### Recording New Fixtures
When the LLM behavior changes or new response types are needed:
1. Set `USE_MOCK_LLM=0` to use the real Neptune endpoint (`http://neptune:8010/v1`)
2. Run the relevant test(s) to capture real LLM responses
3. Record the SSE stream output (use browser DevTools Network tab or add logging to `backend/llama.go`)
4. Save as JSON array in `frontend/tests/fixtures/` — each entry is one SSE `data:` frame
5. Format: `{"choices": [{"delta": {"content": "..."}, "finish_reason": null}]}`
6. Final frame includes `finish_reason: "stop"` and `usage` with token counts/timing
7. Update `mock-llm.js` routing if new fixture needs keyword matching

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

### Backend Tests
- Pre-existing: `go test ./...` (fails with "unable to open database file" — known issue)
- Unit tests in `backend/store_test.go`

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

## Debugging Checklist
1. Server running? `ps aux | grep kanban`
2. Config correct? `curl /api/config`
3. Worktrees dir exists? `ls worktrees/`
4. Frontend built? `npm run build`
5. Browser cache cleared? Hard refresh
6. Check logs: server stdout or `/tmp/kanban.log`
7. Tests passing? `npx playwright test`
