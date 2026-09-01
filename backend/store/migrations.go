package store

import (
	"fmt"
	"log"
)

// SQLite has no "ALTER TABLE … ADD COLUMN IF NOT EXISTS", so startup used to fire ~50
// unconditional ALTERs and discard their errors - 34 of which can do nothing except fail
// with "duplicate column name". Asking first makes a logged failure a real failure.

// hasColumn reports whether an existing table carries the named column. A missing table
// reads as "no such column", which is honest: the ALTER would have failed on it anyway.
func (s *Store) hasColumn(table, column string) bool {
	var n int
	// pragma_table_info is a table-valued function, so the table name binds as a
	// parameter instead of being formatted into SQL.
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		log.Printf("[store] schema probe %s.%s failed: %v", table, column, err)
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
		log.Printf("[store] schema migration %s.%s failed: %v", table, column, err)
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
		log.Printf("[store] schema rename %s.%s -> %s failed: %v", table, from, to, err)
	}
}
