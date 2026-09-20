package config

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/crypto"
	"mike/backend/store"
	"mike/backend/types"
)

// sharedSecretBox is derived once for the whole package: a per-test derivation would dominate the suite.
// A test that needs its own key builds or passes one explicitly.
var sharedSecretBox = sync.OnceValue(func() *crypto.Box {
	box, err := crypto.New("test-passphrase", []byte("0123456789abcdef"))
	if err != nil {
		panic(err)
	}
	return box
})

func testConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	cs, _ := testConfigStoreWithStore(t)
	return cs
}

// testConfigStoreWithStore also hands back the store, for the few tests that seed rows
// directly — raw SQL belongs in a fixture, not in ConfigStore's surface.
func testConfigStoreWithStore(t *testing.T) (*ConfigStore, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.NewStore(dir + "/test.db")
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	return NewConfigStore(db, dir+"/test.db", "test", sharedSecretBox()), db
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

// TestUIFlagPreferences covers every *bool preference in the config in one place,
// because they share one storage rule and its three failure modes:
//
//   - unset must stay nil, so /api/config OMITS the key and the client default applies;
//   - true must round-trip;
//   - an explicit false must come back PRESENT. Collapsing it to nil would quietly
//     restore the behaviour the user just switched off, which is the bug each row below
//     is written to catch.
func TestUIFlagPreferences(t *testing.T) {
	flags := []struct {
		name string
		set  func(cfg *types.Config, v *bool)
		get  func(cfg *types.Config) *bool
		// accessor is the read path some preferences expose; nil when there is none.
		accessor func(cs *ConfigStore) bool
	}{
		{
			name: "hide_model_badge",
			set:  func(c *types.Config, v *bool) { c.HideModelBadge = v },
			get:  func(c *types.Config) *bool { return c.HideModelBadge },
		},
		{
			name:     "run_siblings_parallel",
			set:      func(c *types.Config, v *bool) { c.RunSiblingsParallel = v },
			get:      func(c *types.Config) *bool { return c.RunSiblingsParallel },
			accessor: func(cs *ConfigStore) bool { return cs.GetRunSiblingsParallel() },
		},
		{
			name: "sort_oldest_first",
			set:  func(c *types.Config, v *bool) { c.SortOldestFirst = v },
			get:  func(c *types.Config) *bool { return c.SortOldestFirst },
		},
		{
			// The client default is ON, so a stored false is the only thing that turns it off.
			name: "auto_maximize_chat_mobile",
			set:  func(c *types.Config, v *bool) { c.AutoMaximizeChatMobile = v },
			get:  func(c *types.Config) *bool { return c.AutoMaximizeChatMobile },
		},
		{
			// Unset is "never asked", not "off": a stored answer must not dissolve back into
			// never-asked, and only the round-trip rows tell them apart.
			name: "update_check_enabled",
			set:  func(c *types.Config, v *bool) { c.UpdateCheckEnabled = v },
			get:  func(c *types.Config) *bool { return c.UpdateCheckEnabled },
		},
	}

	for _, f := range flags {
		t.Run(f.name+" is unset and omitted from the response", func(t *testing.T) {
			cs := testConfigStore(t)
			cfg, err := cs.GetConfig()
			if err != nil {
				t.Fatalf("get config: %v", err)
			}
			if got := f.get(cfg); got != nil {
				t.Fatalf("expected nil %s when unset, got %v", f.name, *got)
			}
			b, err := json.Marshal(cs.buildConfigResponse(cfg))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// Match the JSON key, not the bare name: the temp database path in this very
			// response contains the subtest name, which mentions the key.
			if strings.Contains(string(b), `"`+f.name+`":`) {
				t.Fatalf("expected %q omitted from the response when nil, got %s", f.name, b)
			}
			if f.accessor != nil && f.accessor(cs) {
				t.Errorf("accessor reported true while %s is unset", f.name)
			}
		})

		t.Run(f.name+" round-trips true and an explicit false", func(t *testing.T) {
			cs := testConfigStore(t)
			// true → false → true also proves an explicit value overwrites the previous one.
			for _, want := range []bool{true, false, true} {
				cfg, err := cs.GetConfig()
				if err != nil {
					t.Fatalf("get config: %v", err)
				}
				f.set(cfg, ptrBool(want))
				if err := cs.UpdateConfig(cfg); err != nil {
					t.Fatalf("update config: %v", err)
				}

				read, err := cs.GetConfig()
				if err != nil {
					t.Fatalf("get config: %v", err)
				}
				got := f.get(read)
				if got == nil {
					t.Fatalf("%s=%t came back as nil (unset) — it must stay present", f.name, want)
				}
				if *got != want {
					t.Fatalf("%s = %t, want %t", f.name, *got, want)
				}

				b, err := json.Marshal(cs.buildConfigResponse(read))
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				if wantJSON := `"` + f.name + `":` + strconv.FormatBool(want); !strings.Contains(string(b), wantJSON) {
					t.Fatalf("expected %s in the response, got %s", wantJSON, b)
				}
				if f.accessor != nil && f.accessor(cs) != want {
					t.Errorf("accessor = %t, want %t for %s", f.accessor(cs), want, f.name)
				}
			}
		})
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

	// Persisted agent:// rows are pruned (registry-derived) — a persisted
	// non-agent:// agent-kind provider is still found.
	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
			{Name: "my-agent", Kind: "agent", URL: "agent://my-agent", Concurrency: 1},
			{Name: "claude-https", Kind: "claude", URL: "https://api.claude.com/v1", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	if p := cs.GetAgentProvider(); p == nil || p.Name != "claude-https" {
		t.Errorf("expected persisted agent-kind provider claude-https, got %v", p)
	}

	// A registry agent is returned when no persisted agent-kind row exists,
	// and persisted rows take precedence over the registry.
	cs.SetAgentRegistry(&mockAgentLister{
		providers: []types.Provider{
			{Name: "pi", Kind: "agent", DisplayName: "Pi Coding Agent", URL: "agent://pi", Concurrency: 1},
		},
	})
	if p := cs.GetAgentProvider(); p == nil || p.Name != "claude-https" {
		t.Errorf("expected persisted provider to win over registry, got %v", p)
	}

	// Remove the persisted agent-kind row — the registry agent is returned.
	cfg.Providers = cfg.Providers[:1]
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	if p := cs.GetAgentProvider(); p == nil || p.Name != "pi" {
		t.Errorf("expected registry agent pi, got %v", p)
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
		// Every provider in the response must have a non-nil Models slice so
		// the JSON is "models": [] (not null).
		if p.Models == nil {
			t.Errorf("provider %s: Models = nil, want non-nil empty slice", p.URL)
		}
	}
	if !found {
		t.Error("agent://pi provider not found in response")
	}

	// A persisted LLM provider with a nil models list (legacy row) must also
	// come back non-nil.
	seedLegacyProviderRows(t, cs, []types.Provider{
		{ID: 7, Name: "legacy", Kind: "llm", URL: "http://legacy:8080/v1", Concurrency: 1},
	})
	readCfg, err = cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	resp = cs.buildConfigResponse(readCfg)
	for _, p := range resp.Providers {
		if p.URL == "http://legacy:8080/v1" && p.Models == nil {
			t.Error("legacy provider: Models = nil, want non-nil empty slice")
		}
	}
}

// seedLegacyProviderRows writes raw provider JSON directly to the config
// store, simulating a pre-fix database that persisted agent:// rows.
func seedLegacyProviderRows(t *testing.T, cs *ConfigStore, providers []types.Provider) {
	t.Helper()
	raw, err := json.Marshal(providers)
	if err != nil {
		t.Fatalf("marshal providers: %v", err)
	}
	if err := cs.db.SetConfigValue("providers", string(raw)); err != nil {
		t.Fatalf("seed providers: %v", err)
	}
}

func TestGetConfigPrunesOrphanAgentProviders(t *testing.T) {
	cs := testConfigStore(t)

	// Legacy DB: an agent:// row persisted alongside LLM providers and a
	// non-agent:// agent-kind provider.
	seedLegacyProviderRows(t, cs, []types.Provider{
		{Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
		{Name: "pi", Kind: "agent", URL: "agent://pi", Concurrency: 1},
		{Name: "claude-https", Kind: "claude", URL: "https://api.claude.com/v1", Concurrency: 1},
	})

	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("expected 2 providers after pruning, got %d: %v", len(cfg.Providers), cfg.Providers)
	}
	for _, p := range cfg.Providers {
		if strings.HasPrefix(p.URL, "agent://") {
			t.Errorf("agent:// provider %q was not pruned", p.URL)
		}
	}
}

func TestUpdateConfigNeverPersistsAgentProviders(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			{Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
			{Name: "pi", Kind: "agent", URL: "agent://pi", Concurrency: 1, DisplayName: "Pi Coding Agent"},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if len(read.Providers) != 1 || read.Providers[0].URL != "http://localhost:8080/v1" {
		t.Fatalf("expected only the LLM provider to persist, got %v", read.Providers)
	}

	// The raw DB value must not contain the agent row either.
	raw, err := cs.db.GetConfigValue("providers")
	if err != nil {
		t.Fatalf("get raw providers: %v", err)
	}
	if strings.Contains(raw, "agent://") {
		t.Errorf("agent:// row persisted to database: %s", raw)
	}
}

func TestBuildConfigResponseExactAgentSet(t *testing.T) {
	cs := testConfigStore(t)

	// Legacy orphan agent row in the DB (agent extension no longer enabled).
	seedLegacyProviderRows(t, cs, []types.Provider{
		{Name: "llm", Kind: "llm", URL: "http://localhost:8080/v1", Concurrency: 1},
		{Name: "orphan", Kind: "agent", URL: "agent://orphan", Concurrency: 1},
	})

	// Registry provides only 'pi'.
	cs.SetAgentRegistry(&mockAgentLister{
		providers: []types.Provider{
			{Name: "pi", Kind: "agent", DisplayName: "Pi Coding Agent", URL: "agent://pi", Concurrency: 1},
		},
	})

	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	resp := cs.buildConfigResponse(cfg)

	urls := make([]string, 0, len(resp.Providers))
	for _, p := range resp.Providers {
		urls = append(urls, p.URL)
	}
	if len(resp.Providers) != 2 {
		t.Fatalf("expected 2 providers (LLM + registry agent), got %d: %v", len(resp.Providers), urls)
	}
	for _, u := range urls {
		if u == "agent://orphan" {
			t.Error("orphaned agent provider present in response")
		}
	}
}

func TestHandleConfigPutDropsAgentProviders(t *testing.T) {
	cs := testConfigStore(t)

	body, _ := json.Marshal(map[string]any{
		"providers": []map[string]any{
			{"name": "llm", "kind": "llm", "url": "http://localhost:8080/v1", "models": []any{map[string]any{"name": "model-a"}}, "concurrency": 1},
			// Agent rows carry no models; the client sends an empty list (Provider.UnmarshalJSON
			// requires the field to be present).
			{"name": "pi", "kind": "agent", "url": "agent://pi", "models": []any{}, "concurrency": 1, "display_name": "Pi Coding Agent"},
		},
		"worker_provider":  "llm",
		"worker_model":     "model-a",
		"utility_provider": "llm",
		"utility_model":    "model-a",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	cs.HandleConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Providers []types.Provider `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Providers) != 1 || resp.Providers[0].URL != "http://localhost:8080/v1" {
		t.Fatalf("expected only the LLM provider in response, got %v", resp.Providers)
	}

	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if len(read.Providers) != 1 {
		t.Fatalf("expected only the LLM provider persisted, got %v", read.Providers)
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
	cs, db := testConfigStoreWithStore(t)

	// Seed legacy config directly (bypassing UpdateConfig, which assigns IDs):
	// providers carry slug names and no IDs, worker/utility reference the slugs.
	legacyJSON := `[{"name":"neptune-8010","kind":"llm","url":"http://neptune:8010/v1","concurrency":1,"models":[]},{"name":"other-9000","kind":"llm","url":"http://other:9000/v1","concurrency":1,"models":[]}]`
	if err := cs.db.SetConfigValue("providers", legacyJSON); err != nil {
		t.Fatalf("seed legacy providers: %v", err)
	}
	if err := cs.db.SetConfigValue("worker_provider", "neptune-8010"); err != nil {
		t.Fatalf("seed worker_provider: %v", err)
	}
	if err := cs.db.SetConfigValue("utility_provider", "other-9000"); err != nil {
		t.Fatalf("seed utility_provider: %v", err)
	}
	// Simulate existing message/token rows attributed to the legacy slugs.
	db.Exec(`INSERT INTO projects (id, name, path, created_at, updated_at) VALUES ('p1', 'proj', '/tmp/proj', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`)
	db.Exec(`INSERT INTO tasks (id, project_id, title, prompt, llama_server_url, model_name, created_at, updated_at) VALUES ('t1', 'p1', 'task', 'prompt', '', '', '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`)
	db.Exec(`INSERT INTO task_messages (id, task_id, role, content, provider_name, created_at) VALUES ('m1', 't1', 'user', 'hi', 'neptune-8010', '2024-01-01T00:00:00Z')`)

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
	err = db.QueryRow(`SELECT provider_name FROM task_messages WHERE id = 'm1'`).Scan(&stored)
	if err != nil {
		t.Fatalf("query row: %v", err)
	}
	if stored != id {
		t.Errorf("task_messages provider_name not remapped: got %q want %q", stored, id)
	}
}

func TestBuildConfigResponseMasksAPIKeys(t *testing.T) {
	cs, db := testConfigStoreWithStore(t)

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
	// GetConfig hands out no key; the response masking below is a second line, not the only one.
	if len(readCfg.Providers) != 1 || readCfg.Providers[0].APIKey != nil {
		t.Fatalf("expected a provider with no APIKey, got %+v", readCfg.Providers)
	}
	// The key is still there for the one caller allowed to have it.
	if got := cs.GetProviderAPIKey("http://localhost:8080/v1"); got != "super-secret-key" {
		t.Fatalf("GetProviderAPIKey = %q, want the stored key", got)
	}

	// The HTTP response masks it.
	resp := cs.buildConfigResponse(readCfg)
	if len(resp.Providers) != 1 || resp.Providers[0].APIKey == nil {
		t.Fatalf("expected 1 provider with an APIKey field, got %+v", resp.Providers)
	}
	if got := *resp.Providers[0].APIKey; got != maskedAPIKey {
		t.Fatalf("API key not masked in response: got %q, want %q", got, maskedAPIKey)
	}
	// Neither the plaintext nor the stored ciphertext may appear anywhere in the response body.
	sealed := firstStoredKey(t, db)
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	for _, leak := range []string{"super-secret-key", sealed} {
		if leak != "" && strings.Contains(string(body), leak) {
			t.Errorf("config response body leaks %q", leak)
		}
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

// TestGetProviderStreamIdleIsNoCeilingUnlessConfigured holds the default open: 0 means "wait the
// stream out", and a default number of seconds here would silently reintroduce the bound that
// fails a provider for composing a long answer.
func TestGetProviderStreamIdleIsNoCeilingUnlessConfigured(t *testing.T) {
	cs := testConfigStore(t)
	cfg := &types.Config{
		Providers: []types.Provider{
			{ID: 1, URL: "http://saturn:8100/v1", Concurrency: 1, StreamIdleSeconds: 45},
			{ID: 2, URL: "http://other:9000/v1", Concurrency: 1},
			{ID: 3, URL: "http://negative:9000/v1", Concurrency: 1, StreamIdleSeconds: -7},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	if got := cs.GetProviderStreamIdle("http://saturn:8100/v1"); got != 45*time.Second {
		t.Errorf("configured provider = %v, want 45s", got)
	}
	if got := cs.GetProviderStreamIdle("http://saturn:8100/v1/"); got != 45*time.Second {
		t.Errorf("trailing-slash lookup = %v, want 45s", got)
	}
	if got := cs.GetProviderStreamIdle("http://other:9000/v1"); got != 0 {
		t.Errorf("unset provider = %v, want 0 (no ceiling)", got)
	}
	if got := cs.GetProviderStreamIdle("http://unknown/v1"); got != 0 {
		t.Errorf("unknown URL = %v, want 0 (no ceiling)", got)
	}
	if got := cs.GetProviderStreamIdle("http://negative:9000/v1"); got != 0 {
		t.Errorf("negative value = %v, want it clamped to 0 like the rate limit is", got)
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

// TestKeyboardShortcuts_RoundTrip covers every binding through the same two states,
// because they all ride one JSON column and share the one bug worth guarding: an explicit
// "" (disabled) must come back as "", not as nil — nil means "unset", which would hand
// the user back the default binding they just switched off. hide_hints is a bool and is
// covered next; the all-nil normalization is pinned by
// TestKeyboardShortcuts_AllNilNormalizedToNil.
func TestKeyboardShortcuts_RoundTrip(t *testing.T) {
	bindings := []struct {
		name string
		// custom is a binding the frontend would never generate by default, so a
		// round-trip that silently fell back to the default would be caught.
		custom string
		set    func(ks *types.KeyboardShortcuts, v *string)
		get    func(ks *types.KeyboardShortcuts) *string
	}{
		{"create_task", "shift+k",
			func(ks *types.KeyboardShortcuts, v *string) { ks.CreateTask = v },
			func(ks *types.KeyboardShortcuts) *string { return ks.CreateTask }},
		{"create_task_submit", "shift+enter",
			func(ks *types.KeyboardShortcuts, v *string) { ks.CreateTaskSubmit = v },
			func(ks *types.KeyboardShortcuts) *string { return ks.CreateTaskSubmit }},
		{"open_terminal", "alt+shift+t",
			func(ks *types.KeyboardShortcuts, v *string) { ks.OpenTerminal = v },
			func(ks *types.KeyboardShortcuts) *string { return ks.OpenTerminal }},
		{"start_task", "shift+enter",
			func(ks *types.KeyboardShortcuts, v *string) { ks.StartTask = v },
			func(ks *types.KeyboardShortcuts) *string { return ks.StartTask }},
		{"toggle_fullscreen", "shift+alt+f",
			func(ks *types.KeyboardShortcuts, v *string) { ks.ToggleFullscreen = v },
			func(ks *types.KeyboardShortcuts) *string { return ks.ToggleFullscreen }},
		// A mouse binding (pseudo-key "click") rides the same column and the same
		// normalization as the keyboard ones.
		{"multi_select_click", "shift+click",
			func(ks *types.KeyboardShortcuts, v *string) { ks.MultiSelectClick = v },
			func(ks *types.KeyboardShortcuts) *string { return ks.MultiSelectClick }},
	}

	for _, b := range bindings {
		for _, mode := range []struct {
			label string
			value string
		}{{"custom binding", b.custom}, {"disabled", ""}} {
			t.Run(b.name+"/"+mode.label, func(t *testing.T) {
				cs := testConfigStore(t)
				cfg, err := cs.GetConfig()
				if err != nil {
					t.Fatalf("get config: %v", err)
				}

				// One binding per store: a lone field must survive on its own, which is
				// what the normalization step gets wrong.
				ks := &types.KeyboardShortcuts{}
				b.set(ks, ptr(mode.value))
				cfg.KeyboardShortcuts = ks
				if err := cs.UpdateConfig(cfg); err != nil {
					t.Fatalf("update config: %v", err)
				}

				read, err := cs.GetConfig()
				if err != nil {
					t.Fatalf("get config: %v", err)
				}
				if read.KeyboardShortcuts == nil {
					t.Fatalf("%s=%q vanished: KeyboardShortcuts normalized to nil", b.name, mode.value)
				}
				got := b.get(read.KeyboardShortcuts)
				if got == nil {
					t.Fatalf("%s came back nil, want %q", b.name, mode.value)
				}
				if *got != mode.value {
					t.Errorf("%s = %q, want %q", b.name, *got, mode.value)
				}
			})
		}
	}
}

// TestKeyboardShortcutsHideHints covers the one boolean binding. Both values must come
// back present: hide_hints=false is a real preference (show the hints), so normalizing
// the struct to nil because it carries no key binding would drop it from API responses.
func TestKeyboardShortcutsHideHints(t *testing.T) {
	cs := testConfigStore(t)
	for _, want := range []bool{true, false} {
		cfg, err := cs.GetConfig()
		if err != nil {
			t.Fatalf("get config: %v", err)
		}
		cfg.KeyboardShortcuts = &types.KeyboardShortcuts{HideHints: ptrBool(want)}
		if err := cs.UpdateConfig(cfg); err != nil {
			t.Fatalf("update config: %v", err)
		}

		read, err := cs.GetConfig()
		if err != nil {
			t.Fatalf("get config: %v", err)
		}
		if read.KeyboardShortcuts == nil || read.KeyboardShortcuts.HideHints == nil {
			t.Fatalf("hide_hints=%t vanished (KeyboardShortcuts=%+v)", want, read.KeyboardShortcuts)
		}
		if *read.KeyboardShortcuts.HideHints != want {
			t.Errorf("hide_hints = %t, want %t", *read.KeyboardShortcuts.HideHints, want)
		}
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

// putConfig drives PUT /api/config with each body in order, failing if any is refused. The
// sequences matter: the second PUT is what shows whether a field was reset or preserved.
func putConfig(t *testing.T, cs *ConfigStore, bodies ...string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.HandleConfig(w, r)
	}))
	defer srv.Close()

	for _, body := range bodies {
		req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(body))
		if err != nil {
			t.Fatalf("build PUT request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("put config: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("PUT /api/config %s: status = %d, want 200", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestHandleConfig_KeyboardShortcutsPut covers the three merge rules the PUT handler
// implements for the shortcuts object, from the stored side:
//
//   - a binding is kept as sent, including the empty string, which means "disabled";
//   - a binding ABSENT from the PUT body is reset to its default (nil), because the SPA
//     always sends the full set — a partial body is a reset, not a patch;
//   - so "c" then "" must land on disabled, not on the default.
func TestHandleConfig_KeyboardShortcutsPut(t *testing.T) {
	cases := []struct {
		name   string
		bodies []string
		check  func(t *testing.T, ks *types.KeyboardShortcuts)
	}{
		{
			name:   "custom binding is stored as sent",
			bodies: []string{`{"keyboard_shortcuts":{"create_task":"shift+k"}}`},
			check: func(t *testing.T, ks *types.KeyboardShortcuts) {
				requireBinding(t, "create_task", ks, ptr("shift+k"))
			},
		},
		{
			name: "empty string disables rather than unsetting",
			bodies: []string{
				`{"keyboard_shortcuts":{"create_task":"c"}}`,
				`{"keyboard_shortcuts":{"create_task":""}}`,
			},
			check: func(t *testing.T, ks *types.KeyboardShortcuts) {
				requireBinding(t, "create_task", ks, ptr(""))
			},
		},
		{
			name: "bindings missing from the body reset to default",
			bodies: []string{
				`{"keyboard_shortcuts":{"create_task":"shift+k","create_task_submit":"shift+enter","open_terminal":"alt+shift+t"}}`,
				`{"keyboard_shortcuts":{"create_task":"c"}}`,
			},
			check: func(t *testing.T, ks *types.KeyboardShortcuts) {
				requireBinding(t, "create_task", ks, ptr("c"))
				requireBinding(t, "create_task_submit", ks, nil)
				requireBinding(t, "open_terminal", ks, nil)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := testConfigStore(t)
			putConfig(t, cs, tc.bodies...)

			read, err := cs.GetConfig()
			if err != nil {
				t.Fatalf("get config: %v", err)
			}
			if read.KeyboardShortcuts == nil {
				t.Fatalf("shortcuts vanished after PUT %v", tc.bodies)
			}
			tc.check(t, read.KeyboardShortcuts)
		})
	}
}

// requireBinding asserts a binding's stored state: want nil means "unset" (absent from the
// object), want non-nil means present with that exact value — including "".
func requireBinding(t *testing.T, name string, ks *types.KeyboardShortcuts, want *string) {
	t.Helper()

	fields := map[string]*string{
		"create_task":        ks.CreateTask,
		"create_task_submit": ks.CreateTaskSubmit,
		"open_terminal":      ks.OpenTerminal,
		"start_task":         ks.StartTask,
		"toggle_fullscreen":  ks.ToggleFullscreen,
	}
	got, ok := fields[name]
	if !ok {
		t.Fatalf("unknown binding %q", name)
	}
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %q, want unset (default)", name, *got)
	case want != nil && got == nil:
		t.Errorf("%s is unset, want %q", name, *want)
	case want != nil && *got != *want:
		t.Errorf("%s = %q, want %q", name, *got, *want)
	}
}

// TestHandleConfig_KeyboardShortcutsWireFormat covers what the SPA actually reads back after
// a partial PUT, which the stored-side table above cannot see:
//
//   - the bindings it sent come back by their JSON names;
//   - a binding it did not send is OMITTED from the object (not null, not ""), so the client
//     falls through to its own default instead of rendering an empty key;
//   - touching keyboard_shortcuts must not clobber the rest of the config — the seeded
//     provider and worker pair have to survive, because PUT is a partial update.
func TestHandleConfig_KeyboardShortcutsWireFormat(t *testing.T) {
	cs := testConfigStore(t)
	putConfig(t, cs,
		`{"providers":[{"id":1,"url":"http://neptune:8010/v1","concurrency":1,"models":[]}],"worker_provider":"1","worker_model":"m1","utility_provider":"1","utility_model":"m1"}`,
		`{"keyboard_shortcuts":{"create_task":"control+k","create_task_submit":"meta+enter"}}`)

	srv := httptest.NewServer(http.HandlerFunc(cs.HandleConfig))
	defer srv.Close()

	getResp, err := http.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer getResp.Body.Close()

	var got struct {
		KeyboardShortcuts *struct {
			CreateTask       *string `json:"create_task"`
			CreateTaskSubmit *string `json:"create_task_submit"`
			StartTask        *string `json:"start_task"`
		} `json:"keyboard_shortcuts"`
		WorkerProvider string `json:"worker_provider"`
	}
	body, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode get config: %v", err)
	}

	if got.KeyboardShortcuts == nil {
		t.Fatalf("keyboard_shortcuts missing from the response: %s", body)
	}
	requireBinding(t, "create_task", &types.KeyboardShortcuts{CreateTask: got.KeyboardShortcuts.CreateTask}, ptr("control+k"))
	requireBinding(t, "create_task_submit", &types.KeyboardShortcuts{CreateTaskSubmit: got.KeyboardShortcuts.CreateTaskSubmit}, ptr("meta+enter"))
	requireBinding(t, "start_task", &types.KeyboardShortcuts{StartTask: got.KeyboardShortcuts.StartTask}, nil)
	if strings.Contains(string(body), `"start_task"`) {
		t.Errorf("unset binding must be omitted from the object, not sent as null: %s", body)
	}
	if got.WorkerProvider != "1" {
		t.Errorf("worker_provider = %q after a shortcuts-only PUT, want \"1\" (partial update must not clobber)", got.WorkerProvider)
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

// TestWorkspaceDirMatchesGetConfig: the cheap accessor fs-path checks use must
// agree with the value GetConfig publishes, or the fs handlers would jail paths to
// a different directory than the one the app shows and writes to.
func TestWorkspaceDirMatchesGetConfig(t *testing.T) {
	for _, override := range []string{"", t.TempDir()} {
		cs := testConfigStore(t)
		if override != "" {
			cs.SetWorkspaceDirOverride(override)
		}
		read, err := cs.GetConfig()
		if err != nil {
			t.Fatalf("get config: %v", err)
		}
		got := cs.WorkspaceDir()
		if got != read.WorkspaceDir {
			t.Errorf("WorkspaceDir() = %q, GetConfig().WorkspaceDir = %q (override %q)", got, read.WorkspaceDir, override)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("WorkspaceDir() = %q, want absolute — callers compare it against absolute paths", got)
		}
	}
}

// TestWorkspaceDirRelativeOverrideIsAbsolute: a relative override is made
// absolute, because callers compare it against absolute paths.
func TestWorkspaceDirRelativeOverrideIsAbsolute(t *testing.T) {
	cs := testConfigStore(t)
	cs.SetWorkspaceDirOverride(".relative-worktrees")
	got := cs.WorkspaceDir()
	want, _ := filepath.Abs(".relative-worktrees")
	if got != want {
		t.Errorf("WorkspaceDir() = %q, want %q", got, want)
	}
}

// About reads the ceiling the gate was started with, so the operator can see what every sandbox MIKE
// started has to share. A store that was never told answers 0 rather than inventing a figure.
func TestConfigResponseReportsTheMemoryBudget(t *testing.T) {
	marshal := func(t *testing.T, cs *ConfigStore) map[string]any {
		t.Helper()
		cfg, err := cs.GetConfig()
		if err != nil {
			t.Fatalf("get config: %v", err)
		}
		raw, err := json.Marshal(cs.buildConfigResponse(cfg))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return out
	}

	t.Run("unset answers zero", func(t *testing.T) {
		out := marshal(t, testConfigStore(t))
		if out["memory_budget_mb"] != float64(0) || out["memory_total_mb"] != float64(0) || out["memory_budget_pct"] != float64(0) {
			t.Errorf("a store never told the budget reads %v/%v/%v, want 0/0/0",
				out["memory_budget_mb"], out["memory_total_mb"], out["memory_budget_pct"])
		}
	})

	t.Run("the startup decision is reported", func(t *testing.T) {
		cs := testConfigStore(t)
		cs.SetMemoryBudget(13624, 15138, 90)
		out := marshal(t, cs)
		if out["memory_budget_mb"] != float64(13624) || out["memory_total_mb"] != float64(15138) || out["memory_budget_pct"] != float64(90) {
			t.Errorf("budget read %v/%v/%v, want 13624/15138/90",
				out["memory_budget_mb"], out["memory_total_mb"], out["memory_budget_pct"])
		}
	})
}

// stubToolLister stands in for the extension tool registry, which is wired at startup.
type stubToolLister struct{ list []types.NetworkedExtension }

func (s stubToolLister) NetworkedExtensions() []types.NetworkedExtension { return s.list }

// The list is derived from what is installed, so it is served and ignored on write.
func TestConfigResponseServerNetworkExtensionsIsServedAndNeverWritten(t *testing.T) {
	cs := testConfigStore(t)
	cs.SetServerToolLister(stubToolLister{list: []types.NetworkedExtension{
		{Name: "contrib-searxng", DisplayName: "SearXNG Search", Tools: []string{"web_search"}},
	}})

	read := func() []types.NetworkedExtension {
		t.Helper()
		w := httptest.NewRecorder()
		cs.HandleConfig(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		var out struct {
			ServerNetworkExtensions []types.NetworkedExtension `json:"server_network_extensions"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return out.ServerNetworkExtensions
	}

	got := read()
	if len(got) != 1 || got[0].DisplayName != "SearXNG Search" || len(got[0].Tools) != 1 || got[0].Tools[0] != "web_search" {
		t.Fatalf("served %v, want the extension and its tool", got)
	}

	body := `{"server_network_extensions":[{"name":"injected","display_name":"Injected","tools":["nope"]}]}`
	w := httptest.NewRecorder()
	cs.HandleConfig(w, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("a PUT naming a derived key was refused: %d %s", w.Code, w.Body.String())
	}
	if after := read(); len(after) != 1 || after[0].Name != "contrib-searxng" {
		t.Errorf("a PUT wrote the derived list: %v", after)
	}
}

// No extension tool registry means no notice to show, so the key is absent rather than empty.
func TestConfigResponseOmitsServerNetworkExtensionsWithoutARegistry(t *testing.T) {
	w := httptest.NewRecorder()
	cs := testConfigStore(t)
	cs.HandleConfig(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["server_network_extensions"]; ok {
		t.Errorf("the key is served with nothing wired: %v", out["server_network_extensions"])
	}
}
