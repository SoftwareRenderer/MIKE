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

### Git/Worktree
- Worktrees in `/home/mwren/projects/kanban/worktrees/`
- Baseline branch: `__kanban_baseline_<taskID>`
- Always checkout to `main` before recreating baseline branch

## Debugging Checklist
1. Server running? `ps aux | grep kanban`
2. Config correct? `curl /api/config`
3. Worktrees dir exists? `ls worktrees/`
4. Frontend built? `npm run build`
5. Browser cache cleared? Hard refresh
6. Check logs: server stdout or `/tmp/kanban.log`
7. Tests passing? `npx playwright test`
