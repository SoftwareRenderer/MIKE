package config

import (
	"os"
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

	// workspace_dir is set via CLI/env, not via UpdateConfig.
	// Use a temp dir for the test fixture.
	wsDir := t.TempDir()
	if err := cs.store.SetConfigValue("workspace_dir", wsDir); err != nil {
		t.Fatalf("set workspace_dir: %v", err)
	}

	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.WorkspaceDir != wsDir {
		t.Fatalf("expected %q, got %q", wsDir, read.WorkspaceDir)
	}
}

func TestUpdateConfigIgnoresWorkspaceDir(t *testing.T) {
	cs := testConfigStore(t)

	// Set initial workspace_dir via store
	originalDir := t.TempDir()
	if err := cs.store.SetConfigValue("workspace_dir", originalDir); err != nil {
		t.Fatalf("set workspace_dir: %v", err)
	}

	// Try to change workspace_dir via UpdateConfig — should be ignored
	newDir := t.TempDir()
	cfg := &types.Config{WorkspaceDir: newDir}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.WorkspaceDir != originalDir {
		t.Fatalf("workspace_dir should not change via UpdateConfig: got %q", read.WorkspaceDir)
	}
}

func TestGetAgentConfigDir_NoAgent(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// no agent configured
	if got := cs.GetAgentConfigDir(); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestGetAgentConfigDir_PiProvider(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
			{Name: "pi", Kind: "agent", URL: "pi://agent", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	got := cs.GetAgentConfigDir()
	if got == "" {
		t.Fatal("expected non-empty agent config dir")
	}
	// defaults to ~/.pi/agent
	homeDir, _ := os.UserHomeDir()
	expected := homeDir + "/.pi/agent"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestGetAgentConfigDir_PiWithExplicitDir(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "pi", Kind: "agent", URL: "pi://agent", AgentConfigDir: "/custom/pi/config", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	got := cs.GetAgentConfigDir()
	if got != "/custom/pi/config" {
		t.Errorf("expected /custom/pi/config, got %q", got)
	}
}

func TestGetAgentConfigDir_PiWithTildeDir(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "pi", Kind: "agent", URL: "pi://agent", AgentConfigDir: "~/.my-pi", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	got := cs.GetAgentConfigDir()
	homeDir, _ := os.UserHomeDir()
	expected := homeDir + "/.my-pi"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestIsAgentProvider(t *testing.T) {
	cs := testConfigStore(t)

	// Set up config with both LLM and agent providers
	cfg := &types.Config{
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
