# Locking

The primary rules are in `AGENTS.md → Concurrency`. This document describes the implementation details, table shapes, and the reasoning behind timeouts.

## Keyed Locks (`pkg/keylock`)

The system uses keyed locks to manage concurrency across shared resources.

- **Domain Isolation**: Each lock domain must have its own named type (e.g., `type stashLocks struct{ keylock.Table }`). This prevents accidental "fusion" of domains that share the same ID alphabet (e.g., task IDs and project IDs).
- **Implementation**: Tables are embedded by value.
- **Wait Strategies**: 
  - `Lock(key)`: Waits indefinitely.
  - `TryLock(key, timeout)`: Gives up after the timeout.
- **Cleanup**: If a `TryLock` times out but the goroutine eventually acquires the lock, that goroutine **must** release it immediately. Failure to do so "steals" the lock and may block other operations until restart.

## Accessor Pattern

To prevent the misuse of mutexes, accessors must return a release function (`func()`) rather than the mutex itself (`*sync.Mutex`).

Returning a mutex would allow callers to store or copy it, potentially leading to situations where a caller serializes on the wrong lock while believing it holds the correct one. `TestNoExportedMutexAccessor` ensures no exported methods return a mutex.

## Lock Order and Wait Rules

- **Merge vs. Stash**: Always acquire merge locks before stash locks.
- **Deadlock Prevention**: Never hold a mutex across a `WaitGroup.Wait` for a goroutine that also requires that mutex. (e.g., `WorktreeWatcher.Close` must release its mutex before waiting for the pool).
- **Git-Family Timeouts**: Avoid adding seconds-scale bounds to git-family locks (e.g., `mergeMu`, `stashMu`). These are often held across tool calls that can last up to 300s. Instead, use slow-wait reports (e.g., `gitSlowWait` at 2s) to identify wedged sections.
- **Execution Ownership**: A prepared execution (`prepareTaskExecution`) is owned by the caller until its loop is launched or it is released via `releasePrepared`. Failing to release a prepared execution leaves the task in a "running" state and delays subsequent stop operations.

## Watched Waits (User Notifications)

`keylock.Table.LockWatched` provides a way to notify the user when they are waiting for a lock.

- **Behavior**: It sends a notice periodically while waiting and one final notice when the lock is acquired. It remains silent if the lock is acquired immediately.
- **UI Integration**: Notifications are shaped via `stream.LockWaitNotifier` and rendered in the `QueueIndicator` (e.g., "Awaiting LLM provider...").
- **Usage**: Watched locks are used for user-facing actions (e.g., Commit & Done, thread switches) but not for background processes (e.g., GC or turn-based merges).

## Sandbox Lock (`sandboxMu`)

`executionState.sandboxMu` is a leaf lock.

- **Hierarchy**: It can be taken while holding `taskMu`, but `taskMu` must **never** be taken while holding `sandboxMu`.
- **Consistency**: To avoid inconsistent states, always read sandbox state as a snapshot via `sandbox()` or update it via `setSandbox`. Never read fields piecemeal.

## Checking this

`make check-race` runs `-race` over the packages that take these locks; `go test ./pkg/keylock/` covers the keyed table itself.

