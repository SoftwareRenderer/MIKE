# TODO

## Goal
Make per-provider `concurrency` limit the number of **running tasks** per LLM provider (a task holds a slot while actively working, i.e. `in_progress`; queued tasks show the existing "Pending" badge + chat queue indicator), while the existing per-request in-flight LLM limit (incl. RPS) keeps applying to every LLM call — task LLM calls **and** utility calls, which queue if the provider is saturated.

## Design (decisions confirmed with user)
- **Two gates, one setting.** A new `taskGate *ConcurrencyGate` (separate instance → separate wait-estimate stats) limits running tasks per provider URL to `concurrency`. The existing `gate` (per-LLM-request, with RPS) stays and now guards: task LLM calls (loop) + all utility calls (`RunWithGate`, unchanged).
- **Slot lifecycle (LLM providers only — agents excluded per user):**
  - Acquired at the start of `executeTask` (before sandbox setup, so queued tasks don't hold a container).
  - **Released when the task goes idle** (turn complete → `review`/`idle`; summarize-no-input idle).
  - **Re-acquired when a user message resumes the task** (may queue again → "Pending").
  - **Swapped on mid-run provider switch** (release old provider, acquire new — may queue).
  - Released on every executor exit via the `executeTask` top-level defer (stop, fail, moved out of In Progress, panic) — guarded by a `taskSlotURL` field on `taskExecSetup` (`""` = not held) to avoid double-release after idle-release.
- **Display (existing UI, no new components):** while waiting for a task slot the task is `status: in_progress` + `execution_state: idle` → orange **"Pending"** badge in the In Progress column (same pattern as started-but-pending subtask parents); the 1s queue poller sends `queue` WS events → existing chat `QueueIndicator` ("position N, ~Xs"). On grant → `execution_state: running` → "Running" badge.
- **Task-slot acquire uses `maxRPS = 0`** — RPS applies to actual LLM request starts (per-request gate), not task starts.
- **Incidental fix (same bug class):** the per-call gate in the loop and summarization calls currently key on `setup.serverURL`, which is never updated on mid-run model/provider switches (only `lc.mc` is). Switch them to the current provider from `lc.mc.get()` so the in-flight limit tracks the provider the request actually goes to.
- **No frontend logic changes** — only the ProviderCard "Concurrency" tooltip text (semantics changed).

## Tasks

### 1. Task-gate plumbing (backend)
- [x] Add `taskGate *ConcurrencyGate` field to `TaskExecutor` + init in `NewTaskExecutor` (`backend/exec/executor.go`).
- [x] Add `taskSlotURL string` to `taskExecSetup` (`backend/exec/executor.go:923`).
- [x] New `backend/exec/task_gate.go` with helpers:
  - `acquireTaskSlot(setup, providerURL) bool` — sets `in_progress`/`idle` (Pending) + broadcast while waiting, runs the 1s queue poller (`taskGate.Position`/`EstimateWait`, `queue` WS event), blocks on `taskGate.Acquire(ctx, url, concurrency, 0)`, sets `taskSlotURL` + `running` on grant; returns `false` on ctx cancel.
  - `releaseTaskSlot(setup)` — releases + clears `taskSlotURL` if held.
  - `syncTaskSlot(setup, wantURL) bool` — no-op if same URL; else release old + `acquireTaskSlot(wantURL)`.
- [x] Wire into `executeTask` (`backend/exec/loop.go:283`): for non-agent providers acquire before sandbox setup (on cancel → `handleStopped` + return); add `releaseTaskSlot(setup)` to the top-level defer.

### 2. Slot lifecycle in the loop (backend)
- [x] Release slot at the two idle transitions: text-response-complete path (`loop.go` ~1457, `Status="review"`/`ExecutionState="idle"`) and summarize-no-input path (~1587).
- [x] Re-acquire (`syncTaskSlot` on current `lc.mc` URL) at resume paths: idleWait user-message case (~1610), post-response queued-message case (~1503) (incl. its ContextChanged variant), and the summarize-with-queued-input break; on cancel → `handleStopped` + return.
- [x] Swap slot on mid-run provider switches: top-of-loop DB-driven check (~456, only when URL changed) and user-message switches at ~636, mid-stream drain loop, ~938 (when `um.ServerURL != ""`); on cancel → `handleStopped` + return.

### 3. Per-call gate follows current provider (backend)
- [x] In the loop iteration: capture `gateURL` from `lc.mc.get()` before `Acquire`; use it for `Acquire`, all five `Release` sites in the iteration, and the queue poller (replaces stale `setup.serverURL`).
- [x] Summarization: pass current `lc.mc` URL instead of `setup.serverURL` in `maybeAutoSummarize` and the manual summarize path in `idleWait` (so fallback (non-utility) summary requests count against the right provider).

### 4. Backend unit tests
- [x] `gofmt` touched files; `go build -trimpath -o kanban .` passes.
- [x] New executor-level tests (using `testServer(t)` pattern from `executor_test.go`; local httptest "provider" endpoints as needed) — `backend/exec/task_gate_test.go`:
  - [x] concurrency=1: 2nd task queues — `in_progress` + `execution_state: idle` (Pending); grants slot (→ `running`) when 1st task goes idle or stops. (`TestTaskGate_SecondTaskQueues`)
  - [x] Per-provider isolation: task on provider A doesn't block task on provider B. (`TestTaskGate_PerProviderIsolation`)
  - [x] Agent providers bypass the task gate. (`TestTaskGate_AgentProviderBypasses`)
  - [x] `syncTaskSlot` swaps provider when a running task's provider changes (releases A, queues on B if B full). (`TestTaskGate_ProviderSwapFreesOldProvider`)
  - [x] Utility calls (`ChatCompletion`/`RunWithGate`) still respect per-request limit and queue while a request is in flight. (`TestUtilityCalls_RespectInFlightLimit`)
  - [x] Bonus: idle release frees the slot for a new task. (`TestTaskSlotReleasedWhenIdle`)
- [x] `go test ./backend/... ./pkg/... -count=1 -parallel 8` green — exception: `backend/server` `TestTaskOperatingModeRemount` fails in THIS environment because podman cannot pull the sandbox image (overlay-on-overlayfs storage driver error) — environmental, unrelated (task starts, slot acquires with no contention; failure is at `podman pull`).

### 5. E2E test
- [x] New `frontend/tests/e2e/provider-task-concurrency.spec.ts` — **isolated project** (config-modifying; add to `testMatch` in `frontend/playwright.config.ts`):
  - [x] `beforeEach`/`afterEach` (page fixture is per-test): PUT config → provider `concurrency: 1` / restore `16`.
  - [x] Task 1 created + started (prompt `… slow-start stop-test …` → 3s initial delay + 16×100ms slow fixture, ~5s turn) → assert `running` in In Progress.
  - [x] Tasks 2 and 3 created WHILE task 1's stream is in flight (so their title utility calls also queue on the per-request gate) + started → assert both in In Progress with `execution_state: idle` (Pending badge in DOM), zero assistant messages, task 2's chat shows the queue indicator ("position … in queue").
  - [x] After task 1's turn completes (task 1 → Review/idle) → assert task 2 becomes `running` (slot freed + re-granted), task 3 still Pending.
  - [x] Send a user message to idle task 1 (UI fill+Enter, verified via API to dodge the racy shared `sendMessage` count helper) → it transitions back to In Progress (re-acquires/queues per limit), turn completes → review.
  - [x] Utility calls unaffected: title generation for all 3 tasks succeeds (title ≠ prompt; no deadlock with queued tasks).
  - [x] `cd frontend && npx playwright test provider-task-concurrency` green (19.4s).

### 5b. Bugs found + fixed during e2e validation
- [x] **Stale queue position** (`backend/exec/task_gate.go`): `acquireTaskSlot` discarded the `queueEntry` from `Acquire`, so the poller called `Position(url, nil)` — reporting total waiters instead of the task's own position. Now captures `entry` (same pattern as the per-call gate in loop.go) and passes it to `Position`/`EstimateWait`.
- [x] **Title-gen stale full-row write clobbers task state** (pre-existing, now reliably hit because title calls queue behind in-flight streams): `startTitleGeneration` read the row at creation (status=backlog), then after the (slow) LLM call wrote the FULL row back via `UpdateTask` — reverting a started task to `backlog` (observed live: queued task 3 flipped to Backlog exactly when its title call completed). Fix: targeted writes `store.SetTaskTitle` / `store.ClearTitleGenerating` (single-column UPDATEs), used by `startTitleGeneration` + `finishTitleGeneration`. Regression test: `TestTitleGenerationDoesNotClobberTaskStatus` (red on old code, green on fix).
- [x] **Queue indicator never rendered for task-slot waits** (`frontend/src/lib/components/Chat/ChatEntries.svelte`): indicator was gated on `taskExecutionState === 'running'`, but a task-slot-waiting task is `in_progress` + `idle` (Pending). Added the pending disjunct: `(running || (in_progress && idle))`.

### 6. Frontend touch-up + docs
- [x] Update ProviderCard "Concurrency" tooltip ("Max tasks running concurrently on this provider; also caps in-flight LLM requests to this number.") — `frontend/src/lib/components/ConfigModal/ProviderCard.svelte`.
- [x] `cd frontend && npm run check` (TS-clean, 0 errors) + `npm run build`.

### 7. Executor-side title clobber (2nd round, found via live diagnostic + e2e)
- [x] The `SetTaskTitle` fix (5b) only half-closed the race: the EXECUTOR's full-row `UpdateTask(setup.task)` writes (20 loop sites + task_gate + prepare/StartTask/stop/fail/summarize/model-switch sites) still write the start-time row copy, reverting a title generated while the task was queued/running. Reproduced live with `frontend/tests/debug-task-concurrency.mjs` (logged server): title flipped generated → first-line exactly when the executor performed its post-slot-grant writes.
- [x] Fix: `storeTask(task, projectID)` helper (`backend/exec/loop_helpers.go`) — refreshes `Title` + `TitleGenerating` from the DB before the full-row write; `persistTask(setup)` = storeTask on the executor's copy. Converted ALL executor-side full-row writes: 20× loop.go, 2× task_gate.go, loop_helpers.go (handleStopped, applyModelSwitch, applySummarization, handleLoopDetected, setupSandbox), failTask, executor.go (prepareTaskExecution, StartTask, StopProjectTasks, RetryTask, handleLazyStart, subtask/parent flows), handler_http.go (send/summarize handlers), loop_agent.go (all sites), tools.go create_task. Kept raw: `tools.go` edit_task (intentional title write from a fresh row) and managers (fresh rows).
- [x] `applyModelSwitch` gained a `projectID` parameter (5 call sites updated).
- [x] Regression tests: `TestPersistTaskPreservesTitle` (unit, stale copy + concurrent SetTaskTitle) + `TestTaskGate_TitleSurvivesExecutorWrites` (through the real loop). Both green; live diagnostic re-run shows all 3 titles persist.
- [x] `go test ./backend/... ./pkg/... -count=1 -parallel 8` — everything green except the pre-existing/environmental set below.

### 8. Final validation (this sandbox)
- [x] **Go:** `exec`/`managers`/`store`/`stream`/all `pkg` green. Known non-regressions (all verified pre-existing at pristine HEAD via `git archive` copy or environmental): `exec TestExtractCommitContext_*` (6) — broken since upstream `dbb08d6a8`, fail identically at HEAD; `sandbox` (all) — podman overlay-on-overlayfs unsupported here; `server TestTaskOperatingModeRemount` — podman pull. `stream` flakes under full-suite load (passes standalone in both trees).
- [x] **E2E:** `provider-task-concurrency` PASS (19.4s). Regression batches (commit-and-done-unstaged, commit-dialog, chat-messages, settings-provider, card-title-truncation, chat-core, subtask-autostart-chain, edit-task-auto-merge-sync, parent-merged-subtasks-diff, auto-summarize-proceed): 55 passed; 4 failures (commit-dialog, card-title-truncation, settings-provider model-switch, edit-task-auto-merge-sync) ALL reproduced at pristine HEAD → pre-existing.
- [x] Frontend `npm run check` 0 errors + `npm run build` clean. `go build -trimpath -o kanban .` + `go vet ./backend/exec/` clean; touched files gofmt-clean.
- [x] Full-suite e2e (444 tests) cannot run to completion in this sandbox — the environment wipes /tmp + kills all processes ~2 min into the run; targeted batches used instead (see above).
- Note: `/home/sandbox/MIKE` is a git worktree ("jungle-zephyr-soar") whose `.git` lives on a read-only FS → `git add`/stash/worktree operations fail; user stages/commits manually.
- Diagnostic script kept per repo convention (tracked debug-*.mjs): `frontend/tests/debug-task-concurrency.mjs`.

### 9. Redundancy / dead-code review (post-completion pass)
- [x] **Dead return removed:** `ConcurrencyGate.Acquire`'s first `int` return was always 0 (vestige of a "position") — every caller used `_`. Signature is now `Acquire(ctx, url, conc, rps, onEnqueued) (*queueEntry, error)`; all call sites + `gate_test.go` updated.
- [x] **Inert `entry` capture fixed:** both poller call sites (task gate + loop per-call gate, the latter pre-existing) captured `entry` only AFTER `Acquire` returned — so during the actual wait the poller always passed `nil` and reported the TOTAL waiter count, not the caller's own position. `Acquire` now takes an `onEnqueued func(*queueEntry)` callback invoked at enqueue time (before blocking); the new `startGateQueuePoller` helper hands the entry to the poller over a channel (race-free).
- [x] **Duplicated poller extracted:** the ~20-line 1s queue-position ticker was copied verbatim between `loop.go` and `task_gate.go` — now `startGateQueuePoller` in `loop_helpers.go`, used by both.
- [x] **Gate-key normalization moved to the gate boundary:** `task_gate.go` trimmed trailing slashes but the per-call gate + `RunWithGate` passed raw URLs — a slash mismatch would split the gate key (double the effective limit). `gateKey()` now normalizes inside `Acquire`/`Release`/`Position`/`EstimateWait`; new test `TestAcquireRelease_TrailingSlashNormalization`.
- [x] **`EstimateWait` doc/impl drift fixed:** docs said "× queue position" but code used `ahead` (front waiter → 0s). Now both `Position` and `EstimateWait` go through `positionLocked` (shared helper, also removed their duplicated ahead/found logic) — estimate = position × avg slot, identical for nil-entry callers.
- [x] **Tests made to test what they claim:** `TestAcquireRelease_ConcurrencyLimit`, `TestAcquireRelease_CancelledEntrySkipped`, `TestEstimateWait_*` previously asserted on entries that were still nil at read time (stale-capture pattern — passed only via the nil path); they now obtain the real entry via the callback while blocked. `TestAcquireRelease_SlotAvailable` asserts the immediate-grant nil entry.
- [x] **Kept deliberately (reviewed, not dead):** `persistTask` (22-site convenience wrapper over `storeTask`); the loop's pre-LLM-call status block (executor's per-iteration persistence point, not just first iteration); `TrimRight` in `task_gate.go` (guards `taskSlotURL` string identity, distinct from gate-key normalization); `tools.go` edit_task raw `UpdateTask` (intentional title write from a fresh row); the debug `.mjs` (repo convention).
- [x] **Pre-existing `-race` failures (NOT from this change):** `TestExecuteTask_ManualSummarize_*` race between the executor reassigning `setup.task` (loop.go) and the test reading `setup.task.ID` — reproduces identically at pristine HEAD; `summarize_idle_test.go` untouched by this diff.
- [x] Re-validated: full Go suite (only the documented pre-existing/environmental set fails), `provider-task-concurrency` e2e PASS (19.3s), regression batch (commit-and-done-unstaged, chat-core, subtask-autostart-chain, auto-summarize-proceed) 31/31 PASS.

## Environment notes (this sandbox)
- `backend/server` `TestTaskOperatingModeRemount` FAILS here: podman cannot pull the sandbox image (overlay-on-overlayfs storage driver). Environmental — verified the task starts and slot acquires before the pull attempt; unrelated to the change.
- Several git-dependent unit tests need a git identity (none is set globally in this sandbox): run with `GIT_AUTHOR_NAME/EMAIL` + `GIT_COMMITTER_NAME/EMAIL` env (or `git config --global`). `TestHandlePull_Success` verified green with identity env.
- `.git` (real dir at /home/user/projects/kanban/MIKE/.git) is read-only → `git stash` ref updates / worktrees / dropping stashes fail. Avoid stash-based workflows; a duplicate `baseline-check` stash entry may remain (working tree verified identical).

## Notes
- **No new frontend components** — reuses "Pending" badge (`Card.svelte`: `in_progress` + `idle`) and `QueueIndicator` (driven by existing `queue` WS events).
- **No deadlock**: task slots are only acquired while holding no request slot; request slots only acquired while (optionally) holding a task slot — no cycle.
- **Known pre-existing edge (unchanged):** restarting an executor while the old one is blocked in a gate (rare double-start race) — same pattern exists today for the per-call gate; not addressed.
- **Queued-task card button:** a Pending (queued) task shows the play action (same as existing started-but-pending subtask parents); clicking it restarts and re-queues at the tail. Consistent with current behavior; noted, not changed.
- Agents (Pi etc.) fully excluded per user decision — their executor lifecycle is unchanged.
- RPS (`max_requests_per_second`) semantics unchanged: enforced at LLM request start by the per-request gate.
