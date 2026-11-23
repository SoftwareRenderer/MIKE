# Locking

The primary rules are in `AGENTS.md → Concurrency`. This document describes the implementation details, table shapes, and the reasoning behind timeouts.

## Keyed Locks (`pkg/keylock`)

The system uses keyed locks to manage concurrency across shared resources.

- **Domain Isolation**: Each lock domain must have its own named type (e.g., `type stashLocks struct{ keylock.Table }`). This prevents accidental "fusion" of domains that share the same ID alphabet (e.g., task IDs and project IDs).
- **Implementation**: Tables are embedded by value.
- **Wait Strategies**: 
  - `Lock(key)`: Waits indefinitely.
  - `TryLock(key, timeout)`: Gives up after the timeout.
  - `LockWatchedCtx(ctx, key, every, notice)`: Waits indefinitely and reports the wait; the wait ends with the caller's context. See *Watched Waits*.
- **Cleanup**: If a `TryLock` times out but the goroutine eventually acquires the lock, that goroutine **must** release it immediately. Failure to do so "steals" the lock and may block other operations until restart.

## Accessor Pattern

To prevent the misuse of mutexes, accessors must return a release function (`func()`) rather than the mutex itself (`*sync.Mutex`).

Returning a mutex would allow callers to store or copy it, potentially leading to situations where a caller serializes on the wrong lock while believing it holds the correct one. `TestNoExportedMutexAccessor` ensures no exported methods return a mutex.

## Lock Order and Wait Rules

- **The git-family order**: merge lock, then the task's worktree lock, then the project stash lock — never
  after. `mergeInProject` is the pattern: it takes the merge lock, `mergeInWorktree` takes the task's worktree
  lock inside it, and `WithStash` takes the stash lock inside that. The middle pair is the one that bites.
  `syncTargetIntoTask` holds the worktree lock across its `WithStash`. A checkpoint that asks for the stash lock
  first cycles with it, whenever the two land on two tasks of one project. Both waits are indefinite `Lock`
  calls, so nothing recovers short of a restart. `TestCheckpointTakesTheWorktreeLockBeforeWaitingForTheStashLock`
  holds the edge.
- **One table, the executor's**: `exec.TaskExecutor.worktreeMu` guards a task's worktree, and the server asks for
  it through `Server.lockWorktree` or `Server.lockWorktreeWatched`. The server kept its own table over the same
  task IDs, so one name meant two tables. The two tables never met. A conversation rewind and a tool call then
  rewrote one worktree together.
- **A stop goes outside the worktree lock**: `StopTask` waits for the run, and the run's tool call holds that
  lock. A turn that starts in the gap is stopped and asked for once more. Only a run still going on the second
  look is refused, and a refusal modifies nothing. `TestThreadSwitchStopsTheRunOutsideTheWorktreeLock`,
  `TestThreadSwitchRetriesOnceBeforeRefusing`.
- **A diff reading is taken while the lock is free**: it asks `WorktreeHeld` rather than waiting, since a wait
  would queue a panel behind a tool call of unknown length. A reading taken while the lock is held is dropped and
  the throttle re-armed. `TestWorktreeWatcherDefersWhileTheWorktreeIsBeingRewritten`,
  `TestHeldFollowsTheHolderAndGrowsNothing`.
- **Deadlock Prevention**: Never hold a mutex across a `WaitGroup.Wait` for a goroutine that also requires that mutex. (e.g., `WorktreeWatcher.Close` must release its mutex before waiting for the pool).
- **Git-Family Timeouts**: Avoid adding seconds-scale bounds to git-family locks (e.g., `mergeMu`, `stashMu`). These are often held across tool calls that can last up to 300s. Instead, use slow-wait reports (e.g., `gitSlowWait` at 2s) to identify wedged sections.
- **The submodule fetch is its own domain**: `submoduleFetchLocks` (`backend/git/submodules.go`) keys a
  checkout's path, not its project row, because three callers write one checkout's `.git/config` and its
  `modules` tree — import, the person's click, and a task's fetch. It is not the project's git lock: a fetch
  runs minutes and the git-family locks are held across tool calls, so merging them would queue a merge behind
  a download. Its wait is bounded (`submoduleFetchWait`) rather than indefinite, because the fetch it waits for
  is itself bounded — a holder still there past that is wedged, and "another fetch is running" is then the true
  answer. Tests: `backend/git/submodules_test.go`.
- **Execution Ownership**: A prepared execution (`prepareTaskExecution`) is owned by the caller until its loop is launched or it is released via `releasePrepared`. Failing to release a prepared execution leaves the task in a "running" state and delays subsequent stop operations.

## Watched Waits (User Notifications)

`keylock.Table.LockWatchedCtx` provides a way to notify the user when they are waiting for a lock.

- **Behavior**: It sends a notice periodically while waiting and one final notice when the lock is acquired. It remains silent if the lock is acquired immediately.
- **The ending is part of the notice**: a `keylock.WaitNotice` takes a `WaitOutcome` — `WaitWaiting` on each tick, then `WaitAcquired` once the lock is held or `WaitAbandoned` when the caller was called off before it ever held it. A notice cannot guess which ending it is reporting and a screen cannot either, so the caller says. Both endings take the wait line down; the wire shape is `docs/api-contract.md → A lock wait says how it ended`.
- **A wait a person is watching is abandonable**: `LockWatchedCtx(ctx, key, every, notice)` ends the wait with the caller's context, reports `WaitAbandoned` and answers `false` with a nil release. The reference this waiter took goes with it, or the key outlives everyone who can release it. It is for a wait someone waits on from a request: Commit & Done, a thread switch, a fork, a conversation rewind. A browser that went away then stops having its wait announced to every other viewer of that task while a tool call holds the lock. A path no request owns — the executor's own rewind — asks for none of that: it passes `Server.Detached()`, which no browser can cancel and shutdown can. See `docs/lifecycle.md`.
- **Only the wait is abandonable**: a holder is never interrupted, and a waiter that never took the token modified nothing, which is why giving up is safe. The context bounds nothing on the clock — a legitimate git-family wait is minutes long (*Git-Family Timeouts* above), so a caller that wants a bound asks for one with its own deadline.
- **UI Integration**: Notifications are shaped via `stream.LockWaitNotifier` and rendered in the `QueueIndicator` (e.g., "Awaiting LLM provider...").
- **Usage**: Watched locks are used for user-facing actions (e.g., Commit & Done, thread switches) but not for background processes (e.g., GC or turn-based merges).
- **One form, one loop**: there is one watched entry point, so the reported wait and the reported ending cannot drift apart. Tests: `pkg/keylock/lockwatch_test.go` (each ending, and the reference count), `backend/stream/lock_wait_test.go` (the frame), and `backend/exec/lock_watch_test.go` plus `backend/git/lock_watch_test.go` for the accessors that keep one table.

## Sandbox Lock (`sandboxMu`)

`executionState.sandboxMu` is a leaf lock.

- **Hierarchy**: It can be taken while holding `taskMu`, but `taskMu` must **never** be taken while holding `sandboxMu`.
- **Consistency**: To avoid inconsistent states, always read sandbox state as a snapshot via `sandbox()` or update it via `setSandbox`. Never read fields piecemeal.

## Checking this

`make check-race` runs `-race` over the packages that take these locks; `go test ./pkg/keylock/` covers the keyed table itself.

