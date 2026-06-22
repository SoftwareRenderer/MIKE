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

func TestUpdateConfigSandboxImage(t *testing.T) {
	cs := testConfigStore(t)

	// Set workspace_dir (required for GetConfig to succeed)
	cfg := &types.Config{WorkspaceDir: "/tmp/workspace"}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("initial config: %v", err)
	}

	// Set a custom sandbox image
	cfg.DefaultSandboxImage = "docker.io/library/ubuntu:22.04"
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("set sandbox image: %v", err)
	}

	// Verify it was saved
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.DefaultSandboxImage != "docker.io/library/ubuntu:22.04" {
		t.Fatalf("expected ubuntu:22.04, got %q", read.DefaultSandboxImage)
	}

	// Clear the sandbox image (set to empty string)
	cfg.DefaultSandboxImage = ""
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("clear sandbox image: %v", err)
	}

	// Verify it was cleared
	read, err = cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.DefaultSandboxImage != "" {
		t.Fatalf("expected empty sandbox image after clear, got %q", read.DefaultSandboxImage)
	}
}
