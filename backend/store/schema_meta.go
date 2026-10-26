package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"mike/pkg/logging"
)

// schema_meta records the schema work a database file has been through. Its columns cannot show that, and a
// step that rewrites data needs to know it. The rule is in docs/storage.md.
const schemaMetaTable = `
CREATE TABLE IF NOT EXISTS schema_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

// schemaVersionKey holds the marker layer, not progress through the ladder. A file's progress is the step
// rows it holds.
const schemaVersionKey = "schema_version"

const schemaVersionValue = "1"

// stepRowPrefix marks a step this file has run. oneWayRowPrefix marks a step after which an older build must
// not read the file.
const (
	stepRowPrefix   = "step:"
	oneWayRowPrefix = "one_way:"
	stepRowValue    = "done"
)

func (s *Store) initSchemaMeta() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applySchema(schemaMetaTable)
}

// stampSchemaMarker writes the marker row. It writes only when the row is missing or names another layer, so
// a file this build has already stamped is not written again.
func (s *Store) stampSchemaMarker() error {
	value, found, err := s.metaValue(schemaVersionKey)
	if err != nil {
		return err
	}
	if found && value == schemaVersionValue {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := setMetaRow(s.db, schemaVersionKey, schemaVersionValue); err != nil {
		return fmt.Errorf("stamp schema marker: %w", err)
	}
	return nil
}

// setMetaRow writes one marker row and replaces any previous value. It takes an execer so a step can write
// its marker inside its own transaction.
func setMetaRow(e execer, key, value string) error {
	_, err := e.Exec(`INSERT INTO schema_meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("write schema marker %s: %w", key, err)
	}
	return nil
}

// preReleaseStampTop is the highest PRAGMA user_version a pre-release build left behind. A file inside that
// range predates the first tag. A larger number belongs to no known chain, so the boot says nothing.
const preReleaseStampTop = 12

// preReleaseWarning is what such an operator is told.
const preReleaseWarning = "this database predates v0.1.0. It carries %d in PRAGMA user_version, which only " +
	"pre-release builds wrote. Its structure has been completed to this release."

// warnIfPredatesTheBaseline logs that a pre-release file was completed to this release's structure, and names
// the columns nothing reads any more. The missing marker row is part of the evidence, so this runs before the
// marker is written.
func (s *Store) warnIfPredatesTheBaseline() error {
	if _, found, err := s.metaValue(schemaVersionKey); err != nil || found {
		return err
	}
	stamp, err := s.userVersion()
	if err != nil {
		return err
	}
	if stamp <= 0 || stamp > preReleaseStampTop {
		return nil
	}
	// A file at the baseline has nothing undeclared, so the column sentence is only said when it has something
	// to name.
	extra := s.undeclaredColumns()
	if len(extra) == 0 {
		logging.For(logging.Store).Warnf(preReleaseWarning, stamp)
		return nil
	}
	logging.For(logging.Store).Warnf(preReleaseWarning+" These columns are in the file and nothing reads them: %s",
		stamp, strings.Join(extra, " "))
	return nil
}

// userVersion reads PRAGMA user_version, which this build never writes.
func (s *Store) userVersion() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var stamp int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&stamp); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return stamp, nil
}

// undeclaredColumns lists the columns a file has that this release's DDL does not declare, as
// tasks.plan_mode. A file upgraded through several releases keeps some, because columns are never dropped.
func (s *Store) undeclaredColumns() []string {
	specs, err := parseTables(splitStatements(baseSchemaSQL))
	if err != nil {
		// The DDL is a constant, so this cannot fail, and a warning is not worth refusing a boot for.
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, spec := range specs {
		live, kind, err := s.liveColumns(spec.name)
		if err != nil || kind != "table" {
			continue
		}
		declared := map[string]bool{}
		for _, column := range spec.columns {
			declared[column.name] = true
		}
		for _, column := range live {
			if !declared[column] {
				out = append(out, spec.name+"."+column)
			}
		}
	}
	return out
}

// metaValue reads one marker row and reports whether the row exists.
func (s *Store) metaValue(key string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var value string
	err := s.db.QueryRow(`SELECT value FROM schema_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read schema marker %s: %w", key, err)
	}
	return value, true, nil
}

// markerKeys lists the marker keys that start with prefix, in key order.
func (s *Store) markerKeys(prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT key FROM schema_meta WHERE key LIKE ? ORDER BY key`, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("list schema markers under %s: %w", prefix, err)
	}
	defer func() { _ = rows.Close() }()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("list schema markers under %s: %w", prefix, err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
