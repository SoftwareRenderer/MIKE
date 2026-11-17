// Package teststore builds the storage half of a test fixture, so the packages that touch a task
// stop each carrying their own copy of "open a throwaway database, build the config store the
// server builds".
//
//   - The database is a file in a temporary directory of its own, so a test never reaches the
//     operator's data directory and two tests never share a writer.
//   - The config store carries the arguments every fixture repeats and no test is allowed to
//     choose: the "test" version string, no encryption box, and neither restriction lock held. A
//     test that wants a locked decision asks for it itself, which is what makes it the one thing
//     that test fakes.
//
// Test-only: nothing outside _test files may import it.
package teststore

import (
	"path/filepath"
	"testing"

	"mike/backend/config"
	"mike/backend/store"
)

// Store opens a fresh database in a directory of its own and closes it when the test ends.
func Store(t testing.TB) *store.Store {
	t.Helper()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// StoreIn opens a fresh database at dbPath, for a fixture that wants the file at a stated place —
// a reopen, or a config snapshot that reports the path.
func StoreIn(t testing.TB, dbPath string) *store.Store {
	t.Helper()
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// ConfigStore is the config store every fixture builds identically over s. dbPath is only what the
// config snapshot reports as DatabasePath; a fixture with no database file of its own passes "".
func ConfigStore(t testing.TB, s *store.Store, dbPath string) *config.ConfigStore {
	t.Helper()
	return config.NewConfigStore(s, dbPath, "test", nil, false, false)
}
