# Storage: transactions, schema, files

The rules are in `AGENTS.md → Storage`. This is the list of what is inside `tx`, what is deliberately
outside it, how the schema is created, and how an older database is completed by it.

SQLite, one connection (`SetMaxOpenConns(1)`) so the `PRAGMA`s apply everywhere; `store.Store.mu` is the
serialisation point. Tests: `backend/store/` (`store_test.go`, `schema_test.go`, `schema_upgrade_test.go`,
`sessions_test.go`).

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

## The schema is created whole, and an older file is completed

- `baseSchemaSQL` (`backend/store/migrations.go`) declares every table, column and index the task, project,
  message, thread and stats reads use, and `applySchema` runs it on every start. The three credential
  tables are declared beside their own accessors (`sessionTable`, `apiTokenTable`, `passkeyTable`) under
  the same rule. Every statement is `IF NOT EXISTS`.
- A new column goes into `baseSchemaSQL`, in the order its scanner reads it. That declaration is the only
  place a column exists: the upgrade pass reads the same text back rather than keeping a second list.
- `applySchema` (`backend/store/schema_upgrade.go`) runs the statements in two passes with the `ALTER`s
  between them — tables, then the columns the file lacks, then the indexes. The order is the mechanism: an
  index naming a column this release adds fails on the very file being upgraded if it is created first.
- A column is the only thing the shape pass adds, and it compares by **name** — `checkColumns` never reads
  a declared type. What it refuses is what it cannot add: a declared column `ALTER` cannot fill for rows
  that already exist, and a name in `sqlite_master` holding anything but a table. Measured on this tree, a
  column renamed in the DDL is absorbed as an add, the old name left standing and unread. That is why a
  rename is a step and never an edit to the DDL alone.
- `backend/store/schema_test.go` compares each table's `PRAGMA table_info` against the column list its scan
  path selects, and walks each surviving read's query plan. An index dropped from the DDL fails a test
  rather than turning a lookup into a scan.
- `ALTER … ADD COLUMN` fills an existing row with the column's declared default, so a column that arrives
  after a release needs a default its scanner can read. `tasks.session_id`, `branch` and `output` declare
  `NOT NULL DEFAULT ''` for that reason, and `TestEveryColumnCanBeGivenToAnOlderDatabase` refuses a column
  that is none of addable, founding, or read as NULL.
- A column that arrived **nullable** is beyond that pass: the column is there, so no `ALTER` runs, and rows
  holding NULL fail every scan that reads the column as a value. `2026-10-26-tasks-null-to-blank` repairs
  exactly those three columns, and `TestARowThatArrivedWithNullsBootsAndReads` fails without it.

### The marker, and the step ladder beside the shape pass

- The marker is one table, `schema_meta` (`backend/store/schema_meta.go`). Its `schema_version` row is written
  when it is missing or holds another marker layer, and left alone otherwise, so a file this build has already
  stamped takes no write at boot. The row says the file has been through a build that owns the layer; no such
  row says the file predates it. A file carrying 1..12 in `PRAGMA user_version` is a pre-release build, because
  only those ran the chain that wrote it, and the boot says so in words that name the columns nothing reads any
  more. `PRAGMA user_version` is read for that one sentence and never written,
  and `TestFreshDatabaseCarriesTheSchemaMarker` pins that a fresh file still reads 0 there.
- Structure needs no step. `applySchema` adds what a file lacks from the DDL alone, so a step that only
  added a column would state one fact in two places.
- The ladder is `schemaSteps` (`backend/store/schema_steps.go`): keyed steps for the work a column
  declaration cannot express — a rename, a rewrite, a value whose meaning moved. Keys are dated and
  registered oldest first, because two task branches collide on the next integer far more easily than on a
  name. A step runs once, records `step:<key>`, and a failure stops the boot naming the step and leaves no
  row, so the next boot tries again.
- A step is `OneWay` only when it destroys what an older binary reads — a rename or a drop, not a rewrite
  that leaves the row more readable than it was. A file marked with a one-way step this build does not have
  refuses the boot naming it; a file ahead of this build by an additive step boots with one `Warn`.
  `TestAFileFromTheFutureRefusesAStepItCannotUndo` and
  `TestAFileFromTheFutureBootsWhenTheStepOnlyAdded` are those two.
- The floor is the schema `v0.1.1` shipped, kept verbatim at `backend/store/testdata/schema-v0.1.1.sql`.
  A file with no marker row is trusted to be at least that, because `v0.1.0` and `v0.1.1` declare the same
  shape and no released build ran the deleted chain. `TestBaselineStaysASubsetOfTheDDL` is what fails the
  day the DDL stops declaring something the baseline declared, which is the day that trust stops holding
  and the floor becomes a refusal.
- A foreign key is never added to a table that predates it, because SQLite has no `ADD CONSTRAINT`. An
  upgraded file therefore keeps no `ON DELETE CASCADE` on its old tables; deleting a project removes its
  tasks through `deleteTaskTrees` before the row goes, and that is the invariant the deletion path holds.
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

## Every upgrade is logged

- A boot that added a column to an older file logs which columns before it serves a request, under
  `component=store`, and says nothing else. Nothing in the app carries the news: an added column is a fact
  about the operator's file, and the board reads it from the same boot that wrote it.
- A step that ran logs its key, and the values it rewrote when it rewrote some. A boot that did nothing
  logs nothing, which is the assertion the quiet-reopen tests make.
- A database this binary created logs nothing, because every column the DDL declares is already there.

## Checking this

`go test ./backend/store/` covers the transaction boundary, the schema against its scanners, completing an
older file, the stored instants, the ID shapes and the free-page reclaim.
`-run 'Autovacuum|DefaultMode|ReclaimFreePages'` is the reclaim on its own;
`-run 'TestSchemaUpgradesAnOlderTableInPlace|TestEveryColumnCanBeGivenToAnOlderDatabase'`
is the upgrade path and the gate that keeps it open, and the first of those asserts the boot logged the
column names it. The refusal is
`-run 'TestSchemaRefusesAFileItCannotBringUp|TestSchemaRefusesWhatIsNotATable'`.
The marker, the ladder and what the boot says about a file older or newer than this build are
`-run 'SchemaMeta|SchemaMarker|SchemaStep|Baseline|Future|PreRelease|Nulls'`.
