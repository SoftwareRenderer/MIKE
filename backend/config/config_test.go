package config

import (
	"testing"

	"kanban/backend/store"
	"kanban/backend/types"
)

func testConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	dir := t.TempDir()
	db, err := store.NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	return NewConfigStore(db, dir+"/test.db")
}

func TestGetConfig(t *testing.T) {
	cs := testConfigStore(t)

	// Set workspace_dir (required for GetConfig to succeed)
	cfg := &types.Config{WorkspaceDir: "/tmp/workspace"}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("initial config: %v", err)
	}

	// Verify it was saved
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.WorkspaceDir != "/tmp/workspace" {
		t.Fatalf("expected /tmp/workspace, got %q", read.WorkspaceDir)
	}
}
