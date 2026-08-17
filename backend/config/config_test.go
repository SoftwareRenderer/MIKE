package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
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

func TestGetConfigDefaultWorkspaceDir(t *testing.T) {
	cs := testConfigStore(t)
	// No override set — should use the compiled-in default
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	// Default is resolved to absolute path
	expectedAbs, _ := filepath.Abs(DefaultWorkspaceDir)
	if read.WorkspaceDir != expectedAbs {
		t.Fatalf("expected default workspace dir %q, got %q", expectedAbs, read.WorkspaceDir)
	}
}

func TestAutoSummarizeThreshold_Default(t *testing.T) {
	cs := testConfigStore(t)
	if got := cs.AutoSummarizeThreshold(); got != 90 {
		t.Fatalf("expected default threshold 90, got %d", got)
	}
}

func TestAutoSummarizeThreshold_Roundtrip(t *testing.T) {
	cs := testConfigStore(t)
	v := 60
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.DefaultAutoSummarizeThreshold = &v
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	if got := cs.AutoSummarizeThreshold(); got != 60 {
		t.Fatalf("expected threshold 60 after update, got %d", got)
	}
}

func TestHideModelBadge_UnsetDefault(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if cfg.HideModelBadge != nil {
		t.Fatalf("expected nil HideModelBadge when unset, got %v", *cfg.HideModelBadge)
	}
}

func TestHideModelBadge_RoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.HideModelBadge = ptrBool(true)
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.HideModelBadge == nil || !*read.HideModelBadge {
		t.Fatalf("expected hide_model_badge=true after update, got %v", read.HideModelBadge)
	}
}

// An explicit false must round-trip as present (non-nil), not collapse to the
// default (badge visible).
func TestHideModelBadge_FalseRoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.HideModelBadge = ptrBool(true)
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	cfg.HideModelBadge = ptrBool(false)
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.HideModelBadge == nil || *read.HideModelBadge {
		t.Fatalf("expected hide_model_badge=false (present), got %v", read.HideModelBadge)
	}
}

// A nil HideModelBadge must be omitted from the /api/config JSON.
func TestHideModelBadge_OmittedWhenNil(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	b, err := json.Marshal(cs.buildConfigResponse(cfg))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "hide_model_badge") {
		t.Fatalf("expected hide_model_badge omitted from response when nil, got %s", b)
	}
}

func TestGetConfigRespectsWorkspaceDirOverride(t *testing.T) {
	cs := testConfigStore(t)
	overrideDir := t.TempDir()
	cs.SetWorkspaceDirOverride(overrideDir)

	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.WorkspaceDir != overrideDir {
		t.Fatalf("expected workspace dir %q, got %q", overrideDir, read.WorkspaceDir)
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

func TestBuildConfigResponseMasksAPIKeys(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1, APIKey: ptr("super-secret-key")},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	readCfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	// Sanity check: the stored key decrypts to the plaintext.
	if len(readCfg.Providers) != 1 || readCfg.Providers[0].APIKey == nil || *readCfg.Providers[0].APIKey != "super-secret-key" {
		t.Fatalf("expected decrypted key in GetConfig, got %+v", readCfg.Providers)
	}

	// The HTTP response must mask the key, never return it in plaintext.
	resp := cs.buildConfigResponse(readCfg)
	if len(resp.Providers) != 1 || resp.Providers[0].APIKey == nil {
		t.Fatalf("expected 1 provider with an APIKey field, got %+v", resp.Providers)
	}
	if got := *resp.Providers[0].APIKey; got != maskedAPIKey {
		t.Fatalf("API key not masked in response: got %q, want %q", got, maskedAPIKey)
	}
	if got := *resp.Providers[0].APIKey; got == "super-secret-key" {
		t.Fatal("plaintext API key leaked in config response")
	}
}

func TestUpdateConfig_MaxRequestsPerSecond(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			{ID: 1, URL: "http://neptune:8010/v1", Concurrency: 1, MaxRequestsPerSecond: 4},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if len(read.Providers) != 1 || read.Providers[0].MaxRequestsPerSecond != 4 {
		t.Fatalf("expected MaxRequestsPerSecond=4 persisted, got %+v", read.Providers)
	}
}

func TestHandleConfig_MaxRequestsPerSecondRoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.HandleConfig(w, r)
	}))
	defer srv.Close()

	// PUT: send providers with max_requests_per_second
	putBody := `{"providers":[{"id":1,"url":"http://neptune:8010/v1","concurrency":2,"max_requests_per_second":4,"models":[]}]}`
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(putBody))
	if err != nil {
		t.Fatalf("build PUT request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("put config: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/config status = %d, want 200", resp.StatusCode)
	}

	// GET: the value must round-trip via the JSON field
	getResp, err := http.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer getResp.Body.Close()
	var got struct {
		Providers []struct {
			URL                  string `json:"url"`
			MaxRequestsPerSecond int    `json:"max_requests_per_second"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode get config: %v", err)
	}
	if len(got.Providers) != 1 || got.Providers[0].MaxRequestsPerSecond != 4 {
		t.Fatalf("expected max_requests_per_second=4 in GET response, got %+v", got.Providers)
	}
}

func TestUpdateConfig_ClampsNegativeMaxRequestsPerSecond(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			{ID: 1, URL: "http://neptune:8010/v1", Concurrency: 1, MaxRequestsPerSecond: -5},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if len(read.Providers) != 1 || read.Providers[0].MaxRequestsPerSecond != 0 {
		t.Fatalf("expected negative MaxRequestsPerSecond clamped to 0, got %+v", read.Providers)
	}
}

func TestGetProviderMaxRequestsPerSecond(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			{ID: 1, URL: "http://neptune:8010/v1", Concurrency: 1, MaxRequestsPerSecond: 4},
			{ID: 2, URL: "http://other:9000/v1", Concurrency: 1}, // unset → 0
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// Exact URL
	if got := cs.GetProviderMaxRequestsPerSecond("http://neptune:8010/v1"); got != 4 {
		t.Fatalf("expected 4 for matching URL, got %d", got)
	}
	// Trailing-slash tolerant (like GetProviderConcurrency)
	if got := cs.GetProviderMaxRequestsPerSecond("http://neptune:8010/v1/"); got != 4 {
		t.Fatalf("expected 4 for trailing-slash URL, got %d", got)
	}
	// Unset on a known provider → 0
	if got := cs.GetProviderMaxRequestsPerSecond("http://other:9000/v1"); got != 0 {
		t.Fatalf("expected 0 for provider without the value set, got %d", got)
	}
	// Unknown URL → 0
	if got := cs.GetProviderMaxRequestsPerSecond("http://unknown/v1"); got != 0 {
		t.Fatalf("expected 0 for unknown URL, got %d", got)
	}
}

func TestKeyboardShortcuts_UnsetDefault(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if cfg.KeyboardShortcuts != nil {
		t.Fatalf("expected nil KeyboardShortcuts when unset, got %+v", cfg.KeyboardShortcuts)
	}
}

func TestKeyboardShortcuts_CustomRoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.KeyboardShortcuts = &types.KeyboardShortcuts{CreateTask: ptr("shift+k")}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.CreateTask == nil || *read.KeyboardShortcuts.CreateTask != "shift+k" {
		t.Fatalf("expected create_task=\"shift+k\", got %+v", read.KeyboardShortcuts)
	}
}

func TestKeyboardShortcuts_DisabledRoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	// Empty string means "disabled" — must round-trip as "" (not nil).
	cfg.KeyboardShortcuts = &types.KeyboardShortcuts{CreateTask: ptr("")}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.CreateTask == nil {
		t.Fatalf("expected disabled create_task (empty string), got %+v", read.KeyboardShortcuts)
	}
	if *read.KeyboardShortcuts.CreateTask != "" {
		t.Fatalf("expected empty string, got %q", *read.KeyboardShortcuts.CreateTask)
	}
}

func TestKeyboardShortcutsSubmit_CustomRoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.KeyboardShortcuts = &types.KeyboardShortcuts{CreateTaskSubmit: ptr("shift+enter")}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.CreateTaskSubmit == nil || *read.KeyboardShortcuts.CreateTaskSubmit != "shift+enter" {
		t.Fatalf("expected create_task_submit=\"shift+enter\", got %+v", read.KeyboardShortcuts)
	}
}

func TestKeyboardShortcutsSubmit_DisabledRoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.KeyboardShortcuts = &types.KeyboardShortcuts{CreateTaskSubmit: ptr("")}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.CreateTaskSubmit == nil || *read.KeyboardShortcuts.CreateTaskSubmit != "" {
		t.Fatalf("expected disabled (empty string) create_task_submit, got %+v", read.KeyboardShortcuts)
	}
}

func TestKeyboardShortcutsOpenTerminal_CustomRoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.KeyboardShortcuts = &types.KeyboardShortcuts{OpenTerminal: ptr("alt+shift+t")}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.OpenTerminal == nil || *read.KeyboardShortcuts.OpenTerminal != "alt+shift+t" {
		t.Fatalf("expected open_terminal=\"alt+shift+t\", got %+v", read.KeyboardShortcuts)
	}
	if read.KeyboardShortcuts.CreateTask != nil || read.KeyboardShortcuts.CreateTaskSubmit != nil {
		t.Fatalf("expected other bindings to stay nil, got %+v", read.KeyboardShortcuts)
	}
}

func TestKeyboardShortcutsOpenTerminal_DisabledRoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.KeyboardShortcuts = &types.KeyboardShortcuts{OpenTerminal: ptr("")}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.OpenTerminal == nil || *read.KeyboardShortcuts.OpenTerminal != "" {
		t.Fatalf("expected disabled (empty string) open_terminal, got %+v", read.KeyboardShortcuts)
	}
}

func TestKeyboardShortcutsStartTask_CustomRoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.KeyboardShortcuts = &types.KeyboardShortcuts{StartTask: ptr("shift+enter")}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.StartTask == nil || *read.KeyboardShortcuts.StartTask != "shift+enter" {
		t.Fatalf("expected start_task=\"shift+enter\", got %+v", read.KeyboardShortcuts)
	}
	if read.KeyboardShortcuts.CreateTask != nil || read.KeyboardShortcuts.CreateTaskSubmit != nil || read.KeyboardShortcuts.OpenTerminal != nil {
		t.Fatalf("expected other bindings to stay nil, got %+v", read.KeyboardShortcuts)
	}
}

func TestKeyboardShortcutsStartTask_DisabledRoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.KeyboardShortcuts = &types.KeyboardShortcuts{StartTask: ptr("")}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.StartTask == nil || *read.KeyboardShortcuts.StartTask != "" {
		t.Fatalf("expected disabled (empty string) start_task, got %+v", read.KeyboardShortcuts)
	}
}

func TestKeyboardShortcutsHideHints_RoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.KeyboardShortcuts = &types.KeyboardShortcuts{HideHints: ptrBool(true)}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.HideHints == nil || !*read.KeyboardShortcuts.HideHints {
		t.Fatalf("expected hide_hints=true, got %+v", read.KeyboardShortcuts)
	}
}

// A struct with only hide_hints set must NOT normalize to nil — otherwise the
// preference vanishes from API responses.
func TestKeyboardShortcutsHideHints_OnlyFieldStaysPresent(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.KeyboardShortcuts = &types.KeyboardShortcuts{HideHints: ptrBool(false)}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.HideHints == nil || *read.KeyboardShortcuts.HideHints {
		t.Fatalf("expected hide_hints=false (present), got %+v", read.KeyboardShortcuts)
	}
}

func TestKeyboardShortcuts_AllNilNormalizedToNil(t *testing.T) {
	cs := testConfigStore(t)
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	// A struct with no fields set should normalize back to nil (all defaults).
	cfg.KeyboardShortcuts = &types.KeyboardShortcuts{}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts != nil {
		t.Fatalf("expected nil KeyboardShortcuts after saving all-nil struct, got %+v", read.KeyboardShortcuts)
	}
}

func TestHandleConfig_KeyboardShortcutsRoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.HandleConfig(w, r)
	}))
	defer srv.Close()

	// Seed a provider so we can verify partial updates don't clobber other keys.
	seed := `{"providers":[{"id":1,"url":"http://neptune:8010/v1","concurrency":1,"models":[]}],"worker_provider":"1","worker_model":"m1","utility_provider":"1","utility_model":"m1"}`
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(seed))
	if err != nil {
		t.Fatalf("build seed request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := srv.Client().Do(req); err != nil {
		t.Fatalf("seed config: %v", err)
	} else {
		resp.Body.Close()
	}

	// Partial PUT: only keyboard_shortcuts.
	putBody := `{"keyboard_shortcuts":{"create_task":"control+k"}}`
	req, err = http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(putBody))
	if err != nil {
		t.Fatalf("build PUT request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("put config: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/config status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	getResp, err := http.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer getResp.Body.Close()
	var got struct {
		KeyboardShortcuts *struct {
			CreateTask *string `json:"create_task"`
		} `json:"keyboard_shortcuts"`
		WorkerProvider string `json:"worker_provider"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode get config: %v", err)
	}
	if got.KeyboardShortcuts == nil || got.KeyboardShortcuts.CreateTask == nil || *got.KeyboardShortcuts.CreateTask != "control+k" {
		t.Fatalf("expected keyboard_shortcuts.create_task=\"control+k\", got %+v", got.KeyboardShortcuts)
	}
	// Partial update must not clobber other config keys.
	if got.WorkerProvider != "1" {
		t.Fatalf("expected worker_provider preserved as \"1\", got %q", got.WorkerProvider)
	}
}

func TestHandleConfig_KeyboardShortcutsResetOmittedInResponse(t *testing.T) {
	cs := testConfigStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.HandleConfig(w, r)
	}))
	defer srv.Close()

	// Reset (empty struct) — the response must omit the field entirely so
	// clients see "all defaults", not a half-set binding.
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(`{"keyboard_shortcuts":{}}`))
	if err != nil {
		t.Fatalf("build PUT request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("put config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/config status = %d, want 200", resp.StatusCode)
	}
	var got map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if _, ok := got["keyboard_shortcuts"]; ok {
		t.Fatalf("expected keyboard_shortcuts omitted from PUT response after reset")
	}
}

func TestHandleConfig_KeyboardShortcutsClear(t *testing.T) {
	cs := testConfigStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.HandleConfig(w, r)
	}))
	defer srv.Close()

	// Set a custom binding, then clear it ("" = disabled).
	for _, body := range []string{
		`{"keyboard_shortcuts":{"create_task":"c"}}`,
		`{"keyboard_shortcuts":{"create_task":""}}`,
	} {
		req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(body))
		if err != nil {
			t.Fatalf("build PUT request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if resp, err := srv.Client().Do(req); err != nil {
			t.Fatalf("put config: %v", err)
		} else {
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("PUT /api/config status = %d, want 200", resp.StatusCode)
			}
			resp.Body.Close()
		}
	}

	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.CreateTask == nil {
		t.Fatalf("expected disabled (empty string) create_task, got %+v", read.KeyboardShortcuts)
	}
	if *read.KeyboardShortcuts.CreateTask != "" {
		t.Fatalf("expected empty string, got %q", *read.KeyboardShortcuts.CreateTask)
	}
}

func TestHandleConfig_KeyboardShortcutsSubmitRoundTrip(t *testing.T) {
	cs := testConfigStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.HandleConfig(w, r)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(`{"keyboard_shortcuts":{"create_task_submit":"meta+enter"}}`))
	if err != nil {
		t.Fatalf("build PUT request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("put config: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/config status = %d, want 200", resp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer getResp.Body.Close()
	var got struct {
		KeyboardShortcuts *struct {
			CreateTask       *string `json:"create_task"`
			CreateTaskSubmit *string `json:"create_task_submit"`
		} `json:"keyboard_shortcuts"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode get config: %v", err)
	}
	if got.KeyboardShortcuts == nil || got.KeyboardShortcuts.CreateTaskSubmit == nil || *got.KeyboardShortcuts.CreateTaskSubmit != "meta+enter" {
		t.Fatalf("expected create_task_submit=\"meta+enter\", got %+v", got.KeyboardShortcuts)
	}
	if got.KeyboardShortcuts.CreateTask != nil {
		t.Fatalf("expected create_task omitted, got %q", *got.KeyboardShortcuts.CreateTask)
	}
}

// The frontend contract: the Settings UI always sends BOTH bindings in the
// save payload (absent = reset to default), so the replace semantics let
// "Reset to default" work without a special clear token.
func TestHandleConfig_KeyboardShortcutsReplaceSemantics(t *testing.T) {
	cs := testConfigStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.HandleConfig(w, r)
	}))
	defer srv.Close()

	for _, body := range []string{
		`{"keyboard_shortcuts":{"create_task":"shift+k","create_task_submit":"shift+enter","open_terminal":"alt+shift+t"}}`,
		`{"keyboard_shortcuts":{"create_task":"c"}}`, // no create_task_submit/open_terminal -> reset to default
	} {
		req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(body))
		if err != nil {
			t.Fatalf("build PUT request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if resp, err := srv.Client().Do(req); err != nil {
			t.Fatalf("put config: %v", err)
		} else {
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("PUT /api/config status = %d, want 200", resp.StatusCode)
			}
			resp.Body.Close()
		}
	}

	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.CreateTask == nil || *read.KeyboardShortcuts.CreateTask != "c" {
		t.Fatalf("expected create_task=\"c\", got %+v", read.KeyboardShortcuts)
	}
	if read.KeyboardShortcuts.CreateTaskSubmit != nil {
		t.Fatalf("expected create_task_submit reset to default (nil), got %+v", read.KeyboardShortcuts.CreateTaskSubmit)
	}
	if read.KeyboardShortcuts.OpenTerminal != nil {
		t.Fatalf("expected open_terminal reset to default (nil), got %+v", read.KeyboardShortcuts.OpenTerminal)
	}
}

// TestGetModelReasoningEffort: explicit values pass through; empty or unknown
// models fall back to "default".
func TestGetModelReasoningEffort(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			{
				Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1,
				Models: []types.ModelConfig{
					{Name: "known", ReasoningEffort: "high"},
					{Name: "empty", ReasoningEffort: ""},
				},
			},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	if got := cs.GetModelReasoningEffort("known"); got != "high" {
		t.Errorf("known model: got %q, want high", got)
	}
	if got := cs.GetModelReasoningEffort("empty"); got != "default" {
		t.Errorf("empty-effort model: got %q, want default", got)
	}
	if got := cs.GetModelReasoningEffort("unknown-model"); got != "default" {
		t.Errorf("unknown model: got %q, want default", got)
	}
}

func ptr(s string) *string { return &s }
func ptrBool(b bool) *bool { return &b }
