package store

import (
	"path/filepath"
	"strings"
	"testing"

	"mike/pkg/logging"
)

// A pre-release file has no marker row and holds columns nothing reads any more. The boot names both, writes
// the marker, and leaves PRAGMA user_version as it found it.
func TestAPreReleaseFileSaysWhatItIsAndIsStamped(t *testing.T) {
	s, dbPath := newStoreAt(t)
	if _, err := s.db.Exec(`ALTER TABLE tasks ADD COLUMN plan_mode TEXT`); err != nil {
		t.Fatalf("add a retired column: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM schema_meta`); err != nil {
		t.Fatalf("empty the marker: %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 12`); err != nil {
		t.Fatalf("set the chain stamp: %v", err)
	}
	s.Close()

	logs, restore := logging.TestSink()
	defer restore()
	reopened := openStore(t, dbPath)

	out := logs.String()
	for _, want := range []string{"predates v0.1.0", "tasks.plan_mode"} {
		if !strings.Contains(out, want) {
			t.Errorf("the pre-release warning never says %q:\n%s", want, out)
		}
	}
	if _, found, err := reopened.metaValue(schemaVersionKey); err != nil || !found {
		t.Errorf("a pre-release file was left unmarked (found=%v err=%v)", found, err)
	}
	if stamp, err := reopened.userVersion(); err != nil || stamp != 12 {
		t.Errorf("user_version = %d after the boot, want the 12 this build only read (err %v)", stamp, err)
	}
}

// A fresh database reads 0 in PRAGMA user_version, which is outside the pre-release range, so the same code
// that names a pre-release file says nothing here.
func TestAFreshFileIsNotReportedAsOlderThanItIs(t *testing.T) {
	logs, restore := logging.TestSink()
	defer restore()
	openStore(t, filepath.Join(t.TempDir(), "fresh.db"))

	if out := logs.String(); strings.Contains(out, "predates") {
		t.Errorf("a database this build created was reported as predating the baseline:\n%s", out)
	}
}

func TestFreshDatabaseCarriesTheSchemaMarker(t *testing.T) {
	s := testStore(t)

	value, found, err := s.metaValue(schemaVersionKey)
	if err != nil {
		t.Fatalf("metaValue: %v", err)
	}
	if !found {
		t.Fatal("a database this build created carries no schema marker")
	}
	if value != schemaVersionValue {
		t.Errorf("schema marker = %q, want %q", value, schemaVersionValue)
	}

	// A fresh file must still read 0 here, so it cannot be confused with a pre-release file.
	var userVersion int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if userVersion != 0 {
		t.Errorf("user_version = %d on a fresh database; this build writes no stamp there", userVersion)
	}
}

// A file written before the marker layer has no marker row, and a boot must add exactly one so a second boot
// cannot read the file as newer than it is. A boot adds rows and never rebuilds the table.
func TestOlderFileIsStampedOnTheFirstBootThatSeesIt(t *testing.T) {
	s, dbPath := newStoreAt(t)
	if _, err := s.db.Exec(`DELETE FROM schema_meta`); err != nil {
		t.Fatalf("empty the marker: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO schema_meta (key, value) VALUES (?, ?)`, "probe_key", "kept"); err != nil {
		t.Fatalf("write a marker row: %v", err)
	}
	if _, found, err := s.metaValue(schemaVersionKey); err != nil || found {
		t.Fatalf("expected the marker gone before the reopen (found=%v err=%v)", found, err)
	}
	s.Close()

	reopened, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	if _, found, err := reopened.metaValue(schemaVersionKey); err != nil || !found {
		t.Fatalf("a boot that saw an unmarked file left it unmarked (found=%v err=%v)", found, err)
	}
	if _, found, err := reopened.metaValue("probe_key"); err != nil || !found {
		t.Errorf("the marker row written before the reopen is gone (found=%v err=%v)", found, err)
	}
	var rows int
	if err := reopened.db.QueryRow(`SELECT count(*) FROM schema_meta WHERE key = ?`, schemaVersionKey).Scan(&rows); err != nil {
		t.Fatalf("count marker rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("schema_version has %d rows; the marker is one row per file", rows)
	}
}

// A file at the baseline has no column this release does not declare, which is the common case in the field.
// The warning still has to be a finished sentence with nothing to name.
func TestAPreReleaseFileWithNothingToNameSaysNoMore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.db")
	db := openRaw(t, path)
	for _, stmt := range splitStatements(baselineDDL(t)) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("build the baseline schema: %v", err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 7`); err != nil {
		t.Fatalf("set the chain stamp: %v", err)
	}
	closeRaw(t, db)

	logs, restore := logging.TestSink()
	defer restore()
	if _, err := NewStore(path); err != nil {
		t.Fatalf("boot: %v", err)
	}

	out := logs.String()
	if !strings.Contains(out, "predates v0.1.0") {
		t.Fatalf("a pre-release file was not named:\n%s", out)
	}
	if strings.Contains(out, "nothing reads") {
		t.Errorf("the warning offered a column list this file has no use for:\n%s", out)
	}
}

// A marker row that names another layer is rewritten with the value this build writes.
func TestAMarkerFromAnotherLayerIsRewritten(t *testing.T) {
	s, dbPath := newStoreAt(t)
	if _, err := s.db.Exec(`UPDATE schema_meta SET value = '0' WHERE key = ?`, schemaVersionKey); err != nil {
		t.Fatalf("lower the marker layer: %v", err)
	}
	s.Close()

	reopened, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	value, _, err := reopened.metaValue(schemaVersionKey)
	if err != nil {
		t.Fatalf("metaValue: %v", err)
	}
	if value != schemaVersionValue {
		t.Errorf("marker layer after the boot = %q, want %q", value, schemaVersionValue)
	}
}
