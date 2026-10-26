package store

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
	"mike/pkg/logging"
)

// The schema is created whole, so a fresh database must carry every column and index the reads use.
// Each expected set here is the list a read is built from, never a second copy of the DDL.

// scannedColumns pairs a table with the column list its row scanner reads, in scan order.
var scannedColumns = []struct {
	table   string
	columns string
}{
	{"tasks", taskColumns},
	{"projects", projectColumns},
	{"task_messages", taskMessageCols},
	{"task_threads", threadColumns},
	{"task_diff_snapshots", taskDiffSnapshotColumns},
	{"sessions", sessionColumns},
}

// TestFreshSchemaHasEveryScannedColumn is the net that replaces the probe chain: a column a scanner
// reads must be declared by the DDL, and the DDL declares nothing no scanner reads. Column order is
// compared too, because every one of these reads is positional.
func TestFreshSchemaHasEveryScannedColumn(t *testing.T) {
	s := testStore(t)
	for _, want := range scannedColumns {
		have := tableColumns(t, s.db, want.table)
		expected := splitColumns(want.columns)
		if missing := notIn(expected, have); len(missing) > 0 {
			t.Errorf("%s: the schema declares no %v, which the scan reads", want.table, missing)
		}
		if extra := notIn(have, expected); len(extra) > 0 {
			t.Errorf("%s: the schema declares %v, which no scan reads", want.table, extra)
		}
		if strings.Join(expected, ",") != strings.Join(have, ",") {
			t.Errorf("%s: the DDL order %v is not the scan order %v", want.table, have, expected)
		}
	}
}

// The tables whose statements name their columns inline instead of through one list are caught by
// running those statements: a column the DDL stopped declaring fails the read or the counter, and
// every one of these paths answers without error only on a schema that still has them.
func TestFreshSchemaServesTheReadsWithoutAColumnList(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	if err := s.CreateProject(newProject("schema-proj", "schema-proj", "/tmp/schema-proj")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("schema-task", "schema-proj")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	m := newMessage("schema-msg", "schema-task", "assistant", "done")
	m.ModelName, m.ProviderName = "m-1", "1"
	m.PromptTokens, m.CacheTokens, m.PredictedTokens = 10, 2, 3
	m.PromptMs, m.PredictedMs, m.Duration = 100, 50, 150
	if err := s.CreateTaskMessage(m); err != nil {
		t.Fatalf("create message: %v", err)
	}
	s.AccumulateProjectStats("schema-proj", "m-1", "1", 0.5)

	sandboxed := newProject("schema-sandbox-proj", "schema-sandbox", "/tmp/schema-sandbox")
	sandboxed.SandboxEnabled = true
	if err := s.CreateProject(sandboxed); err != nil {
		t.Fatalf("create the sandboxed project: %v", err)
	}
	running := newTask("schema-running-task", sandboxed.ID)
	running.ExecutionState = types.ExecStateRunning
	if err := s.CreateTask(running); err != nil {
		t.Fatalf("create the running task: %v", err)
	}
	pool, err := s.GetRunningSandboxedTasks()
	if err != nil {
		t.Errorf("the supervisor's pool read failed: %v", err)
	}
	if len(pool) != 1 || pool[0].ID != running.ID {
		t.Errorf("the supervisor's pool = %v, want only %s", pool, running.ID)
	}

	if _, err := s.GetProjectStats("schema-proj"); err != nil {
		t.Errorf("the cumulative stats read failed: %v", err)
	}
	if err := s.DeleteProjectStats("schema-proj", "m-1", "1"); err != nil {
		t.Errorf("zeroing a model's stats failed: %v", err)
	}
	if _, err := s.GetConfigValues("worker_provider", "worker_model"); err != nil {
		t.Errorf("the config read failed: %v", err)
	}

	if err := s.CreateSession(ctx, storeSession("schema-session")); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, _, _, err := s.GetSessionByToken(ctx, "schema-token"); err != nil {
		t.Errorf("a session read by its token failed: %v", err)
	}
	if _, err := s.ListSessions(ctx); err != nil {
		t.Errorf("listing sessions failed: %v", err)
	}
	if _, err := s.DeleteExpiredSessions(ctx); err != nil {
		t.Errorf("the expired-session sweep failed: %v", err)
	}

	if err := s.CreateAPIToken(ctx, NewAPIToken{ID: "schema-token-row", Token: "schema-secret", Label: "ci"}); err != nil {
		t.Fatalf("create api token: %v", err)
	}
	if _, err := s.GetAPITokenByToken(ctx, "schema-secret"); err != nil {
		t.Errorf("an api token read by its secret failed: %v", err)
	}
	if _, err := s.ListAPITokens(ctx); err != nil {
		t.Errorf("listing api tokens failed: %v", err)
	}
}

// indexReads pairs one surviving read with the index it must walk. The statements are the shapes the
// accessors build, so an index dropped from `baseSchemaSQL` shows up as a full table scan — the cost
// these reads exist to avoid — rather than as a name nobody executes.
var indexReads = []struct {
	name  string
	sql   string
	args  []any
	index string
}{
	{"tasks of a project", `SELECT ` + taskColumns + ` FROM tasks WHERE project_id = ?`, []any{"schema-proj"}, "idx_tasks_project"},
	{"children of a task", `SELECT ` + taskColumns + ` FROM tasks WHERE parent_task_id = ?`, []any{"schema-task"}, "idx_tasks_parent"},
	{"board of a project", `SELECT ` + taskColumns + ` FROM tasks WHERE project_id = ? AND status = ?`, []any{"schema-proj", "backlog"}, "idx_tasks_project_status"},
	{"threads of a task", `SELECT ` + threadColumns + ` FROM task_threads WHERE task_id = ?`, []any{"schema-task"}, "idx_task_threads_task"},
	{"a thread's messages", threadMessagesSQL, []any{"schema-thread"}, "idx_task_messages_thread_time"},
	{"a task's messages", taskMessagesAllSQL, []any{"schema-task"}, "idx_task_messages_task_time"},
	{"the newest row of a thread", lastMessageInThreadSQL, []any{"schema-thread"}, "idx_task_messages_thread_time"},
	{"the prompts of a thread", userTurnCols + `thread_id = ?` + userTurnOnly + ` ` + newestFirstOrder + ` LIMIT ?`, []any{TurnLabelChars, "schema-thread", 5}, "idx_task_messages_user_thread"},
	{"the prompts of a task", userTurnCols + `task_id = ?` + userTurnOnly + ` ` + newestFirstOrder + ` LIMIT ?`, []any{TurnLabelChars, "schema-task", 5}, "idx_task_messages_user_task"},
	{"a session by its digest", `SELECT id FROM sessions WHERE token_hash = ?`, []any{"schema-digest"}, "idx_sessions_token_hash"},
	{"the claim horizon prune", pruneExpiredClaimsSQL, []any{"2026-01-01T00:00:00Z"}, "idx_client_claims_created_at"},
}

func TestFreshSchemaCarriesTheIndexesTheReadsWalk(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("schema-proj", "schema-proj", "/tmp/schema-proj")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("schema-task", "schema-proj")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	thread := newThread("schema-thread", "schema-task", "Main")
	if err := s.CreateThread(thread); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	for _, role := range []string{"user", "assistant"} {
		msg := newMessage("schema-"+role, "schema-task", role, role)
		msg.ThreadID = thread.ID
		if err := s.CreateTaskMessage(msg); err != nil {
			t.Fatalf("create %s message: %v", role, err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO sessions (id, token, token_hash, created_at, expires_at)
		VALUES ('schema-session', 'schema-digest', 'schema-digest', 1, 9999999999)`); err != nil {
		t.Fatalf("seed a session: %v", err)
	}

	for _, read := range indexReads {
		plan := explainPlan(t, s, read.sql, read.args...)
		if !strings.Contains(plan, read.index) {
			t.Errorf("%s does not walk %s:\n%s", read.name, read.index, plan)
		}
	}
}

// A second start must disturb nothing the first one wrote: the DDL is repeatable only because every
// statement is IF NOT EXISTS, and a reopen that logs has found something wrong with the schema.
func TestSchemaReopensQuietlyAndKeepsItsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.db")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.CreateProject(newProject("reopen-proj", "reopen-proj", "/tmp/reopen-proj")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	before := len(tableColumns(t, s.db, "tasks"))
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	logs, restore := logging.TestSink()
	defer restore()
	for i := 0; i < 3; i++ {
		reopened, err := NewStore(path)
		if err != nil {
			t.Fatalf("restart %d: %v", i, err)
		}
		if after := len(tableColumns(t, reopened.db, "tasks")); after != before {
			t.Fatalf("restart %d changed the tasks column count: %d -> %d", i, before, after)
		}
		if err := reopened.Close(); err != nil {
			t.Fatalf("close restart %d: %v", i, err)
		}
	}
	if out := logs.String(); strings.Contains(out, "schema") {
		t.Errorf("reopening a current database reported a problem:\n%s", out)
	}

	verified, err := NewStore(path)
	if err != nil {
		t.Fatalf("final reopen: %v", err)
	}
	defer func() { _ = verified.Close() }()
	if _, err := verified.GetProject("reopen-proj"); err != nil {
		t.Errorf("the row written before the restart is gone: %v", err)
	}
}

// The DDL changes no table it finds: an older tasks table keeps its columns and rows, and each read
// naming a column it lacks fails. This pins the refusal to migrate (docs/storage.md).
func TestSchemaLeavesAnOlderTableAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// The shape the tasks indexes walk, and none of the columns added since.
	if _, err := db.Exec(`CREATE TABLE tasks (
		id TEXT PRIMARY KEY, project_id TEXT NOT NULL, title TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'backlog', parent_task_id TEXT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL, moved_at TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("create the older tasks table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, created_at, updated_at)
		VALUES ('old-1', 'old-proj', 'written before this DDL', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed the older row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	found := []string{"id", "project_id", "title", "status", "parent_task_id", "created_at", "updated_at", "moved_at"}
	if have := tableColumns(t, s.db, "tasks"); strings.Join(have, ",") != strings.Join(found, ",") {
		t.Errorf("initSchema changed the table it found: %v, want %v", have, found)
	}
	var title string
	if err := s.db.QueryRow(`SELECT title FROM tasks WHERE id = 'old-1'`).Scan(&title); err != nil {
		t.Errorf("the row this file arrived with is gone: %v", err)
	}
	if _, err := s.GetTasksByProject("old-proj"); err == nil || !strings.Contains(err.Error(), "no such column") {
		t.Errorf("scanning a table missing its columns answered %v, want a missing-column failure", err)
	}
}

// storeSession is a session row with an expiry in the hour, so the expired sweep leaves it standing.
func storeSession(id string) NewSession {
	return NewSession{
		ID:        id,
		Token:     "schema-token",
		CSRFToken: "schema-csrf",
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

// tableColumns reads a table's columns from the live schema, in the order the DDL declared them.
func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatalf("read columns of %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := scanColumn[string](rows)
	if err != nil {
		t.Fatalf("scan columns of %s: %v", table, err)
	}
	return cols
}

// splitColumns turns one of the package's column-list consts into names.
func splitColumns(list string) []string {
	parts := strings.Split(list, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if name := strings.TrimSpace(p); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// notIn is what the comparison answers with: names in `a` that `b` lacks, sorted so a failure reads
// as a list rather than as map order.
func notIn(a, b []string) []string {
	have := make(map[string]bool, len(b))
	for _, name := range b {
		have[name] = true
	}
	var out []string
	for _, name := range a {
		if !have[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
