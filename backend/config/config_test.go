package config

import (
	"strconv"
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

func TestDisplayNameNotPersistedForLLM(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			// Even if a caller supplies a display name for an LLM provider, it must not be persisted.
			{Name: "llm", Kind: "llm", DisplayName: "Stale Display", URL: "http://localhost:8080/v1", Concurrency: 1},
			{Name: "pi", Kind: "agent", DisplayName: "Pi Coding Agent", URL: "agent://pi", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}

	for _, p := range read.Providers {
		if p.Name == "llm" {
			if p.DisplayName != "" {
				t.Errorf("LLM provider display name should be cleared, got %q", p.DisplayName)
			}
		}
		if p.Name == "pi" {
			if p.DisplayName != "Pi Coding Agent" {
				t.Errorf("agent provider display name should be preserved, got %q", p.DisplayName)
			}
		}
	}
}

func TestGetProviderIDByURLReturnsID(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "llm", Kind: "llm", DisplayName: "Stale Display", URL: "http://localhost:8080/v1", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// ProviderName attribution must use the stable provider ID, not the (uncached) display name.
	if got := cs.GetProviderIDByURL("http://localhost:8080/v1"); got != "1" {
		t.Errorf("GetProviderIDByURL = %q, want %q", got, "1")
	}
	if got := cs.GetProviderIDByURL("http://localhost:8080/v1/"); got != "1" {
		t.Errorf("GetProviderIDByURL (trailing slash) = %q, want %q", got, "1")
	}
	if got := cs.GetProviderIDByURL("http://unknown/v1"); got != "" {
		t.Errorf("GetProviderIDByURL (unknown) = %q, want empty", got)
	}
}

func TestUpdateConfigAssignsProviderIDs(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			{ID: 5, URL: "http://a/v1", Concurrency: 1},
			{URL: "http://b/v1", Concurrency: 1},
			{URL: "http://c/v1", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	got, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if got.Providers[0].ID != 5 {
		t.Errorf("existing provider ID changed: got %d want 5", got.Providers[0].ID)
	}
	if got.Providers[1].ID != 6 || got.Providers[2].ID != 7 {
		t.Errorf("new provider IDs not sequential after max: got %d, %d want 6, 7", got.Providers[1].ID, got.Providers[2].ID)
	}
}

func TestMigrateProviderIDs(t *testing.T) {
	cs := testConfigStore(t)

	// Seed legacy config directly (bypassing UpdateConfig, which assigns IDs):
	// providers carry slug names and no IDs, worker/utility reference the slugs.
	legacyJSON := `[{"name":"neptune-8010","kind":"llm","url":"http://neptune:8010/v1","concurrency":1,"models":[]},{"name":"other-9000","kind":"llm","url":"http://other:9000/v1","concurrency":1,"models":[]}]`
	if err := cs.store.SetConfigValue("providers", legacyJSON); err != nil {
		t.Fatalf("seed legacy providers: %v", err)
	}
	if err := cs.store.SetConfigValue("worker_provider", "neptune-8010"); err != nil {
		t.Fatalf("seed worker_provider: %v", err)
	}
	if err := cs.store.SetConfigValue("utility_provider", "other-9000"); err != nil {
		t.Fatalf("seed utility_provider: %v", err)
	}
	// Simulate existing message/token rows attributed to the legacy slugs.
	cs.store.Exec(`INSERT INTO projects (id, name, path, created_at, updated_at) VALUES ('p1', 'proj', '/tmp/proj', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`)
	cs.store.Exec(`INSERT INTO tasks (id, project_id, title, prompt, llama_server_url, model_name, created_at, updated_at) VALUES ('t1', 'p1', 'task', 'prompt', '', '', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`)
	cs.store.Exec(`INSERT INTO task_messages (id, task_id, role, content, provider_name, created_at) VALUES ('m1', 't1', 'user', 'hi', 'neptune-8010', '2024-01-01T00:00:00Z')`)

	if err := cs.MigrateProviderIDs(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	got, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	// IDs assigned, slugs cleared.
	if got.Providers[0].ID == 0 || got.Providers[0].Name != "" {
		t.Errorf("provider not migrated: got ID=%d Name=%q", got.Providers[0].ID, got.Providers[0].Name)
	}
	id := strconv.Itoa(got.Providers[0].ID)
	// worker/utility references remapped to IDs.
	if got.WorkerProvider != id {
		t.Errorf("worker_provider not remapped: got %q want %q", got.WorkerProvider, id)
	}
	// DB rows remapped to the ID.
	var stored string
	err = cs.store.QueryRow(`SELECT provider_name FROM task_messages WHERE id = 'm1'`).Scan(&stored)
	if err != nil {
		t.Fatalf("query row: %v", err)
	}
	if stored != id {
		t.Errorf("task_messages provider_name not remapped: got %q want %q", stored, id)
	}
}
