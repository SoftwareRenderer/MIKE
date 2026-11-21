package config

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
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
	return NewConfigStore(db, dir+"/test.db", "test", sharedSecretBox(), false, false), db
}

// TestAutoSummarizeThreshold reads the one Settings value both summarization triggers share: unset is
// the compiled-in default, a stored value is sent as stored, and a stored value outside the range reads
// as unset — which is why the PUT clamps rather than refusing, keeping what the UI displays equal to what
// gets stored.
func TestAutoSummarizeThreshold(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store *int
		want  int
	}{
		{name: "unset is the default", want: 90},
		{name: "a stored value is sent as stored", store: intPtr(60), want: 60},
		{name: "a stored value outside the range reads as unset", store: intPtr(0), want: 90},
		{name: "a stored value above the range reads as unset", store: intPtr(101), want: 90},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := testConfigStore(t)
			if tc.store != nil {
				cfg, err := cs.GetConfig()
				if err != nil {
					t.Fatalf("get config: %v", err)
				}
				cfg.DefaultAutoSummarizeThreshold = tc.store
				if err := cs.UpdateConfig(cfg); err != nil {
					t.Fatalf("update config: %v", err)
				}
			}
			if got := cs.AutoSummarizeThreshold(); got != tc.want {
				t.Fatalf("AutoSummarizeThreshold() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestUIFlagPreferences covers every *bool preference in the config in one place,
// because they share one storage rule and its three failure modes:
//
//   - unset must stay nil, so /api/config OMITS the key and the client default applies;
//   - true must round-trip;
//   - an explicit false must come back PRESENT: collapsing it to nil would quietly restore the
//     behaviour the user just switched off.
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
			// Reasoning streams as it is written until a person asks for the collapsed chat.
			name:     "collapse_reasoning",
			set:      func(c *types.Config, v *bool) { c.CollapseReasoning = v },
			get:      func(c *types.Config) *bool { return c.CollapseReasoning },
			accessor: func(cs *ConfigStore) bool { return cs.GetCollapseReasoning() },
		},
		{
			// Off until a person turns it on.
			name:     "reclaim_idle_sandbox",
			set:      func(c *types.Config, v *bool) { c.ReclaimIdleSandbox = v },
			get:      func(c *types.Config) *bool { return c.ReclaimIdleSandbox },
			accessor: func(cs *ConfigStore) bool { return cs.GetReclaimIdleSandbox() },
		},
		{
			// A run that hit its failed-turn limit is handed back until this is on.
			name:     "loop_breaker",
			set:      func(c *types.Config, v *bool) { c.LoopBreaker = v },
			get:      func(c *types.Config) *bool { return c.LoopBreaker },
			accessor: func(cs *ConfigStore) bool { return cs.GetLoopBreaker() },
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

// TestWorkspaceDirIsTheConfiguredDirectory is the jail base every fs path check compares against. It is
// the configured directory, made absolute because callers compare it against absolute paths, and the same
// answer on both read paths: GetConfig publishes it to the board and WorkspaceDir() is what the fs-path
// checks ask for, so one drifting from the other jails paths to a directory the app neither shows nor
// writes to.
func TestWorkspaceDirIsTheConfiguredDirectory(t *testing.T) {
	defaultAbs, err := filepath.Abs(DefaultWorkspaceDir)
	if err != nil {
		t.Fatalf("abs %q: %v", DefaultWorkspaceDir, err)
	}
	relative := ".relative-worktrees"
	relativeAbs, err := filepath.Abs(relative)
	if err != nil {
		t.Fatalf("abs %q: %v", relative, err)
	}

	for _, tc := range []struct {
		name string
		// override hands SetWorkspaceDirOverride the directory a deployment would point the server at,
		// and the answer both readers must give. An empty set leaves the override unset, as startup does
		// when no flag or variable names a directory.
		override func(t *testing.T) (set, want string)
	}{
		{name: "unset is the compiled-in default", override: func(*testing.T) (string, string) {
			return "", defaultAbs
		}},
		{name: "an absolute override is the directory", override: func(t *testing.T) (string, string) {
			dir := t.TempDir()
			return dir, dir
		}},
		{name: "a relative override is made absolute", override: func(*testing.T) (string, string) {
			return relative, relativeAbs
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, want := tc.override(t)
			cs := testConfigStore(t)
			if set != "" {
				cs.SetWorkspaceDirOverride(set)
			}

			read, err := cs.GetConfig()
			if err != nil {
				t.Fatalf("get config: %v", err)
			}
			if read.WorkspaceDir != want {
				t.Errorf("GetConfig().WorkspaceDir = %q, want %q", read.WorkspaceDir, want)
			}
			if got := cs.WorkspaceDir(); got != want {
				t.Errorf("WorkspaceDir() = %q, want %q — the two readers must agree", got, want)
			}
			if !filepath.IsAbs(cs.WorkspaceDir()) {
				t.Errorf("WorkspaceDir() = %q, want absolute — callers compare it against absolute paths", cs.WorkspaceDir())
			}
		})
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

// TestBuildConfigResponseServesTheRegistryAgentSet: the provider list a browser sees is the stored LLM
// rows plus exactly the agents the enabled extensions report — merged in with their display names, with
// an orphan left behind by a disabled extension absent, and with every row carrying a models array rather
// than null, which is what a database written before an empty list was stored as [] holds no list for.
func TestBuildConfigResponseServesTheRegistryAgentSet(t *testing.T) {
	llm := types.Provider{Name: "llm", Kind: types.ProviderKindLLM, URL: "http://localhost:8080/v1", Concurrency: 1}
	pi := types.Provider{Name: "pi", Kind: types.ProviderKindAgent, DisplayName: "Pi Coding Agent", URL: "agent://pi", Concurrency: 1}

	for _, tc := range []struct {
		name string
		// stored saves the rows the normal path would; legacyRows writes raw JSON instead, standing
		// in for a database written before agent rows stopped being persisted.
		stored     []types.Provider
		legacyRows []types.Provider
		// registry is what the enabled agent extensions report; nil means none is wired at all.
		registry []types.Provider
		wantURLs []string
	}{
		{
			name:     "the stored rows alone when no registry is wired",
			stored:   []types.Provider{llm},
			wantURLs: []string{"http://localhost:8080/v1"},
		},
		{
			name:     "the registry agent is merged in beside them",
			stored:   []types.Provider{llm},
			registry: []types.Provider{pi},
			wantURLs: []string{"http://localhost:8080/v1", "agent://pi"},
		},
		{
			// The agent extension was disabled; its row is still in the database, and the registry
			// decides the set rather than the row.
			name:       "an orphan agent row never comes back",
			legacyRows: []types.Provider{llm, {Name: "orphan", Kind: types.ProviderKindAgent, URL: "agent://orphan", Concurrency: 1}},
			registry:   []types.Provider{pi},
			wantURLs:   []string{"http://localhost:8080/v1", "agent://pi"},
		},
		{
			name:       "a legacy row holding no models list is served as an array",
			legacyRows: []types.Provider{{ID: 7, Name: "legacy", Kind: types.ProviderKindLLM, URL: "http://legacy:8080/v1", Concurrency: 1}},
			wantURLs:   []string{"http://legacy:8080/v1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := testConfigStore(t)
			if tc.stored != nil {
				if err := cs.UpdateConfig(&types.Config{Providers: tc.stored}); err != nil {
					t.Fatalf("update config: %v", err)
				}
			}
			if tc.legacyRows != nil {
				seedLegacyProviderRows(t, cs, tc.legacyRows)
			}
			if tc.registry != nil {
				cs.SetAgentRegistry(&mockAgentLister{providers: tc.registry})
			}

			cfg, err := cs.GetConfig()
			if err != nil {
				t.Fatalf("get config: %v", err)
			}
			resp := cs.buildConfigResponse(cfg)

			urls := make([]string, 0, len(resp.Providers))
			for _, p := range resp.Providers {
				urls = append(urls, p.URL)
				// Every provider in the response must have a non-nil Models slice so the JSON is
				// "models": [] (not null).
				if p.Models == nil {
					t.Errorf("provider %s: Models = nil, want non-nil empty slice", p.URL)
				}
				if p.URL == "agent://pi" && p.DisplayName != "Pi Coding Agent" {
					t.Errorf("registry agent display name = %q, want %q", p.DisplayName, "Pi Coding Agent")
				}
			}
			slices.Sort(urls)
			want := slices.Clone(tc.wantURLs)
			slices.Sort(want)
			if !slices.Equal(urls, want) {
				t.Errorf("response providers = %v, want exactly %v", urls, want)
			}
		})
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

// TestAgentProviderRowsNeverReachStorage: an agent provider is derived from the enabled extensions, so
// whichever door it is offered at — UpdateConfig, or PUT /api/config from the browser — only the LLM row
// is stored, the raw row holds no trace of the agent, and the caller is served only that row. An
// agent:// row in the database is a provider that survives its extension being uninstalled.
func TestAgentProviderRowsNeverReachStorage(t *testing.T) {
	for _, tc := range []struct {
		name string
		// write saves the LLM row beside an agent row and returns the providers the caller was
		// served: the PUT response for the route, the config response for the direct call.
		write func(t *testing.T, cs *ConfigStore) []types.Provider
	}{
		{
			name: "UpdateConfig",
			write: func(t *testing.T, cs *ConfigStore) []types.Provider {
				cfg := &types.Config{
					Providers: []types.Provider{
						{Name: "llm", Kind: types.ProviderKindLLM, URL: "http://localhost:8080/v1", Concurrency: 1},
						{Name: "pi", Kind: types.ProviderKindAgent, URL: "agent://pi", Concurrency: 1, DisplayName: "Pi Coding Agent"},
					},
				}
				if err := cs.UpdateConfig(cfg); err != nil {
					t.Fatalf("update config: %v", err)
				}
				read, err := cs.GetConfig()
				if err != nil {
					t.Fatalf("get config: %v", err)
				}
				return cs.buildConfigResponse(read).Providers
			},
		},
		{
			name: "PUT /api/config",
			write: func(t *testing.T, cs *ConfigStore) []types.Provider {
				body, err := json.Marshal(map[string]any{
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
				if err != nil {
					t.Fatalf("marshal the PUT body: %v", err)
				}
				w := httptest.NewRecorder()
				cs.HandleConfig(w, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(string(body))))
				if w.Code != http.StatusOK {
					t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
				}
				var resp struct {
					Providers []types.Provider `json:"providers"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				return resp.Providers
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := testConfigStore(t)

			served := tc.write(t, cs)
			if len(served) != 1 || served[0].URL != "http://localhost:8080/v1" {
				t.Fatalf("the caller was served %v, want only the LLM provider", served)
			}
			read, err := cs.GetConfig()
			if err != nil {
				t.Fatalf("get config: %v", err)
			}
			if len(read.Providers) != 1 || read.Providers[0].URL != "http://localhost:8080/v1" {
				t.Fatalf("expected only the LLM provider to persist, got %v", read.Providers)
			}
			raw, err := cs.db.GetConfigValue("providers")
			if err != nil {
				t.Fatalf("get raw providers: %v", err)
			}
			if strings.Contains(raw, "agent://") {
				t.Errorf("an agent:// row reached the database: %s", raw)
			}
		})
	}
}

// A stored LLM row carries no display name: the browser's label for an LLM provider is its own name, so
// a name written into the row would be a second, staler label for the same provider. A registry agent's
// label is served from the registry instead, and its row is never stored at all — see
// TestBuildConfigResponseServesTheRegistryAgentSet.
func TestDisplayNameNotPersistedForLLM(t *testing.T) {
	cs := testConfigStore(t)

	cfg := &types.Config{
		Providers: []types.Provider{
			// Even if a caller supplies a display name for an LLM provider, it must not be persisted.
			{Name: "llm", Kind: "llm", DisplayName: "Stale Display", URL: "http://localhost:8080/v1", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	read, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if len(read.Providers) != 1 {
		t.Fatalf("stored %d providers, want the one LLM row: %+v", len(read.Providers), read.Providers)
	}
	for _, p := range read.Providers {
		if p.DisplayName != "" {
			t.Errorf("LLM provider %q kept display name %q, want it cleared", p.Name, p.DisplayName)
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
	if err := json.UnmarshalRead(getResp.Body, &got); err != nil {
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

// TestKeyboardShortcutsWithNoBindingReadsAsNil: the row holds the shortcuts object or nothing, and both
// ways of having no bindings read the same way — a store nobody wrote, and an object with every field nil
// saved into one. A stored all-default object would answer "keyboard_shortcuts": {} and hand the user an
// empty binding where they asked for the defaults.
func TestKeyboardShortcutsWithNoBindingReadsAsNil(t *testing.T) {
	for _, tc := range []struct {
		name string
		save bool
	}{
		{name: "never written"},
		{name: "an all-nil object saved", save: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := testConfigStore(t)
			if tc.save {
				cfg, err := cs.GetConfig()
				if err != nil {
					t.Fatalf("get config: %v", err)
				}
				cfg.KeyboardShortcuts = &types.KeyboardShortcuts{}
				if err := cs.UpdateConfig(cfg); err != nil {
					t.Fatalf("update config: %v", err)
				}
			}
			read, err := cs.GetConfig()
			if err != nil {
				t.Fatalf("get config: %v", err)
			}
			if read.KeyboardShortcuts != nil {
				t.Fatalf("expected nil KeyboardShortcuts with no binding set, got %+v", read.KeyboardShortcuts)
			}
		})
	}
}

// TestKeyboardShortcuts_RoundTrip covers every binding through the same two states,
// because they all ride one JSON column and share the one bug worth guarding: an explicit
// "" (disabled) must come back as "", not as nil — nil means "unset", which would hand
// the user back the default binding they just switched off. hide_hints is a bool and is
// covered next; the all-nil normalization is pinned by
// TestKeyboardShortcutsWithNoBindingReadsAsNil.
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
	var got map[string]jsontext.Value
	if err := json.UnmarshalRead(resp.Body, &got); err != nil {
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
