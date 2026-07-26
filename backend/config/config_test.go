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
	return NewConfigStore(db, dir+"/test.db", "test")
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

func TestIsAgentProvider(t *testing.T) {
	cs := testConfigStore(t)

	// Set up config with both LLM and agent providers
	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "llm-provider", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
			{Name: "claude-agent", Kind: "agent", URL: "agent://claude", Concurrency: 1},
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

	// agent:// URL should be detected as agent
	if !cs.IsAgentProvider("agent://claude") {
		t.Error("agent:// URL should be detected as agent")
	}

	// Other agent kind should be detected as agent
	if !cs.IsAgentProvider("https://api.claude.com/v1") {
		t.Error("Other agent kind should be detected as agent")
	}

	// Unknown URL should return false
	if cs.IsAgentProvider("http://unknown/v1") {
		t.Error("Unknown URL should not be detected as agent")
	}
}

func TestGetAgentProvider(t *testing.T) {
	cs := testConfigStore(t)

	// No agents configured
	if p := cs.GetAgentProvider(); p != nil {
		t.Errorf("expected nil, got %v", p)
	}

	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
			{Name: "my-agent", Kind: "agent", URL: "agent://my-agent", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	if p := cs.GetAgentProvider(); p == nil || p.Name != "my-agent" {
		t.Errorf("expected my-agent, got %v", p)
	}
}

// mockAgentLister implements AgentProviderLister for testing.
type mockAgentLister struct {
	providers []types.Provider
}

func (m *mockAgentLister) ToProviders() []types.Provider {
	return m.providers
}

func TestFilterPersistedProviders(t *testing.T) {
	providers := []types.Provider{
		{Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
		{Name: "pi", Kind: "agent", URL: "agent://pi", Concurrency: 1},
		{Name: "other-llm", Kind: "llm", URL: "http://localhost:9090/v1", Concurrency: 2},
	}
	filtered := filterPersistedProviders(providers)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(filtered))
	}
	if filtered[0].Name != "llm" || filtered[1].Name != "other-llm" {
		t.Errorf("agent:// provider was not filtered out")
	}
}

func TestBuildConfigResponseMergesAgentProviders(t *testing.T) {
	cs := testConfigStore(t)

	// Store an LLM provider
	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// No agent registry wired — response should only have stored providers
	readCfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	resp := cs.buildConfigResponse(readCfg)
	if len(resp.Providers) != 1 {
		t.Fatalf("expected 1 provider without agent registry, got %d", len(resp.Providers))
	}

	// Wire an agent registry that provides 'Pi Coding Agent'
	cs.SetAgentRegistry(&mockAgentLister{
		providers: []types.Provider{
			{Name: "pi", Kind: "agent", DisplayName: "Pi Coding Agent", URL: "agent://pi", Concurrency: 1},
		},
		})

	resp = cs.buildConfigResponse(readCfg)
	if len(resp.Providers) != 2 {
		t.Fatalf("expected 2 providers with agent registry, got %d", len(resp.Providers))
	}

	// The agent provider should have the correct display name
	var found bool
	for _, p := range resp.Providers {
		if p.URL == "agent://pi" {
			if p.DisplayName != "Pi Coding Agent" {
				t.Errorf("expected display name 'Pi Coding Agent', got '%s'", p.DisplayName)
			}
			found = true
		}
	}
	if !found {
		t.Error("agent://pi provider not found in response")
	}
}
