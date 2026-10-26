# Storage: transactions, schema, files

The rules are in `AGENTS.md → Storage`. This is the list of what is inside `tx`, what is deliberately
outside it, and how the schema is created.

SQLite, one connection (`SetMaxOpenConns(1)`) so the `PRAGMA`s apply everywhere; `store.Store.mu` is the
serialisation point. Tests: `backend/store/` (`store_test.go`, `schema_test.go`, `sessions_test.go`).

## One transaction per section that must land entirely

`Store.tx` (`backend/store/store.go`) is the only way such a section runs — `SetActiveThread`,
`DeleteThreadWithMessages`, `CopyMessagesToThread`, `DeleteTaskMessagesAfter`, `DeleteProjectStats`,
`ResetPasskeys` — with `execer` (same file, satisfied by both `*sql.DB` and `*sql.Tx`) so a helper's query
text exists once. `ResetPasskeys` belongs on that list because a reset that left a session alive would not
have revoked the device it claims to.

Because the pool is one connection and `s.mu` is held across the section, a transaction buys **failure
atomicity, not isolation** — without it a `SetActiveThread` whose second statement failed leaves a task
with **no active thread at all**.

A `*Locked` helper reads and writes through the `execer` it is handed, never through `s.db`. The pool is one
connection, so a second handle inside the section waits for the transaction that is running it — and `s.mu`
waits behind it, which stops every query in the process, not just this one.
Test: `TestCopyMessagesToThread_CutRowNotInSourceThread`.

What stays outside `tx` states its reason at its definition:

- `TransitionInFlightTask` is one statement.
- `CreateTaskMessage` must not lose a turn's message because a token counter failed.
- `GetSessionByToken`'s second statement is a throttled `last_seen` touch (60 s, `lastSeenInterval`) —
  the store serialises on one connection, so an unthrottled write makes every authenticated request a DB
  write.
- `initSchema` is one `Exec` of the DDL and it returns the error: a database whose tables cannot be
  created refuses the boot instead of being repaired underneath a request.

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

## The schema is created whole

- `baseSchemaSQL` (`backend/store/migrations.go`) declares every table, column and index the task, project,
  message, thread and stats reads use, and `initSchema` runs it on every start. The three credential
  tables are declared beside their own accessors (`sessionTable`, `apiTokenTable`, `passkeyTable`) under
  the same rule. Every statement is `IF NOT EXISTS`.
- Nothing upgrades an older database, because nothing has shipped. `CREATE TABLE IF NOT EXISTS` leaves a
  table it finds exactly as it is, so a file whose tables predate this DDL boots and then fails every read
  that names a column the file lacks. That file is deleted, never upgraded — which is why no
  `PRAGMA user_version` stamp and no probe before an `ALTER` exists to maintain.
- A new column goes into `baseSchemaSQL`, in the order its scanner reads it. `backend/store/schema_test.go`
  compares each table's `PRAGMA table_info` against the column list its scan path selects. It also walks
  each surviving read's query plan, so an index dropped from the DDL fails a test rather than turning a
  lookup into a scan.
- Two task columns have exactly one writer each and are absent from `UpdateTask`: `memory_supervisor_paused`
  (`SetMemorySupervisorPaused`) and the pair `sandbox_memory_peak_mb` / `sandbox_oom_kills`
  (`RecordTaskSandboxMemory`), which the sandbox layer writes as a container is removed. A task copy read
  before that write is alive during most runs. So `UpdateTask` returns those columns and puts them back
  into the struct it hands to the broadcast — otherwise the write would erase a measurement, and a card
  would lose the alert the user has not seen yet.
- `memory_alert_acked` and its three numbers have one writer too, `DismissTaskMemoryAlert`, and the same
  read-back: a dismissal arriving while an executor holds a task copy must survive the copy's next
  `UpdateTask`. Neither writer touches `updated_at`, so neither a teardown nor a dismissal reorders the
  queues that read those columns. The flag is what says "someone dismissed it": `{peak 0, kills 2,
  limit 0}` is a real dismissal on a kernel without `memory.peak`, so the three numbers alone cannot
  carry it.
- `title_llm` is a property of the `title` string, so whichever write stores a title stores who wrote it:
  `SetTaskTitle`, and a person's title `PUT` with the mark cleared. A run's task copy puts both back from
  the stored row, so `UpdateTask` carries the column — a mark that outlived the title it described would
  name a branch after text nobody chose.

## An instant that SQL compares is fixed-width UTC

`ORDER BY`, `>=` and `<` on a stored timestamp compare **text**, so the text must be one fixed width and
one zone. `time.RFC3339Nano` is neither: it trims the zeros off the end of the fraction. And `Z` (0x5A)
sorts above every digit, so the earlier `…:07.3892121Z` came back **after** the later `…:07.38921216Z` —
which is how a chat could read its own turns the wrong way round. A zone offset is the other half of the
same fault: it repeats an hour of wall-clock time at a fall-back. `task_messages.created_at` is stored through `formatMessageTime`
(`messageTimeLayout`); `task_threads.checkpoint_message_time` through `ptrTime`, because
`GetThreadsByCheckpointMessageTime` matches that column against a message's `created_at` rendered by its
caller. `rowid` breaks an exact tie, as `GetLastMessageInThread` already did; the listing queries do the
same. Tests: `backend/store/messages_time_test.go`.

## A conversation is read in the order it was written

`idx_task_messages_thread_time(thread_id, created_at)` and `idx_task_messages_task_time(task_id, created_at)`
are what every `ORDER BY created_at, rowid` on `task_messages` reads through. A single-column index on
`thread_id` or `task_id` satisfies the `WHERE` and leaves SQLite to sort
the whole thread — every content column of it — in a temp B-tree, so the read costs the thread before it
returns a row; the windowed read (`GetTaskMessagePage`, `docs/api-contract.md → The conversation is a
window`) is bounded by its window only because these two carry the order. A composite index covers what a
single-column one covers, so the DDL declares the composite alone and no insert pays for a second copy.
`EXPLAIN QUERY PLAN` is asserted, not assumed: `backend/store/messages_window_test.go`.

`idx_task_messages_user_thread(thread_id, created_at) WHERE role = 'user'` and its `task_id` twin are that
order cut to prompts, and `GetUserTurnPage` reads through them
(`docs/api-contract.md → The conversation is a window, not the thread`). Over the shared index the same read passes
every row of the turn below the prompt it is looking for, so its cost is the turn's length. A partial index
keeps `rowid` as its last key column, which is what lets the role filter keep the tie rule; putting `role`
in the shared index instead pushes `rowid` back and puts a temp B-tree in every conversation read. Tests:
`backend/store/messages_turns_test.go`.

## Free pages go back to the operating system

A database in SQLite's default mode gives back only the pages a delete emptied at the very end of the
file; every interior free page stays on the freelist and the file keeps that size forever. The live file
measured 360 MB with 152 MB of it pages no row uses. `backend/store/vacuum.go` owns both halves of the
answer.

- `NewStore` puts the file in `PRAGMA auto_vacuum = INCREMENTAL`. The conversion is a `VACUUM`, because the
  pragma is ignored on a file whose header has already been written. That rewrite is the last unbounded
  one the file ever pays — 791 ms for this install's 360 MB file, timed at the boot that converted it —
  and a new file takes the mode in microseconds. A refusal is logged and boot continues: such a database
  answers correctly, it just keeps the size of everything ever deleted from it.
- `StartVacuumSweep` truncates free pages in bursts on the server's base context. No shutdown stage names
  it, because the detached-work stage cancels it and the database closes after that stage.
- `vacuumFloorPages` keeps a truncation off the serialisation point until 16 MB is there to give back.
  `vacuumBurstPages` caps one hold at 43 ms, measured on this host's disk with the log it applies included,
  so a deeper mess unwinds over later ticks instead of in one hold.
- **A burst is not over when it returns pages.** In WAL mode the main file lags the page count, and
  SQLite's own auto-checkpoint waits for ~1000 dirty pages — a threshold reclamation never has to cross.
  `reclaimFreePages` therefore checkpoints, and checkpoints again *before* measuring: a page freed inside
  an unapplied transaction is not in the file's freelist, and `PRAGMA freelist_count` reads the file. A
  database holding 500 free pages measured `freelist_count = 0` until its log was applied.
- Incremental rather than FULL autovacuum. FULL moves pages inside whichever write next finds a freelist,
  so a task deletion's cost lands on the next unrelated insert with no bound anyone chose. Here the cost
  sits in one statement on a timer this package declares.
- Fragmentation is out of reach and stays out. A burst truncates pages no row uses at all; it cannot
  compact a page that is half empty. Only the boot `VACUUM` does that, which is why it runs once rather
  than never.

## File permissions

Data dir `0700`; `kanban.db` plus `-wal`/`-shm` `0600` (`backend/store/store.go`); `kanban.trace` `0600`
(`main.go`). `data/tls` is `0700`, its `*.crt` `0644` and its `*.key` `0600` — written owner-only even when
replacing a file that was not, by `writePrivateFile` in `backend/tls/tls.go`.

## IDs

Task and thread IDs (`backend/types/idgen.go`) draw dictionary words through `crypto/rand`
(`secureIndex`), never clock-seeded `math/rand` — they surface in URLs, branch names and worktree paths.
A failing system source panics rather than issuing a guessable ID. Shape: `word-word-word`
(`frost-amber-quartz`) for tasks, two words for threads (`GenerateThreadID()`), and the board URL is
`/?task=frost-amber-quartz`. Test: `backend/types/idgen_test.go`.
## Checking this

`go test ./backend/store/` covers the transaction boundary, the schema against its scanners, the stored
instants, the ID shapes and the free-page reclaim. `-run 'Autovacuum|DefaultMode|ReclaimFreePages'` is the
reclaim on its own.
