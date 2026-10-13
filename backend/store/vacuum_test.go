package store

// Tests for vacuum.go: the file ends in incremental autovacuum whatever mode it arrived in, and a burst
// gives its pages back to the disk.
//
// Every size read here follows a checkpoint, because while the log is open the file's size is not its own.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func storeAt(t *testing.T, dbPath string) *Store {
	t.Helper()
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store at %s: %v", dbPath, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func pragmaInt(t *testing.T, db *sql.DB, pragma string) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRow(pragma).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", pragma, err)
	}
	return v
}

// checkpoint applies the log and returns the file's size, which is only truthful after that.
func checkpoint(t *testing.T, db *sql.DB, dbPath string) int64 {
	t.Helper()
	if err := applyWAL(db); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	st, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat %s: %v", dbPath, err)
	}
	return st.Size()
}

func TestNewStoreLeavesTheDatabaseInIncrementalAutovacuum(t *testing.T) {
	s := testStore(t)
	mode, err := s.autovacuumMode()
	if err != nil {
		t.Fatalf("read autovacuum mode: %v", err)
	}
	if mode != autoVacuumIncremental {
		t.Errorf("autovacuum mode = %d, want %d (incremental) — free pages would never leave the file", mode, autoVacuumIncremental)
	}
}

func TestNewStoreConvertsAFileLeftInTheDefaultMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "default-mode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	db.SetMaxOpenConns(1)
	// WAL, the mode the store runs in.
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatalf("put the fixture in WAL mode: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE scratch (v TEXT NOT NULL)`); err != nil {
		t.Fatalf("create fixture table: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin fixture insert: %v", err)
	}
	for i := 0; i < 8000; i++ {
		if _, err := tx.Exec(`INSERT INTO scratch (v) VALUES (?)`, fmt.Sprintf("%s %d", strings.Repeat("row", 150), i)); err != nil {
			t.Fatalf("fixture insert: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit fixture insert: %v", err)
	}
	// A block from the middle, not every other row: a page joins the freelist only when no row is left on
	// it, and one freed at the end of the file is truncated away instead.
	if _, err := db.Exec(`DELETE FROM scratch WHERE rowid BETWEEN 2000 AND 6000`); err != nil {
		t.Fatalf("delete the fixture's middle: %v", err)
	}

	if mode := pragmaInt(t, db, `PRAGMA auto_vacuum`); mode == autoVacuumIncremental {
		t.Fatal("the fixture is already in incremental autovacuum, so nothing is left for the conversion to do")
	}
	if err := applyWAL(db); err != nil {
		t.Fatalf("checkpoint the fixture: %v", err)
	}
	pagesBefore, free := pragmaInt(t, db, `PRAGMA page_count`), pragmaInt(t, db, `PRAGMA freelist_count`)
	if free == 0 {
		t.Fatal("the deleted block left no free pages, so the fixture proves nothing")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	s := storeAt(t, dbPath)

	mode, err := s.autovacuumMode()
	if err != nil {
		t.Fatalf("read autovacuum mode after conversion: %v", err)
	}
	if mode != autoVacuumIncremental {
		t.Fatalf("autovacuum mode after opening a default-mode file = %d, want %d", mode, autoVacuumIncremental)
	}
	// The schema this store adds is tens of pages, so a file no smaller was not rewritten.
	if pagesAfter := pragmaInt(t, s.db, `PRAGMA page_count`); pagesAfter >= pagesBefore {
		t.Errorf("the conversion left the file at %d pages, from %d with %d free — it was not rewritten", pagesAfter, pagesBefore, free)
	}
}

func TestReclaimFreePagesIsFlooredAndBounded(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reclaim.db")
	s := storeAt(t, dbPath)

	free := pragmaInt(t, s.db, `PRAGMA freelist_count`)
	if free >= vacuumFloorPages {
		t.Fatalf("a fresh store already holds %d free pages, so the floor cannot be measured here", free)
	}
	if freed, err := s.reclaimFreePages(vacuumFloorPages, vacuumBurstPages); err != nil {
		t.Fatalf("reclaim on a file with nothing to give back: %v", err)
	} else if freed != 0 {
		t.Errorf("reclaim below the floor returned %d pages, want 0 — every query in the process waits behind a truncation nobody asked for", freed)
	}

	// Garbage the way the board makes it: a task's messages, then the task deleted.
	if err := s.CreateProject(newProject("proj-vac", "proj-vac", "/tmp/proj-vac")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-vac", "proj-vac")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	payload := strings.Repeat("the same diff text over and over, ", 30)
	for i := 0; i < 1200; i++ {
		if err := s.CreateTaskMessage(newMessage(fmt.Sprintf("msg-vac-%04d", i), "task-vac", "tool_call", payload)); err != nil {
			t.Fatalf("create message %d: %v", i, err)
		}
	}
	if err := s.DeleteTask("proj-vac", "task-vac"); err != nil {
		t.Fatalf("delete the task whose messages made the garbage: %v", err)
	}
	peak := checkpoint(t, s.db, dbPath)
	free = pragmaInt(t, s.db, `PRAGMA freelist_count`)
	const burst = 8
	if free < burst {
		t.Fatalf("the deleted conversation left %d free pages, fewer than the %d-page burst this measures", free, burst)
	}

	// One burst returns at most its ceiling, and the bytes leave the disk rather than only the freelist.
	freed, err := s.reclaimFreePages(1, burst)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if freed != burst {
		t.Errorf("one burst of %d pages returned %d — the ceiling is not the ceiling", burst, freed)
	}
	afterBurst := checkpoint(t, s.db, dbPath)
	if afterBurst >= peak {
		t.Errorf("the file is still %d bytes after returning %d pages, so the space never left for the operating system", afterBurst, freed)
	}

	if _, err := s.reclaimFreePages(1, 1<<20); err != nil {
		t.Fatalf("reclaim the rest: %v", err)
	}
	if left := pragmaInt(t, s.db, `PRAGMA freelist_count`); left != 0 {
		t.Errorf("%d free pages left after a burst wider than the freelist", left)
	}
	if size := checkpoint(t, s.db, dbPath); size >= afterBurst {
		t.Errorf("emptying the freelist left the file at %d bytes, from %d — the pages went somewhere other than the operating system", size, afterBurst)
	}
}
