package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"mike/pkg/logging"
)

func TestSchemaStepRunsOnceAndIsRemembered(t *testing.T) {
	s := testStore(t)
	runs := 0
	steps := []schemaStep{{
		Key:   "2026-01-01-probe",
		Apply: func(*Store) error { runs++; return nil },
	}}

	for boot := 0; boot < 2; boot++ {
		if err := s.runSchemaSteps(steps); err != nil {
			t.Fatalf("boot %d: %v", boot, err)
		}
	}
	if runs != 1 {
		t.Errorf("the step ran %d times across two boots, want once", runs)
	}
	if done, err := s.stepDone("2026-01-01-probe"); err != nil || !done {
		t.Errorf("the step is not recorded as run (done=%v err=%v)", done, err)
	}
}

func TestSchemaStepFailureRefusesTheBoot(t *testing.T) {
	s := testStore(t)
	cause := errors.New("the rewrite could not finish")
	err := s.runSchemaSteps([]schemaStep{{
		Key:   "2026-01-01-failing",
		Apply: func(*Store) error { return cause },
	}})
	if err == nil {
		t.Fatal("a step that failed let the boot continue")
	}
	if !strings.Contains(err.Error(), "2026-01-01-failing") {
		t.Errorf("the refusal does not name the step: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("the refusal lost the cause: %v", err)
	}
	// Nothing is recorded, so the next boot runs the step again.
	if done, err := s.stepDone("2026-01-01-failing"); err != nil || done {
		t.Errorf("a failed step was recorded as done (done=%v err=%v)", done, err)
	}
}

func TestMarkStepDoneRecordsTheWayBackOnlyForAOneWayStep(t *testing.T) {
	s := testStore(t)
	if err := s.markStepDone("additive", false); err != nil {
		t.Fatalf("mark an additive step: %v", err)
	}
	if err := s.markStepDone("destructive", true); err != nil {
		t.Fatalf("mark a one-way step: %v", err)
	}
	if _, found, err := s.metaValue(oneWayRowPrefix + "additive"); err != nil || found {
		t.Errorf("an additive step was marked one-way (found=%v err=%v)", found, err)
	}
	if _, found, err := s.metaValue(oneWayRowPrefix + "destructive"); err != nil || !found {
		t.Errorf("a one-way step is not marked, so an older binary would boot over it (found=%v err=%v)", found, err)
	}
}

// An older build could add a column without a default, which left NULL in rows that the task scan reads as
// strings, so every board read failed. The shape pass cannot see this, because the column is there.
func TestARowThatArrivedWithNullsBootsAndReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nulls.db")
	db := openRaw(t, path)
	for _, stmt := range splitStatements(baselineDDL(t)) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("build the older schema: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, path, created_at, updated_at)
		VALUES ('np', 'np', '/tmp/np', '2026-10-20T00:00:00Z', '2026-10-20T00:00:00Z')`); err != nil {
		t.Fatalf("seed the project: %v", err)
	}
	// The three columns are left out, the way an ALTER without a default leaves them.
	if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, prompt, created_at, updated_at)
		VALUES ('nt', 'np', 'row with the columns unfilled', 'the prompt',
		        '2026-10-20T00:00:00Z', '2026-10-20T00:00:00Z')`); err != nil {
		t.Fatalf("seed the task: %v", err)
	}
	if nulls, err := countNulls(db); err != nil || nulls != 3 {
		t.Fatalf("the seeded row holds %d NULL values in those columns, want 3 (err %v)", nulls, err)
	}
	closeRaw(t, db)

	logs, restore := logging.TestSink()
	defer restore()
	s := openStore(t, path)

	tasks, err := s.GetTasksByProject("np")
	if err != nil {
		t.Fatalf("the board read over the row that arrived with NULLs: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("the row is not on the board: %d tasks", len(tasks))
	}
	if tasks[0].SessionID != "" || tasks[0].Branch != "" || tasks[0].Output != "" {
		t.Errorf("the row reads (%q, %q, %q), want the blank its declaration promises",
			tasks[0].SessionID, tasks[0].Branch, tasks[0].Output)
	}
	if done, err := s.stepDone(schemaSteps[0].Key); err != nil || !done {
		t.Errorf("the step that fixed the row is not recorded (done=%v err=%v)", done, err)
	}
	if out := logs.String(); !strings.Contains(out, "blanked 3 task row values") {
		t.Errorf("a boot that rewrote the row said nothing about it:\n%s", out)
	}
}

// The column is out of this build's DDL, so the shape pass will not touch it and only a step can. An older file
// that has it loses it, and its project row keeps everything this build reads.
func TestAnOlderFileLosesTheRunSetColumnAndKeepsItsRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runset.db")
	db := openRaw(t, path)
	for _, stmt := range splitStatements(baselineDDL(t)) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("build the older schema: %v", err)
		}
	}
	if _, err := db.Exec(`ALTER TABLE projects ADD COLUMN sandbox_run_set_hash TEXT DEFAULT NULL`); err != nil {
		t.Fatalf("add the column the way the release that had it did: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, path, sandbox_run_set_hash, created_at, updated_at)
		VALUES ('rp', 'runset', '/tmp/runset', '` + strings.Repeat("a", 64) + `', '2026-10-20T00:00:00Z', '2026-10-20T00:00:00Z')`); err != nil {
		t.Fatalf("seed the project: %v", err)
	}
	closeRaw(t, db)

	_, restore := logging.TestSink()
	defer restore()
	s := openStore(t, path)

	if _, err := s.db.Exec(`SELECT sandbox_run_set_hash FROM projects`); err == nil {
		t.Error("the column survived the boot that dropped it")
	}
	p, err := s.GetProject("rp")
	if err != nil {
		t.Fatalf("the project row did not survive its column: %v", err)
	}
	if p.Name != "runset" || p.Path != "/tmp/runset" {
		t.Errorf("the row reads (%q, %q), want the values it was seeded with", p.Name, p.Path)
	}
	if done, err := s.stepDone(dropRunSetHashColumnKey); err != nil || !done {
		t.Errorf("the step that dropped the column is not recorded (done=%v err=%v)", done, err)
	}
	// An older build selects the column by name, so the file has to refuse it rather than fail every read.
	if _, found, err := s.metaValue(oneWayRowPrefix + dropRunSetHashColumnKey); err != nil || !found {
		t.Errorf("a step that destroyed a column is not marked one-way (found=%v err=%v)", found, err)
	}
}

// This build's own DDL has no such column, so on a fresh file the step has to decline rather than fail.
func TestAFreshFileBootsOverTheDropStepWithNothingToDrop(t *testing.T) {
	s, _ := newStoreAt(t)
	columns, _, err := s.liveColumns("projects")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range columns {
		if c == "sandbox_run_set_hash" {
			t.Fatal("a file this build created has the column this build does not have")
		}
	}
	if err := s.runSchemaSteps([]schemaStep{{Key: "2026-11-02-probe", Apply: dropProjectRunSetHash}}); err != nil {
		t.Errorf("the drop was refused where there was nothing to drop: %v", err)
	}
}

// markFutureRow writes a marker row for a step this build does not have. It writes the table directly, so the
// test does not use the code under test.
func markFutureRow(t *testing.T, s *Store, key string, oneWay bool) {
	t.Helper()
	rows := [][2]string{{stepRowPrefix + key, stepRowValue}}
	if oneWay {
		rows = append(rows, [2]string{oneWayRowPrefix + key, stepRowValue})
	}
	for _, row := range rows {
		if _, err := s.db.Exec(`INSERT INTO schema_meta (key, value) VALUES (?, ?)`, row[0], row[1]); err != nil {
			t.Fatalf("write %s: %v", row[0], err)
		}
	}
}

// An unknown step that only added data is not a reason to refuse, because this build ignores a column it does
// not know. The boot warns and continues.
func TestAFileFromTheFutureBootsWhenTheStepOnlyAdded(t *testing.T) {
	s, dbPath := newStoreAt(t)
	markFutureRow(t, s, "2999-01-01-adds-a-column", false)
	s.Close()

	logs, restore := logging.TestSink()
	defer restore()
	openStore(t, dbPath)

	if out := logs.String(); !strings.Contains(out, "2999-01-01-adds-a-column") {
		t.Errorf("a boot over a newer file did not say which step it cannot see:\n%s", out)
	}
}

// A step recorded as one-way moved data this build still expects to find, so booting would show a board built
// from what is left. It refuses and names the step.
func TestAFileFromTheFutureRefusesAStepItCannotUndo(t *testing.T) {
	s, dbPath := newStoreAt(t)
	markFutureRow(t, s, "2999-01-01-renames-a-column", true)
	s.Close()

	_, err := NewStore(dbPath)
	if err == nil {
		t.Fatal("an older binary booted a file whose data a one-way step had moved")
	}
	if !strings.Contains(err.Error(), "2999-01-01-renames-a-column") {
		t.Errorf("the refusal does not name the step: %v", err)
	}
}

// countNulls counts the NULL values in those columns, read straight from the file.
func countNulls(db execer) (int, error) {
	total := 0
	for _, column := range nullToBlankColumns {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM tasks WHERE ` + column + ` IS NULL`).Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}
