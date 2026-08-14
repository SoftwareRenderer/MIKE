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
