package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"mike/pkg/logging"
)

// These tests check the DDL reading against reality: the parser is compared with the schema a fresh
// database gets, and every statement is accounted for by one of the two passes.

// TestParsedSchemaIsTheSchemaADatabaseHas compares what parseTables reads out of the DDL with what a fresh
// database was built with. A parser that misses or invents a column adds or skips that column on a real file.
func TestParsedSchemaIsTheSchemaADatabaseHas(t *testing.T) {
	s := testStore(t)

	specs, err := parseTables(splitStatements(baseSchemaSQL))
	if err != nil {
		t.Fatalf("parseTables: %v", err)
	}
	if len(specs) == 0 {
		t.Fatal("the DDL declares no table")
	}
	for _, spec := range specs {
		var declared []string
		for _, c := range spec.columns {
			declared = append(declared, c.name)
		}
		have := tableColumns(t, s.db, spec.name)
		if strings.Join(declared, ",") != strings.Join(have, ",") {
			t.Errorf("%s: the DDL parses to %v, the database was built with %v", spec.name, declared, have)
		}
	}
}

// indexTarget finds the table and the columns one CREATE INDEX names.
var indexTarget = regexp.MustCompile(`(?i)\bON\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(([^)]*)\)`)

// TestEveryStatementIsRunByAPass puts each DDL statement where applySchema puts it. A statement that no pass
// runs leaves a declared table or index missing from every file this build upgrades.
func TestEveryStatementIsRunByAPass(t *testing.T) {
	statements := splitStatements(baseSchemaSQL)
	specs, err := parseTables(statements)
	if err != nil {
		t.Fatalf("parseTables: %v", err)
	}
	declared := map[string][]string{}
	for _, spec := range specs {
		declared[spec.name] = nil
		for _, c := range spec.columns {
			declared[spec.name] = append(declared[spec.name], c.name)
		}
	}

	tables := 0
	for _, stmt := range statements {
		if name, _, ok := createTableOf(stmt); ok {
			tables++
			if _, known := declared[name]; !known {
				t.Errorf("statement creates %s, which parseTables did not read", name)
			}
			continue
		}
		upper := strings.ToUpper(stmt)
		if !strings.HasPrefix(upper, "CREATE INDEX") && !strings.HasPrefix(upper, "CREATE UNIQUE INDEX") {
			t.Errorf("a statement is neither a table nor an index, so no pass runs it:\n%s", stmt)
			continue
		}
		on := indexTarget.FindStringSubmatch(stmt)
		if on == nil {
			t.Errorf("index statement names no table: %s", stmt)
			continue
		}
		table := on[1]
		columns, known := declared[table]
		if !known {
			t.Errorf("an index names %s, which the DDL declares no table for", table)
			continue
		}
		for _, want := range strings.Split(on[2], ",") {
			name := strings.TrimSpace(want)
			if name == "" {
				continue
			}
			if !slices.Contains(columns, name) {
				t.Errorf("the index on %s names %s, which its declaration does not", table, name)
			}
		}
	}
	if tables != len(specs) {
		t.Errorf("%d table statements for %d parsed tables", tables, len(specs))
	}
}

// TestSplitStatementsIgnoresSemisInsideCommentsAndStrings: the DDL carries both, and a cut inside either
// one splits a statement in two.
func TestSplitStatementsIgnoresSemisInsideCommentsAndStrings(t *testing.T) {
	ddl := `
-- a comment with a semi; in it
CREATE TABLE IF NOT EXISTS widget (
    id   TEXT PRIMARY KEY,
    kind TEXT NOT NULL DEFAULT 'a;b'
);
CREATE INDEX IF NOT EXISTS idx_widget_kind ON widget(kind);
`
	got := splitStatements(ddl)
	if len(got) != 2 {
		t.Fatalf("split into %d statements, want 2:\n%#v", len(got), got)
	}
	if _, _, ok := createTableOf(got[0]); !ok {
		t.Errorf("the first statement is not a table: %s", got[0])
	}
	if !strings.Contains(got[0], "'a;b'") {
		t.Errorf("the quoted semicolon did not survive the split: %s", got[0])
	}
	if !strings.Contains(got[0], "id   TEXT PRIMARY KEY") {
		t.Errorf("the comment's semicolon cut the statement short: %s", got[0])
	}
}

// TestEveryColumnCanBeGivenToAnOlderDatabase keeps the upgrade path open: every column the DDL declares is
// either one ALTER can add to a table that lacks it, or listed below as one no file can be missing.
func TestEveryColumnCanBeGivenToAnOlderDatabase(t *testing.T) {
	// neverAddable holds the columns no released file ever lacked: a primary key, or NOT NULL without a
	// default, cannot be given to a table that is missing it.
	neverAddable := map[string]bool{
		"projects.id": true, "projects.name": true, "projects.path": true,
		"projects.created_at": true, "projects.updated_at": true,
		"tasks.id": true, "tasks.project_id": true, "tasks.title": true, "tasks.prompt": true,
		"tasks.created_at": true, "tasks.updated_at": true,
		"config.key": true, "config.value": true,
		"task_threads.id": true, "task_threads.task_id": true, "task_threads.created_at": true,
		"task_messages.id": true, "task_messages.task_id": true, "task_messages.role": true,
		"task_messages.content": true, "task_messages.created_at": true,
		"client_claims.token": true, "client_claims.project_id": true, "client_claims.task_id": true,
		"client_claims.kind": true, "client_claims.created_at": true,
		"project_token_stats.project_id": true,
		"project_token_tasks.project_id": true, "project_token_tasks.task_id": true,
		"task_diff_snapshots.task_id": true, "task_diff_snapshots.project_id": true,
		"task_diff_snapshots.base_commit": true, "task_diff_snapshots.branch_tip": true,
		"task_diff_snapshots.stats_json": true, "task_diff_snapshots.diff_json": true,
		"task_diff_snapshots.created_at": true,
	}
	// fillsNULL holds the columns whose declaration gives SQLite nothing to fill an existing row with: the
	// row gets NULL, so scanTask, scanProject, scanMessage and scanThread must read NULL for them.
	fillsNULL := map[string]bool{
		"projects.origin_branch": true, "projects.sandbox_image": true,
		"projects.sandbox_dockerfile_path": true, "projects.sandbox_forward_ports": true,
		"projects.sandbox_mounts": true, "projects.sandbox_envs": true,
		"projects.sandbox_capabilities": true, "projects.mcp_server_ids": true,
		"tasks.base_ref": true, "tasks.base_commit": true, "tasks.branch_tip": true,
		"tasks.reasoning_effort": true, "tasks.parent_task_id": true, "tasks.current_thread_id": true,
		"tasks.assigned_ports":          true,
		"task_threads.parent_thread_id": true, "task_threads.git_branch": true,
		"task_threads.checkpoint_message_id": true, "task_threads.checkpoint_message_time": true,
		"task_threads.checkpoint_commit_hash": true, "task_threads.worktree_stash_hash": true,
		"task_messages.thread_id": true, "task_messages.severity": true,
		"task_messages.reasoning_content": true, "task_messages.tool_calls": true,
		"task_messages.tool_result": true, "task_messages.attachments": true,
		"task_messages.model_name": true, "task_messages.provider_name": true,
		"task_messages.git_commit_hash": true, "task_messages.assistant_msg_id": true,
		"task_messages.kind": true,
	}

	specs, err := parseTables(splitStatements(baseSchemaSQL))
	if err != nil {
		t.Fatalf("parseTables: %v", err)
	}
	for _, spec := range specs {
		for _, column := range spec.columns {
			name := spec.name + "." + column.name
			upper := strings.ToUpper(column.declaration)
			notNullWithoutDefault := strings.Contains(upper, "NOT NULL") && !strings.Contains(upper, "DEFAULT")
			if strings.Contains(upper, "PRIMARY KEY") || notNullWithoutDefault {
				if !neverAddable[name] {
					t.Errorf("%s cannot be given to a table that lacks it: a primary key, or NOT NULL with "+
						"nothing to fill the rows with. Declare it with a DEFAULT.", name)
				}
				continue
			}
			nullFill := !strings.Contains(upper, "DEFAULT") || strings.Contains(upper, "DEFAULT NULL")
			if nullFill && !fillsNULL[name] {
				t.Errorf("%s would arrive as NULL in the rows an older database has, which its scan reads "+
					"as a value — give it a DEFAULT, or read it as NULL", name)
			}
		}
	}
}

// foundingTasksDDL is the shape a released database can hold: every column SQLite would refuse to add
// later is present, and everything this release reads beside that is missing.
const foundingTasksDDL = `CREATE TABLE tasks (
	id          TEXT PRIMARY KEY,
	project_id  TEXT NOT NULL,
	title       TEXT NOT NULL,
	prompt      TEXT NOT NULL,
	status      TEXT NOT NULL DEFAULT 'backlog',
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
)`

// TestSchemaUpgradesAnOlderTableInPlace: a file from an older release keeps its rows, gains the columns
// this release reads, and serves them on the same boot.
func TestSchemaUpgradesAnOlderTableInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "older.db")
	db := openRaw(t, path)
	if _, err := db.Exec(foundingTasksDDL); err != nil {
		t.Fatalf("create the older tasks table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, prompt, created_at, updated_at)
		VALUES ('old-1', 'old-proj', 'written before this release', 'the prompt', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed the older row: %v", err)
	}
	closeRaw(t, db)

	logs, restore := logging.TestSink()
	defer restore()
	s := openStore(t, path)

	have := tableColumns(t, s.db, "tasks")
	for _, want := range []string{"title_llm", "execution_state", "operating_mode", "memory_alert_acked"} {
		if !slices.Contains(have, want) {
			t.Errorf("the upgrade left %s off tasks: %v", want, have)
		}
	}
	// An existing row takes the column's declared default rather than NULL.
	var (
		execution string
		titleLLM  int
	)
	if err := s.db.QueryRow(`SELECT execution_state, title_llm FROM tasks WHERE id = 'old-1'`).Scan(&execution, &titleLLM); err != nil {
		t.Fatalf("read the upgraded row: %v", err)
	}
	if execution != "idle" || titleLLM != 0 {
		t.Errorf("the added columns = (%q, %d), want (idle, 0) as the DDL defaults them", execution, titleLLM)
	}

	tasks, err := s.GetTasksByProject("old-proj")
	if err != nil {
		t.Fatalf("the board read on the upgraded file: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Title != "written before this release" {
		t.Fatalf("the row this file arrived with is not on the board: %+v", tasks)
	}

	// The indexes arrive as well, because they run after the columns they name.
	var indexes int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_tasks_project'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Errorf("idx_tasks_project did not arrive: count=%d err=%v", indexes, err)
	}

	if out := logs.String(); !strings.Contains(out, "tasks.title_llm") {
		t.Errorf("a boot that changed the file said nothing about it:\n%s", out)
	}

	// A second boot changes nothing and says nothing: the upgrade is one pass, not a re-run every start.
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	quiet, restoreQuiet := logging.TestSink()
	defer restoreQuiet()
	reopened := openStore(t, path)
	if got := tableColumns(t, reopened.db, "tasks"); strings.Join(got, ",") != strings.Join(have, ",") {
		t.Errorf("the second boot changed the table again: %v after %v", got, have)
	}
	if out := quiet.String(); strings.Contains(out, "tasks.") {
		t.Errorf("a second boot reported an upgrade it did not do:\n%s", out)
	}
}

// TestSchemaRefusesAFileItCannotBringUp: a file this release cannot complete stops the boot and names the
// column, instead of answering every read with "no such column".
func TestSchemaRefusesAFileItCannotBringUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refusing.db")
	db := openRaw(t, path)
	// No prompt column: NOT NULL with nothing to fill the existing rows with, which is the one thing an
	// ADD COLUMN cannot do.
	if _, err := db.Exec(`CREATE TABLE tasks (
		id TEXT PRIMARY KEY, project_id TEXT NOT NULL, title TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'backlog', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create the older tasks table: %v", err)
	}
	// SQLite adds a NOT NULL column to an empty table and refuses the same ALTER once a row needs a value,
	// so the row is what makes this file impossible to complete.
	if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, created_at, updated_at)
		VALUES ('old-1', 'old-proj', 'written before this release', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed the older row: %v", err)
	}
	closeRaw(t, db)

	_, err := NewStore(path)
	if err == nil {
		t.Fatal("a file this release cannot complete booted anyway")
	}
	if !strings.Contains(err.Error(), "tasks.prompt") {
		t.Errorf("the refusal does not name the column: %v", err)
	}
}

// TestSchemaRefusesWhatIsNotATable: a name the DDL declares for a table can be held by another kind of
// object, which an ALTER cannot answer to. The boot says what it found instead of failing every read.
func TestSchemaRefusesWhatIsNotATable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "view.db")
	db := openRaw(t, path)
	if _, err := db.Exec(`CREATE VIEW projects AS SELECT 1 AS id`); err != nil {
		t.Fatalf("create the view: %v", err)
	}
	closeRaw(t, db)

	_, err := NewStore(path)
	if err == nil {
		t.Fatal("a view standing where a table is declared booted anyway")
	}
	if !strings.Contains(err.Error(), "projects") {
		t.Errorf("the refusal does not name what it found: %v", err)
	}
}

// baselineSchemaFile holds the DDL exactly as the v0.1.1 tag declared it: the body of baseSchemaSQL in
// git show v0.1.1:backend/store/migrations.go. A fixture kept as a copy of a release can be re-derived from
// it, and one that was typed cannot.
const baselineSchemaFile = "testdata/schema-v0.1.1.sql"

func baselineDDL(t *testing.T) string {
	t.Helper()
	text, err := os.ReadFile(baselineSchemaFile)
	if err != nil {
		t.Fatalf("read the baseline schema %s: %v", baselineSchemaFile, err)
	}
	if len(text) == 0 {
		t.Fatalf("the baseline schema %s is empty", baselineSchemaFile)
	}
	return string(text)
}

// TestAV011DatabaseBootsAndIsStamped is the case the marker exists for. A file a released build wrote keeps
// its rows, gains what this release reads, and is left carrying the marker.
func TestAV011DatabaseBootsAndIsStamped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v0.1.1.db")
	db := openRaw(t, path)
	for _, stmt := range splitStatements(baselineDDL(t)) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("build the v0.1.1 schema from %q: %v", stmt[:min(len(stmt), 48)], err)
		}
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, path, created_at, updated_at)
		VALUES ('v011-proj', 'v011', '/tmp/v011', '2026-10-20T00:00:00Z', '2026-10-20T00:00:00Z')`); err != nil {
		t.Fatalf("seed the v0.1.1 project: %v", err)
	}
	// The row is seeded the way the v0.1.1 write path seeded it. Those three columns were nullable then, are
	// read as strings now, and a released writer stored an empty string in each.
	if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, prompt, session_id, branch, output,
	                                      created_at, updated_at)
		VALUES ('v011-task', 'v011-proj', 'written in v0.1.1', 'the prompt', '', '', '',
		        '2026-10-20T00:00:00Z', '2026-10-20T00:00:00Z')`); err != nil {
		t.Fatalf("seed the v0.1.1 task: %v", err)
	}
	if slices.Contains(tableColumns(t, db, "tasks"), "title_llm") {
		t.Fatal("the baseline fixture already has the column this release adds — it is not a v0.1.1 file")
	}
	closeRaw(t, db)

	logs, restore := logging.TestSink()
	defer restore()
	s := openStore(t, path)

	if _, found, err := s.metaValue(schemaVersionKey); err != nil || !found {
		t.Errorf("a released file booted without being marked (found=%v err=%v)", found, err)
	}
	var titleLLM int
	if err := s.db.QueryRow(`SELECT title_llm FROM tasks WHERE id = 'v011-task'`).Scan(&titleLLM); err != nil {
		t.Fatalf("the column this release added did not arrive: %v", err)
	}
	if titleLLM != 0 {
		t.Errorf("title_llm = %d on a row that predates it, want its declared default 0", titleLLM)
	}
	tasks, err := s.GetTasksByProject("v011-proj")
	if err != nil || len(tasks) != 1 || tasks[0].Title != "written in v0.1.1" {
		t.Fatalf("the board read on the released file: %d tasks, err %v", len(tasks), err)
	}
	if out := logs.String(); !strings.Contains(out, "tasks.title_llm") {
		t.Errorf("a boot that changed a released file said nothing about it:\n%s", out)
	}
}

// TestBaselineStaysASubsetOfTheDDL holds the promise that a file with no marker row is at least at the
// baseline. That is true only while today's DDL still declares everything the baseline declared, so dropping
// or renaming a baseline column fails here.
func TestBaselineStaysASubsetOfTheDDL(t *testing.T) {
	baseline, err := parseTables(splitStatements(baselineDDL(t)))
	if err != nil {
		t.Fatalf("parse the baseline schema: %v", err)
	}
	current, err := parseTables(splitStatements(baseSchemaSQL))
	if err != nil {
		t.Fatalf("parse the current DDL: %v", err)
	}

	declared := map[string]map[string]bool{}
	for _, spec := range current {
		columns := map[string]bool{}
		for _, column := range spec.columns {
			columns[column.name] = true
		}
		declared[spec.name] = columns
	}

	added := 0
	for _, spec := range baseline {
		today, tableThere := declared[spec.name]
		if !tableThere {
			t.Fatalf("the DDL no longer declares %s, which the baseline database has and the marker trusts", spec.name)
		}
		wasThere := map[string]bool{}
		for _, column := range spec.columns {
			wasThere[column.name] = true
			if !today[column.name] {
				t.Errorf("the baseline declares %s.%s, which this release dropped — an unmarked file "+
					"would be stamped as trustworthy with that column unread", spec.name, column.name)
			}
		}
		for name := range today {
			if !wasThere[name] {
				added++
			}
		}
	}
	// A baseline identical to today's DDL no longer predates this release, so it proves nothing.
	if added == 0 {
		t.Fatal("the baseline fixture declares everything the DDL declares — it no longer predates this release")
	}
}

// openRaw opens a database without running the store's schema work, so a test can write an older shape first.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func closeRaw(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// openStore opens a database through the store, closing it when the test ends.
func openStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
