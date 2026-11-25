package config

import (
	"path/filepath"
	"testing"

	"mike/backend/store"
)

// DatabaseDir must name the same directory as the DatabasePath GetConfig publishes, or the deletion guard
// protects a directory this server does not own.

func TestDatabaseDirNamesTheDirectoryHoldingTheDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := store.NewStore(filepath.Join(dir, "kanban.db"))
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	cs := NewConfigStore(db, filepath.Join(dir, "kanban.db"), "test", sharedSecretBox(), false, false)

	got := cs.DatabaseDir()
	if got != dir {
		t.Errorf("DatabaseDir() = %q, want %q", got, dir)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if filepath.Dir(read.DatabasePath) != got {
		t.Errorf("GetConfig().DatabasePath = %q does not sit in DatabaseDir() = %q", read.DatabasePath, got)
	}
}
