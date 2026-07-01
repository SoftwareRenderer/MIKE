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

func TestIsAgentProvider(t *testing.T) {
	cs := testConfigStore(t)

	// Set up config with both LLM and agent providers
	cfg := &types.Config{
		WorkspaceDir: "/tmp/workspace",
		Providers: []types.Provider{
			{Name: "llm-provider", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
			{Name: "pi-agent", Kind: "pi", URL: "pi://agent", Concurrency: 1},
			{Name: "other-agent", Kind: "claude", URL: "https://api.claude.com/v1", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// LLM provider should NOT be an agent
	if cs.IsAgentProvider("http://localhost:8080/v1") {
		t.Error("LLM provider should not be detected as agent")
	}

	// Pi agent should be detected as agent
	if !cs.IsAgentProvider("pi://agent") {
		t.Error("Pi agent should be detected as agent")
	}

	// Other agent kind should be detected as agent
	if !cs.IsAgentProvider("https://api.claude.com/v1") {
		t.Error("Other agent kind should be detected as agent")
	}

	// pi://agent sentinel URL should be detected as agent (even without registered provider)
	if !cs.IsAgentProvider("pi://agent") {
		t.Error("pi://agent sentinel URL should be detected as agent")
	}

	// Unknown URL should return false
	if cs.IsAgentProvider("http://unknown/v1") {
		t.Error("Unknown URL should not be detected as agent")
	}
}
