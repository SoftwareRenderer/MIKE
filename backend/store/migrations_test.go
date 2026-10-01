package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/types"
	"mike/pkg/logging"
)

// Migrations are the only schema path for a database created by an older MIKE. Pinning the
// two things that matter: an old database arrives at the current shape with its rows, and a
// current database is left alone in silence.

// legacyDB writes a database whose one named table has a given older shape. The rest of the
// schema is left to NewStore: only that table pre-exists, as in an upgrade in place.
func legacyDB(t *testing.T, tableDDL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kanban.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(tableDDL); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	return path
}

// columnsOf reads a table's columns from the file, so assertions are about the schema.
func columnsOf(t *testing.T, dbPath, table string) map[string]bool {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("read columns of %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		cols[name] = true
	}
	return cols
}

func TestHasColumn(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "kanban.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	if !s.hasColumn("tasks", "title") {
		t.Error("hasColumn(tasks.title) = false, want true")
	}
	if s.hasColumn("tasks", "definitely_not_a_column") {
		t.Error("hasColumn(tasks.definitely_not_a_column) = true, want false")
	}
	// A missing table answers "no such column" — the ALTER the caller was about to
	// skip would have failed on the missing table anyway.
	if s.hasColumn("no_such_table", "title") {
		t.Error("hasColumn(no_such_table.title) = true, want false")
	}
}

// A tasks table from before the columns existed must gain them, keeping its rows.
func TestMigration_AddsColumnsMissingFromOldDatabase(t *testing.T) {
	path := legacyDB(t, `
		CREATE TABLE tasks (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			title TEXT,
			status TEXT,
			created_at TEXT
		);
		INSERT INTO tasks (id, project_id, title, status, created_at)
			VALUES ('t1', 'p1', 'Legacy task', 'backlog', '2024-01-01T00:00:00Z');`)

	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore on legacy db: %v", err)
	}
	s.Close()

	cols := columnsOf(t, path, "tasks")
	// execution_state is absent on purpose: it only ever arrived by renaming
	// status_state (see the rename test), and a table with neither name is older
	// than the migration itself.
	for _, want := range []string{
		"auto_merge", "subtasks_auto_merge", "operating_mode",
		"summarizing", "has_merge_conflict", "moved_at", "assigned_ports",
		"current_thread_id", "base_commit", "session_cost", "sandbox_memory_peak_mb",
		"memory_supervisor_paused",
	} {
		if !cols[want] {
			t.Errorf("migration did not add tasks.%s", want)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	var title string
	if err := db.QueryRow(`SELECT title FROM tasks WHERE id = 't1'`).Scan(&title); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if title != "Legacy task" {
		t.Errorf("title = %q, want %q — migration damaged existing data", title, "Legacy task")
	}
}

// The two columns that were renamed must be renamed, not re-added: the value the
// user set under the old name is the value that must survive.
func TestMigration_RenamesLegacyColumnsKeepingValues(t *testing.T) {
	path := legacyDB(t, `
		CREATE TABLE tasks (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			title TEXT,
			status TEXT,
			created_at TEXT,
			status_state TEXT DEFAULT 'idle',
			merge_on_commit INTEGER NOT NULL DEFAULT 1,
			merge_children INTEGER NOT NULL DEFAULT 0
		);
		INSERT INTO tasks (id, project_id, title, status, created_at, status_state, merge_on_commit, merge_children)
			VALUES ('t1', 'p1', 'Legacy task', 'review', '2024-01-01T00:00:00Z', 'running', 0, 1);`)

	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore on legacy db: %v", err)
	}
	s.Close()

	cols := columnsOf(t, path, "tasks")
	for _, want := range []string{"execution_state", "auto_merge", "subtasks_auto_merge"} {
		if !cols[want] {
			t.Errorf("tasks.%s missing after rename migration", want)
		}
	}
	for _, gone := range []string{"status_state", "merge_on_commit", "merge_children"} {
		if cols[gone] {
			t.Errorf("tasks.%s still present — the rename left both names behind", gone)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	var state string
	var autoMerge, subtasksMerge int
	if err := db.QueryRow(`SELECT execution_state, auto_merge, subtasks_auto_merge FROM tasks WHERE id = 't1'`).
		Scan(&state, &autoMerge, &subtasksMerge); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if state != "running" {
		t.Errorf("execution_state = %q, want %q (carried over from status_state)", state, "running")
	}
	if autoMerge != 0 || subtasksMerge != 1 {
		t.Errorf("auto_merge=%d subtasks_auto_merge=%d, want 0 and 1 — the old names held these values", autoMerge, subtasksMerge)
	}
}

// Guards the two columns that must not come back: adding merge_on_commit unconditionally hit
// databases that already had auto_merge (the rename right after it then failed), and plan_mode
// had no writer and no reader - plan mode lives in operating_mode.
func TestMigration_FreshDatabaseHasNoLegacyOrJunkColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.db")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s.Close()

	cols := columnsOf(t, path, "tasks")
	for _, unwanted := range []string{"status_state", "merge_on_commit", "merge_children", "plan_mode"} {
		if cols[unwanted] {
			t.Errorf("fresh database has tasks.%s — it is a retired name", unwanted)
		}
	}
}

// Silence must mean "nothing to do". Before the probes were added, a fresh startup
// logged nothing only because every error was discarded along with 34 ALTERs that
// could not succeed; now a schema failure has somewhere to be reported.
func TestMigration_SilentAndErrorFreeOnCurrentDatabase(t *testing.T) {
	logs, restore := logging.TestSink()
	defer restore()

	path := filepath.Join(t.TempDir(), "kanban.db")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// Second open = the upgrade-in-place path against a database that is current.
	s.Close()
	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopening current db: %v", err)
	}
	s2.Close()

	if out := logs.String(); strings.Contains(out, "schema") {
		t.Errorf("migrations reported a problem on a current database:\n%s", out)
	}
}

// Reopening must not disturb rows or column set — the migrations run every start.
func TestMigration_IdempotentAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.db")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := s.Exec(`INSERT INTO projects (id, name, path, created_at, updated_at) VALUES ('p1', 'proj', '/tmp/p1', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	before := len(columnsOf(t, path, "tasks"))
	s.Close()

	for i := 0; i < 3; i++ {
		s2, err := NewStore(path)
		if err != nil {
			t.Fatalf("restart %d: %v", i, err)
		}
		s2.Close()
	}

	if after := len(columnsOf(t, path, "tasks")); after != before {
		t.Errorf("tasks column count changed across restarts: %d -> %d", before, after)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	var name string
	if err := db.QueryRow(`SELECT name FROM projects WHERE id = 'p1'`).Scan(&name); err != nil {
		t.Fatalf("read row after restarts: %v", err)
	}
	if name != "proj" {
		t.Errorf("name = %q, want %q", name, "proj")
	}
}

// TestSchemaVersionStamped checks that a migrated database carries the stamp, so the next start
// skips the probe chain. The stamp is read from the file, not through the Store, because what is
// under test is what the Store wrote.
func TestSchemaVersionStamped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.db")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if v != schemaVersion {
		t.Errorf("user_version = %d after a fresh initSchema, want %d", v, schemaVersion)
	}

	// Reopening must not re-run the chain or change the stamp.
	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := s2.userVersion(); got != schemaVersion {
		t.Errorf("user_version after reopen = %d, want %d", got, schemaVersion)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("close reopened: %v", err)
	}
}

// TestNewerSchemaVersionIsLeftAlone covers the guard that makes the stamp worth having: a database
// written by a newer MIKE is not edited by this one. Dropping a column and stamping ahead of this
// build is how a downgrade looks; running the probe chain there would ALTER a schema we do not own.
func TestNewerSchemaVersionIsLeftAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.db")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE tasks DROP COLUMN session_cost`); err != nil {
		t.Fatalf("drop column to simulate a newer schema: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatalf("stamp newer version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	logs, restore2 := logging.TestSink()
	defer restore2()

	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("open a newer database: %v", err)
	}
	defer func() { _ = s2.Close() }()

	if cols := columnsOf(t, path, "tasks"); cols["session_cost"] {
		t.Error("a database stamped newer than this build was migrated anyway — the guard did not hold")
	}
	if !strings.Contains(logs.String(), "newer than this build") {
		t.Errorf("expected a log line naming the newer schema version, got:\n%s", logs.String())
	}
	if v := s2.userVersion(); v != 99 {
		t.Errorf("user_version = %d, want 99 — this build must not rewrite a newer stamp", v)
	}
}

func TestMigration_ClearsDoneTaskDiffSnapshotsWrittenBeforeParsedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.db")

	legacyBody := `{"diff":"patch text","untracked":[],"short_stat":"1 file changed"}`
	currentBody := `{"diff":"patch text","parsed_files":[{"path":"a.txt","rows":[],"added":1,"removed":0}],"untracked":[],"short_stat":"1 file changed"}`

	seed := func(withTask bool, diffJSON string) {
		s, err := NewStore(path)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		defer func() { _ = s.Close() }()
		if withTask {
			if err := s.CreateProject(&types.Project{ID: "mig-proj", Name: "mig-proj", Path: "/tmp/mig-proj"}); err != nil {
				t.Fatalf("create project: %v", err)
			}
			if err := s.CreateTask(&types.Task{ID: "mig-task", ProjectID: "mig-proj", Title: "t", Status: "done"}); err != nil {
				t.Fatalf("create task: %v", err)
			}
		}
		if err := s.UpsertTaskDiffSnapshot(&TaskDiffSnapshot{
			TaskID: "mig-task", ProjectID: "mig-proj", BaseCommit: "aaa", BranchTip: "bbb",
			StatsJSON: `{"files_n":1}`, DiffJSON: diffJSON,
		}); err != nil {
			t.Fatalf("seed snapshot: %v", err)
		}
	}

	stamp := func(v int) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer func() { _ = db.Close() }()
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v)); err != nil {
			t.Fatalf("stamp user_version: %v", err)
		}
	}

	snapshots := func() int {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer func() { _ = db.Close() }()
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM task_diff_snapshots`).Scan(&n); err != nil {
			t.Fatalf("count snapshots: %v", err)
		}
		return n
	}

	seed(true, legacyBody)
	stamp(doneDiffParsedStamp - 1)
	if got := snapshots(); got != 1 {
		t.Fatalf("fixture should hold one snapshot, got %d", got)
	}

	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("open the pre-parsed database: %v", err)
	}
	if v := s.userVersion(); v != schemaVersion {
		t.Errorf("user_version = %d, want %d", v, schemaVersion)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := snapshots(); got != 0 {
		t.Errorf("%d snapshot rows survived the upgrade — their bodies carry no parsed view and are served verbatim", got)
	}

	// A row written at the current stamp survives: the drop is the migration, not every start.
	seed(false, currentBody)
	if got := snapshots(); got != 1 {
		t.Fatalf("fixture should hold one snapshot after reseeding, got %d", got)
	}
	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen at the current stamp: %v", err)
	}
	_ = s2.Close()
	if got := snapshots(); got != 1 {
		t.Errorf("snapshot written at the current stamp was deleted: %d rows left", got)
	}
}

// The stored peaks written under the old definition counted the host's page cache and no later run
// can lower them, so they are cleared once. A peak written by this build must survive a later start.
func TestZeroLegacySandboxPeaks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peak.db")

	seed := func() {
		s, err := NewStore(path)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		defer func() { _ = s.Close() }()
		if err := s.CreateProject(&types.Project{ID: "peak-proj", Name: "peak-proj", Path: "/tmp/peak-proj"}); err != nil {
			t.Fatalf("create project: %v", err)
		}
		if err := s.CreateTask(&types.Task{ID: "peak-task", ProjectID: "peak-proj", Title: "t", Status: "done"}); err != nil {
			t.Fatalf("create task: %v", err)
		}
		if err := s.RecordTaskSandboxMemory("peak-task", 4990, 2); err != nil {
			t.Fatalf("seed peak: %v", err)
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer func() { _ = db.Close() }()
		if _, err := db.Exec(`UPDATE tasks SET memory_alert_acked = 1, memory_alert_ack_peak_mb = 4990,
			memory_alert_ack_oom_kills = 2, memory_alert_ack_limit_mb = 1024 WHERE id = 'peak-task'`); err != nil {
			t.Fatalf("seed dismissal: %v", err)
		}
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, doneLegacyPeakClearedStamp-1)); err != nil {
			t.Fatalf("stamp user_version: %v", err)
		}
		// The store opened above stamped its own marker; an old database does not have it.
		if _, err := db.Exec(`DELETE FROM config WHERE key = 'migration_sandbox_peak_definition'`); err != nil {
			t.Fatalf("clear migration marker: %v", err)
		}
	}

	read := func() (peak, acked int64) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer func() { _ = db.Close() }()
		if err := db.QueryRow(`SELECT sandbox_memory_peak_mb, memory_alert_acked FROM tasks WHERE id = 'peak-task'`).
			Scan(&peak, &acked); err != nil {
			t.Fatalf("read peak: %v", err)
		}
		return peak, acked
	}

	seed()
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if peak, acked := read(); peak != 0 || acked != 0 {
		t.Fatalf("after the migration: peak=%d acked=%d, want 0/0", peak, acked)
	}

	// A peak recorded by this build is a measurement, not legacy text: the next start must leave it.
	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := s2.RecordTaskSandboxMemory("peak-task", 300, 0); err != nil {
		t.Fatalf("record peak: %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s3, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := s3.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if peak, _ := read(); peak != 300 {
		t.Errorf("peak after a second start = %d, want 300 — the migration must run exactly once", peak)
	}
}

// A database older than the prompt indexes gains them on the next start: the read that names the user
// message above a position has nothing else to read through, and a missing index there is a walk over
// every row of the turn below it.
func TestMigration_AddsTheUserTurnIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.db")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("create the database: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, name := range []string{"idx_task_messages_user_thread", "idx_task_messages_user_task"} {
		if _, err := db.Exec(`DROP INDEX IF EXISTS ` + name); err != nil {
			t.Fatalf("drop %s to simulate an older database: %v", name, err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, doneMessageOrderIndexesStamp)); err != nil {
		t.Fatalf("stamp the older version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	s, err = NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s.Close() }()

	for _, name := range []string{"idx_task_messages_user_thread", "idx_task_messages_user_task"} {
		if !hasIndex(t, s, name) {
			t.Errorf("index %s is missing after the upgrade", name)
			continue
		}
		// The predicate is the index: without it the same read walks the answers too.
		var ddl string
		if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&ddl); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(ddl, "WHERE role = 'user'") {
			t.Errorf("index %s is not partial on user rows: %s", name, ddl)
		}
	}
}
