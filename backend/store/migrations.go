package store

import (
	"fmt"

	"kanban/pkg/logging"
)

// SQLite has no "ALTER TABLE … ADD COLUMN IF NOT EXISTS", so startup used to fire ~50
// unconditional ALTERs and discard their errors - 34 of which can do nothing except fail
// with "duplicate column name". Asking first makes a logged failure a real failure.
//
// The cost is a probe per column, 53 per start, each one a no-op on a current database.
// schemaVersion is what lets a database that has been through the chain skip it.

// schemaVersion is the value PRAGMA user_version carries once initSchema has brought a database up
// to date, and the number a new migration bumps.
//
// To add a migration: extend initSchema's probe chain and raise this constant, leaving the earlier
// steps in place. A database stamped N has been through 1..N; an older one reads 0 and runs the
// whole chain. So the steps must stay idempotent, and one that must run exactly once needs its own
// marker (see the config-row guard on the backfill) rather than trusting this number.
const schemaVersion = 1

// userVersion reads PRAGMA user_version. A read failure answers 0, which reruns the probe chain:
// slow, but it cannot leave a column missing. The caller holds s.mu.
func (s *Store) userVersion() int {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		logging.For(logging.Store).Warnf("reading user_version failed, re-running schema probes: %v", err)
		return 0
	}
	return v
}

// setUserVersion stamps the schema version. A failed stamp only costs a rerun of the chain on the
// next start, so it is logged rather than fatal; refusing to open the database would be worse.
func (s *Store) setUserVersion(v int) {
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v)); err != nil {
		logging.For(logging.Store).Warnf("stamping schema version %d failed: %v", v, err)
	}
}

// hasColumn reports whether an existing table carries the named column. A missing table
// reads as "no such column", which is honest: the ALTER would have failed on it anyway.
func (s *Store) hasColumn(table, column string) bool {
	var n int
	// pragma_table_info is a table-valued function, so the table name binds as a
	// parameter instead of being formatted into SQL.
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		logging.For(logging.Store).Warnf("schema probe %s.%s failed: %v", table, column, err)
		return false
	}
	return n > 0
}

// addColumn adds column with the given definition (everything after
// "ADD COLUMN <name>") unless the table already has it.
func (s *Store) addColumn(table, column, def string) {
	if s.hasColumn(table, column) {
		return
	}
	if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, def)); err != nil {
		logging.For(logging.Store).Warnf("schema migration %s.%s failed: %v", table, column, err)
	}
}

// renameColumn renames from to to, and only while the database still uses the old name.
// If both names exist the data is ambiguous and a rename would lose a column, so it
// refuses and leaves it for a human.
func (s *Store) renameColumn(table, from, to string) {
	if !s.hasColumn(table, from) || s.hasColumn(table, to) {
		return
	}
	if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE %s RENAME COLUMN %s TO %s`, table, from, to)); err != nil {
		logging.For(logging.Store).Warnf("schema rename %s.%s -> %s failed: %v", table, from, to, err)
	}
}
