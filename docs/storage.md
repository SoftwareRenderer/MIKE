# Storage: transactions, schema, files

The rules are in `AGENTS.md → Storage`. This is the list of what is inside `tx`, what is deliberately
outside it, and how the schema gate works.

SQLite, one connection (`SetMaxOpenConns(1)`) so the `PRAGMA`s apply everywhere; `store.Store.mu` is the
serialisation point. Tests: `backend/store/` (`store_test.go`, `migrations_test.go`, `sessions_test.go`).

## One transaction per section that must land entirely

`Store.tx` (`backend/store/store.go`) is the only way such a section runs — `SetActiveThread`,
`DeleteThreadWithMessages`, `CopyMessagesToThread`, `DeleteTaskMessagesAfter`, `DeleteProjectStats` —
with `execer` (same file, satisfied by both `*sql.DB` and `*sql.Tx`) so a helper's query text exists once.

Because the pool is one connection and `s.mu` is held across the section, a transaction buys **failure
atomicity, not isolation** — without it a `SetActiveThread` whose second statement failed leaves a task
with **no active thread at all**.

What stays outside `tx` states its reason at its definition:

- `TransitionInFlightTask` is one statement.
- `CreateTaskMessage` must not lose a turn's message because a token counter failed.
- `GetSessionByToken`'s second statement is a throttled `last_seen` touch (60 s, `lastSeenInterval`) —
  the store serialises on one connection, so an unthrottled write makes every authenticated request a DB
  write.
- The schema probe chain *is* the recovery mechanism for a half-applied migration.

Mid-section faults are induced with SQLite `RAISE(ABORT)` triggers — never a production seam for tests.
Test: `backend/store/atomic_writes_test.go`.

## Project deletion

`store.DeleteProject` is one `DELETE FROM projects`: every child table declares `ON DELETE CASCADE` and
`PRAGMA foreign_keys = ON`. A hand-written child delete could only fail on its own, leaving the project
row standing over the children it already deleted — and a project ID is the slug of its path, so
re-adding the path finds its rows gone.

CASCADE is the **row** story only. `api.handleDeleteProject` deletes the project's tasks through
`TaskHandler.deleteTaskTrees` first, because a task owns a sandbox container, a worktree, branches and
upload directories that keep running with no row left to name them, and `staleContainerNames` skips
running containers — nothing else would ever remove them.

## The schema is gated by `PRAGMA user_version`

- SQLite has no `ADD COLUMN IF NOT EXISTS`, so each added column is probed before its `ALTER`
  (`hasColumn`/`addColumn`/`renameColumn`, `backend/store/migrations.go`).
- `initSchema` returns early when the database is already at `schemaVersion`, and refuses to touch one
  stamped **newer** than this build.
- Adding a migration means extending the probe chain *and* raising `schemaVersion`, leaving the earlier
  steps in place — they must stay idempotent, and a step that must run exactly once needs its own marker
  (the config-row guard on the backfill) rather than trusting the number.
- Two task columns have exactly one writer each and are absent from `UpdateTask`: `memory_supervisor_paused`
  (`SetMemorySupervisorPaused`) and the pair `sandbox_memory_peak_mb` / `sandbox_oom_kills`
  (`RecordTaskSandboxMemory`), which the sandbox layer writes as a container is removed. A task copy read
  before that write is alive during most runs, so `UpdateTask` returns those columns and puts them back
  into the struct it hands to the broadcast — otherwise the write would erase a measurement, and a card
  would lose the alert the user has not seen yet.
- `memory_alert_acked` and its three numbers have one writer too, `DismissTaskMemoryAlert`, and the same
  read-back: a dismissal arriving while an executor holds a task copy must survive the copy's next
  `UpdateTask`. Neither writer touches `updated_at`, so neither a teardown nor a dismissal reorders the
  queues that read those columns. The flag is what says "someone dismissed it": `{peak 0, kills 2,
  limit 0}` is a real dismissal on a kernel without `memory.peak`, so the three numbers alone cannot
  carry it.

## File permissions

Data dir `0700`; `kanban.db` plus `-wal`/`-shm` `0600` (`backend/store/store.go`); `kanban.trace` `0600`
(`main.go`).

## IDs

Task and thread IDs (`backend/types/idgen.go`) draw dictionary words through `crypto/rand`
(`secureIndex`), never clock-seeded `math/rand` — they surface in URLs, branch names and worktree paths.
A failing system source panics rather than issuing a guessable ID. Shape: `word-word-word`
(`frost-amber-quartz`) for tasks, two words for threads (`GenerateThreadID()`), and the board URL is
`/?task=frost-amber-quartz`. Test: `backend/types/idgen_test.go`.
