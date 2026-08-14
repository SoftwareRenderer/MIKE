# TODO

## Goal
Add a per-provider `max_requests_per_second` setting (0 = unlimited) and enforce it in the ConcurrencyGate so LLM request starts for a provider URL are spaced ≥ 1/N s apart.

## Tasks

### 1. Red phase — write new tests first
- [x] `backend/exec/gate_test.go`: add `TestAcquire_RateLimit_Spacing` (concurrency=5, maxRPS=4, 3 sequential acquires ≥180ms apart, total ≥0.45s)
- [x] `backend/exec/gate_test.go`: add `TestAcquire_RateLimit_ZeroUnlimited` (maxRPS=0, 10 sequential acquires < 100ms total)
- [x] `backend/exec/gate_test.go`: add `TestAcquire_RateLimit_PerProviderIsolation` (A's rate wait doesn't delay B)
- [x] `backend/exec/gate_test.go`: add `TestAcquire_RateLimit_Cancellation` (ctx cancelled after ~50ms while rate-blocked; prompt error; state consistent)
- [x] `backend/exec/gate_test.go`: add `TestAcquire_RateLimit_WithSlotWait` (concurrency=1, maxRPS=1; slot+rate waits compose, total ≥ ~1s)
- [x] `backend/exec/gate_test.go`: add `TestAcquire_RateLimit_FIFO` (two queued waiters served first-then-second)
- [x] `backend/config/config_test.go`: add test for `UpdateConfig` persisting `MaxRequestsPerSecond` + round-trip via `HandleConfig` GET/PUT (JSON `max_requests_per_second`)
- [x] `backend/config/config_test.go`: add test for `UpdateConfig` clamping negative `MaxRequestsPerSecond` to 0
- [x] `backend/config/config_test.go`: add `GetProviderMaxRequestsPerSecond` test (trailing-slash tolerant; 0 for unset/unknown)
- [x] Confirm red: `go test ./backend/exec/ ./backend/config/ -count=1` fails (compile errors count as red)

### 2. Types + config changes
- [x] `backend/types/domain.go`: add `MaxRequestsPerSecond int \`json:"max_requests_per_second"\`` to `Provider` next to `Concurrency`
- [x] `backend/config/llm.go`: add `GetProviderMaxRequestsPerSecond(serverURL string) int` mirroring `GetProviderConcurrency` URL matching; 0 for missing/negative
- [x] `backend/config/config.go` `UpdateConfig`: clamp `MaxRequestsPerSecond < 0` → 0 in the per-provider loop

### 3. Gate implementation
- [x] `backend/exec/gate.go`: extend `providerState` with `nextAllowed time.Time`
- [x] `backend/exec/gate.go`: change signature to `Acquire(ctx, providerURL string, concurrency, maxRPS int) (int, *queueEntry, error)`; grant iff `ps.active < concurrency && now >= ps.nextAllowed` (rate part only when maxRPS > 0); on grant set `ps.nextAllowed = now + 1s/maxRPS` when maxRPS > 0; keep `lastAcquire` tracking
- [x] `backend/exec/gate.go`: queued waiter loops — on `entry.done` wake re-check under lock (don't steal if rate window pending; if this entry isn't the front-most unserved waiter for its provider, go back to waiting); select over `entry.done`, `ctx.Done()`, and `time.After(rateRemaining)` only when rateRemaining > 0; cancellation semantics unchanged; `Release`/`Position`/`EstimateWait`/`Close` unchanged
- [x] Update all existing `g.Acquire(ctx, url, n)` calls in `gate_test.go` to `g.Acquire(ctx, url, n, 0)`

### 4. Production call sites
- [x] `backend/exec/executor.go` `RunWithGate`: fetch `maxRPS := te.configStore.GetProviderMaxRequestsPerSecond(serverURL)` and pass into `te.gate.Acquire(ctx, serverURL, concurrency, maxRPS)`
- [x] `backend/exec/loop.go` (~line 604/629): fetch `maxRPS` alongside `concurrency` and pass into `te.gate.Acquire(...)`

### 5. Verification
- [x] `go test ./backend/exec/ -count=1` green
- [x] `go test ./backend/... -count=1 -parallel 8` green
- [x] `gofmt -w` on all changed Go files; `go vet ./backend/...` clean

## Notes
- `maxRPS < 1` is treated as 0 (unlimited) — no negative intervals.
- Agent (subprocess) providers don't go through the gate — no change needed.
- `HandleConfig` PUT merges the `providers` array wholesale, so the new plain field flows through GET/PUT automatically; only clamping is needed.
- TDD: tests written first, red confirmed, then implementation.
- Keep comments concise (project convention).

---

# TODO — frontend: per-provider "Max requests/sec" (Settings → Providers)

## Goal
Add a per-provider "Max requests/sec" option (integer input, min 0, `0` = unlimited, LLM providers only) to Settings → Providers, persisted via `max_requests_per_second` through `PUT/GET /api/config`.

## Tasks

### 1. E2E test first (red phase)
- [x] Add `test.describe.serial('Provider max requests per second')` to `frontend/tests/e2e/settings-provider.spec.ts` with three tests:
  - API round-trip: `apiPutConfig` a provider with `max_requests_per_second: 5` (+ url, models, concurrency), `waitForConfig`, `apiGetConfig` → assert `=== 5`
  - UI show & persist: seed config with value 5, open Settings, expand the LLM provider card, assert "Max requests/sec" input visible with value 5, set to 3, Save, re-open, expand, assert 3; also assert `apiGetConfig` saved value is 3
  - New provider default: click "+ Add Provider", assert the new (expanded) LLM card shows Max requests/sec input with value 0, then cancel without saving
- [x] Run `npx playwright test settings-provider` (clean stale state first) — confirmed red: UI test fails on missing "Max requests/sec" label (5.9s), serial halts; API round-trip test already green (backend landed). Baseline run (block excluded) shows identical pre-existing failures (1, 4, 7, 8) → unrelated to this change.

### 2. Frontend implementation (green phase)
- [x] `frontend/src/lib/types.ts`: add `max_requests_per_second: number;` to `Provider` interface after `concurrency` (with `0 = unlimited` comment)
- [x] `frontend/src/lib/components/ConfigModal.svelte`:
  - `ProviderDraft` interface: add `max_requests_per_second: number;`
  - `buildProviders`: map `max_requests_per_second: p.max_requests_per_second || 0`
  - Both new-provider default drafts (`addProvider`, `addAgent`): set `max_requests_per_second: 0`
  - Save mapping: `max_requests_per_second: Math.max(0, Math.floor(p.max_requests_per_second || 0))`
- [x] `frontend/src/lib/components/ConfigModal/ProviderCard.svelte`:
  - Local `ProviderDraft`: add `max_requests_per_second: number;`
  - LLM branch `.provider-options`: second `.option-row` — "Max requests/sec" label + help tooltip + `<input id={max-rps-${index}} type="number" min="0" step="1" bind:value={provider.max_requests_per_second} class="option-input" onchange={() => onDirty?.()}>`
  - CSS: spacing between option rows (`.provider-options .option-row + .option-row { margin-top: 8px; }`)
- [x] Fix `frontend/src/lib/utils/__tests__/providers.unit.ts` fixtures for the new required field (svelte-check: 0 errors)

### 3. Verification
- [x] `cd frontend && npm run check` — zero TypeScript errors
- [x] `cd frontend && npm test` — vitest suite green (74 files, 893 tests)
- [x] `cd frontend && npx playwright test settings-provider` — new block fully green (3/3: round-trip, UI persist, default-0); pre-existing failures (Model switcher, Settings Model Delete, Reasoning persist, Primary Providers display) proven identical with/without this change — see Discovered Tasks
- [x] Mark all TODO items done and request review

## Discovered Tasks (not started — need approval)
- [ ] (discovered) Pre-existing settings-provider e2e failures in this environment, unrelated to this feature:
  - `Settings Model Delete`, `Reasoning effort › persist`, `Primary Providers` (×3): test fixtures seed name-based `worker_provider`/`utility_provider` refs (e.g. 'primary-a'), but the current UI keys LLM providers by stable integer ID (commit 9ed9a70c, which postdates the spec's last update). Consequence: primary-provider selects show "Select provider" and Save is blocked by "Please select a Worker provider and model." Fix = reseed with ID-based refs (two-step PUT, same pattern as the new block).
  - `Model switcher in task view`: LLM/executor-dependent; times out waiting for the task to go idle in this sandbox (gVisor/podman sandboxing is broken here: "failed to list stale containers: exit status 125").
  - Environment notes: Playwright browsers were missing (installed chromium); `frontend/dist` was not built (built); harness quirk: `globalTeardown` reuses global-setup's default export, re-running full setup after every run.

## Notes
- Backend field already exists (commit `5d028851`): plain int, always serialized, `UpdateConfig` clamps negatives to 0. Do NOT stub/modify the backend.
- `settings-provider.spec.ts` runs in the Playwright **isolated** project (port 3001, serialized, `workers: 1`) per `playwright.config.ts` — not the main chromium project. Page-based API helpers work identically there.
- LLM provider references in the UI are stable integer IDs (`String(p.id)`). For the UI test's save path to pass modal validation, seed the config in two steps: PUT provider (server assigns ID) → GET to read the ID → PUT again with `worker_provider`/`utility_provider` set to `String(id)`.
- The new "+ Add Provider" draft has `expanded: true` by default and `concurrency: 1`, `max_requests_per_second: 0`.
- Cancel path after dirtying the form triggers the "Discard changes?" confirm dialog — click "Discard".
- Clean stale state before e2e runs: `lsof -ti:3000,3001,9999 | xargs kill 2>/dev/null; rm -f /tmp/kanban-test-setup.lock; rm -rf /tmp/kanban-test-data /tmp/kanban-test-data-isolated /tmp/kanban-test-worktrees`
