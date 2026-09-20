# Locking

The rules are in `AGENTS.md → Concurrency`. This is the shape of the tables and the reasons behind the
timeouts.

## Keyed locks come from `pkg/keylock`

- One named domain type per lock domain: `type stashLocks struct{ keylock.Table }` in the package that
  owns it — `stashLocks`/`mergeLocks`/`worktreeLocks` in `git`, `taskWorktreeLocks` in `exec`,
  `gitTaskLocks`/`gitProjectLocks` in `server`. The table is embedded **by value**, so a zero-value owner
  still works and `go vet` copylocks refuses copying an owner struct.
- Two forms: `Lock(key)` waits indefinitely, `TryLock(key, timeout)` gives up.
- **Distinct types and distinct tables are both required**: a task ID and a project ID are drawn from the
  same alphabet, so a shared table would fuse two domains in silence.
  `TestLockProjectStashAndMergeAreDistinctTables` holds behaviour, table identity and type identity.
- No hand-rolled key→mutex tables in `backend/`: archgate's `locks` rule keeps it that way. Its baseline
  is the bridge's `fileLocks`, the one deliberate exception: a different process, and migrating it
  re-signs a committed binary for nothing.
- A timed-out `TryLock` acquires nothing later: a goroutine that wins the lock after its caller gave up
  has to release it, or the lock is stolen for an instant. (`CreateWorktree`'s `tryLockProjectWorktree`
  does exactly this; a goroutine that acquires after its caller gave up must release, or the project
  stays uncreatable until restart.)
- Tests: `pkg/keylock/keylock_test.go`, `backend/git/locks_test.go`.

## Accessors return a release function, never a mutex

`WorktreeManager` exposes `LockProjectStash(id) func()` / `LockProjectMerge(id) func()`, `TaskExecutor`
exposes `LockWorktree(id) func()`, and `api.GitHandler` is injected with `func(string) func()`. A
returned `*sync.Mutex` can be stored, copied and passed on, and a caller holding a copy serializes on a
*different* lock while believing it holds the one that protects the repository.
`TestNoExportedMutexAccessor` fails on any exported method returning `*sync.Mutex`.

## Lock order and waits

- **Merge before stash, never after** — which is why a release is written out explicitly (not deferred)
  wherever the enclosing function later calls `MergeBranch`.
- **No mutex is ever held across a `WaitGroup.Wait` for a goroutine that takes it.**
  `WorktreeWatcher.Close` stops the timers and closes `doneCh` under `ww.mu`, releases it, and only then
  waits for the pool, because `computeAndBroadcast` locks the same mutex in its deferred in-flight
  cleanup; a compute in flight a second after a file write is the ordinary case, so holding the mutex
  across the wait deadlocks `Close` — and with it the shutdown step that closes the watchers. Pinned by
  `TestCloseDoesNotWaitHoldingTheLock`.
- **Do not add a seconds-scale bound to a git-family lock.** `mergeMu` and `stashMu` are both reachable
  while a goroutine waits on the task's tool lock (`mergeOrchestrator`'s restore path and
  `exec/context.go`'s request lock each take it), which is held across an entire tool call (`bash` ≤ 300 s), so the wait is
  usually legitimate. Wedged sections are surfaced by the per-domain slow-wait report instead:
  `gitSlowWait` 2 s, `requestLockSlowWait` 5 s, `taskLockSlowWait` 60 s. The one bounded git wait is
  `worktreeLockTimeout` (30 s) on worktree creation, which waits on nothing else.

## A wait a person is watching announces itself

`keylock.Table.LockWatched` repeats a notice every `every` while the caller waits and once more when the
lock is taken, and says nothing at all when the lock was free — a line that flashes for a 2 ms
acquisition is a line that gets ignored. The owning packages expose watched twins
(`LockProjectStashWatched`, `LockWorktreeWatched`) that take the notice as a parameter, because the table
must still not leave the package. The caller supplies `stream.LockWaitNotifier(projectID, taskID,
domain)`, which is where a lock's frame is shaped — the one other producer of that frame is a wait that is
not a lock and carries one extra field, so it shapes its own (`docs/reconnection.md`) — and
`QueueIndicator` renders it as the top rung of
the line that already says "Awaiting LLM provider…". It is wired where a click is the waiter (Commit &
Done behind a tool call, a thread switch behind a stash) and deliberately not where it is not (merges from
turns, GC) — the split is `Lock` versus `TryLock` in the same package, expressed in UI terms.

Tests: `pkg/keylock/lockwatch_test.go`, `backend/exec/lock_watch_test.go`, `backend/git/lock_watch_test.go`,
`backend/stream/lock_wait_test.go`, `frontend/src/lib/components/__tests__/queue-indicator.unit.svelte.ts`.

## `executionState.sandboxMu` is a leaf lock

It may be taken under `taskMu`; `taskMu` must never be taken under it. Fields guarded by it are read as a
**snapshot** (`sandbox()`) and written through `setSandbox`, never captured piecemeal: `executeTask`'s
wait reads some of them after a `StopTask` can have run and would assemble a mix of old and new.
`sandboxRecreate.underLock` takes `*sandboxState` for the same reason — it runs while `sandboxMu` is held.
Test: `backend/exec/tools_test.go`.
