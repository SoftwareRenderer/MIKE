// Package config owns the effective server configuration: the config table, the two
// provider/model pairs that route work (worker_* for chat, utility_* for titles, summaries
// and commit messages), and the environment defaults used where a row is absent.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"mike/backend/crypto"
	"mike/backend/httputil"
	"mike/backend/llmadapter"
	"mike/backend/store"
	"mike/backend/types"
	"mike/pkg/logging"
)

// AgentProviderLister provides agent provider entries discovered from extensions.
// The agent.AgentRegistry satisfies this interface (ToProviders() []types.Provider).
// Defined as an interface to avoid a circular import between config and agent packages.
type AgentProviderLister interface {
	ToProviders() []types.Provider
}

// ServerToolLister is satisfied by extensions.ToolRegistry; the interface keeps config from importing it.
// The list is served with the configuration and never stored: what is installed is not a preference.
type ServerToolLister interface {
	NetworkedExtensions() []types.NetworkedExtension
}

// DefaultWorkspaceDir is the default git worktrees directory when no
// --worktrees-dir flag or MIKE_WORKTREES_DIR env var is provided.
const DefaultWorkspaceDir = "./.worktrees"

// DefaultRegistryURL is the hosted extension registry index, used when MIKE_REGISTRY_URL names
// another index (a local fixture, an offline mirror).
const DefaultRegistryURL = "https://registry.getmike.dev/registry.json"

// SetRegistryURL wires the resolved registry index (startup.Config.RegistryURL) into the store.
// Wiring-time, before the server serves: the index is sourced exclusively from that variable and is
// never persisted to the database, so a per-call environment read was the only other way to learn it.
func (cs *ConfigStore) SetRegistryURL(url string) {
	if url == "" {
		url = DefaultRegistryURL
	}
	cs.registryURL = url
}

// RegistryURL returns the extension registry index URL this store was wired with. It is no longer
// persisted to the database.
func (cs *ConfigStore) RegistryURL() string {
	if cs.registryURL == "" {
		return DefaultRegistryURL
	}
	return cs.registryURL
}

// configDB is config's whole persistence need: its own key/value table, plus the typed
// remap that rewrites provider attribution in tables the store owns.
type configDB interface {
	GetConfigValue(key string) (string, error)
	GetConfigValues(keys ...string) (map[string]string, error)
	SetConfigValue(key, value string) error
	RemapProviderNames(oldSlugToID map[string]string) error
	// ConfigGeneration counts config-table writes, so a cached config can tell stale from current
	// without re-reading.
	ConfigGeneration() uint64
}

var _ configDB = (*store.Store)(nil)

// ConfigStore reads and writes the server configuration.
type ConfigStore struct {
	db                   configDB
	dbPath               string
	version              string
	agentRegistry        AgentProviderLister // optional — set via SetAgentRegistry
	serverTools          ServerToolLister    // optional — set via SetServerToolLister
	workspaceDirOverride string              // set at startup via --worktrees-dir / MIKE_WORKTREES_DIR
	llmAdapter           *llmadapter.Adapter // optional — set via SetLLMAdapter
	tlsCAAvailable       bool                // set at startup via SetTLSCAAvailable
	registryURL          string              // set at startup via SetRegistryURL; empty means DefaultRegistryURL
	// The memory budget the gate was given, set at startup via SetMemoryBudget: the ceiling in MB, the
	// installed RAM in MB it was computed from, and the configured percentage. Facts about the running
	// process, never stored preferences.
	memoryBudgetMB  int64
	memoryTotalMB   int64
	memoryBudgetPct int

	// box opens stored provider keys. Built once at startup: a derivation costs hundreds of
	// milliseconds, so no read path may pay one.
	box *crypto.Box

	snapMu sync.Mutex
	snap   *configSnapshot
}

// configSnapshot is the read-side cache. It holds providers exactly as stored, so no plaintext
// secret sits in it.
type configSnapshot struct {
	generation uint64
	rows       map[string]string
	providers  []types.Provider
}

// SetLLMAdapter helps map outgoing LLM API requests to the provider's expected format
func (cs *ConfigStore) SetLLMAdapter(a *llmadapter.Adapter) {
	cs.llmAdapter = a
}

// SetWorkspaceDirOverride sets the worktrees directory.
// Must be called before GetConfig().
func (cs *ConfigStore) SetWorkspaceDirOverride(dir string) {
	cs.workspaceDirOverride = dir
}

// SetTLSCAAvailable records that the server runs its default certificate, signed
// by the local CA — which is what makes that CA downloadable. A fact about the
// running process, never a stored preference.
func (cs *ConfigStore) SetTLSCAAvailable(available bool) {
	cs.tlsCAAvailable = available
}

// SetMemoryBudget records the memory ceiling the gate was started with, so the About tab can state
// what everything MIKE starts is allowed to occupy. A capacity of 0 means the gate is disabled.
func (cs *ConfigStore) SetMemoryBudget(capacityMB, totalMB int64, pct int) {
	cs.memoryBudgetMB = capacityMB
	cs.memoryTotalMB = totalMB
	cs.memoryBudgetPct = pct
}

// WorkspaceDir returns the effective worktrees directory: the startup override
// (--worktrees-dir / MIKE_WORKTREES_DIR) else the default, made absolute.
// GetConfig computes the same value, but reading the config row costs a dozen
// SQLite reads plus a provider JSON parse, so callers that only need this
// directory (fs-API containment, preview resolution) use this instead.
func (cs *ConfigStore) WorkspaceDir() string {
	dir := cs.workspaceDirOverride
	if dir == "" {
		dir = DefaultWorkspaceDir
	}
	// Resolve a relative path so callers can compare it against absolute ones.
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	return dir
}

// SetAgentRegistry wires the agent registry so dynamically-discovered agent
// providers (e.g. agent://pi → 'Pi Coding Agent') are included in the
// config API response without being persisted to the database.
func (cs *ConfigStore) SetAgentRegistry(reg AgentProviderLister) {
	cs.agentRegistry = reg
}

// agentProviders returns dynamically-discovered agent providers from the
// agent registry. Returns nil if no registry is wired.
func (cs *ConfigStore) agentProviders() []types.Provider {
	if cs.agentRegistry == nil {
		return nil
	}
	return cs.agentRegistry.ToProviders()
}

// SetServerToolLister wires the registry the config response reads the server-side tool extensions from.
func (cs *ConfigStore) SetServerToolLister(l ServerToolLister) {
	cs.serverTools = l
}

// filterPersistedProviders removes agent:// providers from the list so they
// are not persisted to the database. Agent providers are discovered dynamically
// at runtime from enabled agent extensions and should not be stored alongside
// user-configured LLM providers. Providers with a non-agent:// URL but a
// non-llm kind remain user-persistable.
func filterPersistedProviders(providers []types.Provider) []types.Provider {
	filtered := make([]types.Provider, 0, len(providers))
	for _, p := range providers {
		if types.IsAgentURL(p.URL) {
			continue
		}
		filtered = append(filtered, p)
	}
	return filtered
}

// assignProviderIDs assigns stable integer IDs to LLM providers that don't have one yet.
// IDs are assigned sequentially starting after the highest existing ID.
func assignProviderIDs(providers []types.Provider) {
	maxID := 0
	for _, p := range providers {
		if p.ID > maxID {
			maxID = p.ID
		}
	}
	next := maxID + 1
	for i := range providers {
		if providers[i].ID == 0 {
			providers[i].ID = next
			next++
		}
	}
}

// MigrateProviderIDs migrates legacy provider configs that stored the provider
// identity as a URL-derived slug name into the new stable integer ID scheme.
// It assigns IDs, remaps the worker/utility provider references and the
// provider_name rows in the message/token tables, and persists the new format.
// It is idempotent: once every persisted provider has an ID it does nothing.
func (cs *ConfigStore) MigrateProviderIDs() error {
	providersRaw, err := cs.db.GetConfigValue("providers")
	if err != nil {
		return err
	}
	providers := make([]types.Provider, 0)
	if providersRaw != "" {
		if err := json.Unmarshal([]byte(providersRaw), &providers); err != nil {
			return err
		}
	}
	needsMigration := false
	for _, p := range providers {
		if !types.IsAgentURL(p.URL) && p.ID == 0 {
			needsMigration = true
			break
		}
	}
	if !needsMigration {
		return nil
	}

	// Build a slug-name -> new-ID map from legacy providers that carry a name.
	slugToID := make(map[string]string)
	assignProviderIDs(providers)
	for i := range providers {
		p := &providers[i]
		if p.Name != "" && p.ID != 0 {
			slugToID[p.Name] = strconv.Itoa(p.ID)
		}
		p.Name = "" // slug is no longer persisted; identity is now the ID
	}

	// Remap worker/utility provider references (which stored the old slug).
	for _, key := range []string{"worker_provider", "utility_provider"} {
		raw, _ := cs.db.GetConfigValue(key)
		if id, ok := slugToID[raw]; ok {
			if err := cs.db.SetConfigValue(key, id); err != nil {
				return err
			}
		}
	}

	// Remap provider_name rows in the message/token tables from slug to ID. The rows are
	// the store's; config only says which slug means which ID.
	if err := cs.db.RemapProviderNames(slugToID); err != nil {
		return err
	}

	providersJSON, err := json.Marshal(providers)
	if err != nil {
		return err
	}
	return cs.db.SetConfigValue("providers", string(providersJSON))
}

// NewConfigStore creates a new ConfigStore. box comes from crypto.New at startup.
func NewConfigStore(s configDB, dbPath, version string, box *crypto.Box) *ConfigStore {
	abs, _ := filepath.Abs(dbPath)
	return &ConfigStore{db: s, dbPath: abs, version: version, box: box}
}

// configRowKeys are the rows a full config read wants, in one query.
var configRowKeys = []string{
	"worker_provider", "worker_model", "utility_provider", "utility_model",
	"default_auto_commit", "default_auto_merge", "default_subtasks_auto_merge",
	"default_operating_mode", "default_max_failed_turns", "default_auto_summarize_threshold",
	"hide_model_badge", "update_check_enabled", "run_siblings_parallel", "sort_oldest_first",
	"auto_maximize_chat_mobile", "keyboard_shortcuts",
}

func (cs *ConfigStore) snapshot() (*configSnapshot, error) {
	generation := cs.db.ConfigGeneration()
	cs.snapMu.Lock()
	defer cs.snapMu.Unlock()
	if cs.snap != nil && cs.snap.generation == generation {
		return cs.snap, nil
	}
	rows, err := cs.db.GetConfigValues(append(append([]string{}, configRowKeys...), "providers")...)
	if err != nil {
		return nil, err
	}
	providers := make([]types.Provider, 0)
	if raw := rows["providers"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &providers); err != nil {
			return nil, err
		}
	}
	cs.snap = &configSnapshot{generation: generation, rows: rows, providers: providers}
	return cs.snap, nil
}

// copyProviders returns a writable copy, so a caller patching a model's context window or dropping a
// secret cannot reach the cached row behind it.
func copyProviders(src []types.Provider) []types.Provider {
	out := make([]types.Provider, len(src))
	copy(out, src)
	for i := range out {
		if out[i].Models != nil {
			out[i].Models = append([]types.ModelConfig{}, out[i].Models...)
		}
	}
	return out
}

// storedProviders is the provider list as stored, secrets sealed. Every read-modify-write of that row
// goes through it, because GetConfig hands out no key and writing its result back would drop one.
func (cs *ConfigStore) storedProviders() ([]types.Provider, error) {
	snap, err := cs.snapshot()
	if err != nil {
		return nil, err
	}
	return copyProviders(snap.providers), nil
}

// GetConfig retrieves the full server configuration and hands out no plaintext secret: a provider's
// APIKey is absent, because this sits on the request path. Whoever authenticates an outbound call asks for
// that one key, by GetProviderAPIKey or ResolveAPIKey.
func (cs *ConfigStore) GetConfig() (*types.Config, error) {
	// Workspace dir is set at startup via CLI flag / env var — not persisted.
	// WorkspaceDir() is that same computation without the config-row reads.
	workspaceDir := cs.WorkspaceDir()
	snap, err := cs.snapshot()
	if err != nil {
		return nil, err
	}
	// Agent providers are derived from enabled agent extensions
	providers := filterPersistedProviders(copyProviders(snap.providers))
	for i := range providers {
		if providers[i].Kind != "agent" {
			providers[i].DisplayName = ""
		}
		providers[i].APIKey = nil
	}
	rows := snap.rows

	workerProvider := rows["worker_provider"]
	workerModel := rows["worker_model"]
	utilityProvider := rows["utility_provider"]
	utilityModel := rows["utility_model"]
	registryURL := cs.RegistryURL()
	defaultAutoCommitRaw := rows["default_auto_commit"]
	defaultAutoMergeRaw := rows["default_auto_merge"]
	defaultSubtasksAutoMergeRaw := rows["default_subtasks_auto_merge"]
	defaultOperatingModeRaw := rows["default_operating_mode"]
	defaultMaxFailedTurnsRaw := rows["default_max_failed_turns"]
	defaultAutoSummarizeThresholdRaw := rows["default_auto_summarize_threshold"]
	hideModelBadgeRaw := rows["hide_model_badge"]
	updateCheckEnabledRaw := rows["update_check_enabled"]
	runSiblingsParallelRaw := rows["run_siblings_parallel"]
	sortOldestFirstRaw := rows["sort_oldest_first"]
	autoMaximizeChatMobileRaw := rows["auto_maximize_chat_mobile"]
	keyboardShortcutsRaw := rows["keyboard_shortcuts"]

	cfg := &types.Config{
		WorkspaceDir:    workspaceDir,
		DatabasePath:    cs.dbPath,
		RegistryURL:     registryURL,
		Providers:       providers,
		WorkerProvider:  workerProvider,
		WorkerModel:     workerModel,
		UtilityProvider: utilityProvider,
		UtilityModel:    utilityModel,
	}
	if v, err := parseBoolPtr(defaultAutoCommitRaw); err == nil && v != nil {
		cfg.DefaultAutoCommit = v
	}
	if v, err := parseBoolPtr(defaultAutoMergeRaw); err == nil && v != nil {
		cfg.DefaultAutoMerge = v
	}
	if v, err := parseBoolPtr(defaultSubtasksAutoMergeRaw); err == nil && v != nil {
		cfg.DefaultSubtasksAutoMerge = v
	}
	if defaultOperatingModeRaw != "" {
		// A stored value is coerced rather than rejected: reading a row written by an older
		// build must not fail the read, and the only thing that can be done with a mode nothing
		// dispatches on is fall back. Writing one is a different story - see HandleConfig.
		if mode, valid := types.ParseOperatingMode(defaultOperatingModeRaw); valid {
			cfg.DefaultOperatingMode = &mode
		} else {
			act := types.ModeAct
			cfg.DefaultOperatingMode = &act
		}
	}
	if v, err := parseIntPtr(defaultMaxFailedTurnsRaw); err == nil && v != nil {
		cfg.DefaultMaxFailedTurns = v
	}
	if v, err := parseIntPtr(defaultAutoSummarizeThresholdRaw); err == nil && v != nil {
		cfg.DefaultAutoSummarizeThreshold = v
	}
	if keyboardShortcutsRaw != "" {
		ks := &types.KeyboardShortcuts{}
		if err := json.Unmarshal([]byte(keyboardShortcutsRaw), ks); err == nil {
			cfg.KeyboardShortcuts = ks
		}
	}
	if v, err := parseBoolPtr(hideModelBadgeRaw); err == nil && v != nil {
		cfg.HideModelBadge = v
	}
	if v, err := parseBoolPtr(updateCheckEnabledRaw); err == nil && v != nil {
		cfg.UpdateCheckEnabled = v
	}
	if v, err := parseBoolPtr(runSiblingsParallelRaw); err == nil && v != nil {
		cfg.RunSiblingsParallel = v
	}
	if v, err := parseBoolPtr(sortOldestFirstRaw); err == nil && v != nil {
		cfg.SortOldestFirst = v
	}
	if v, err := parseBoolPtr(autoMaximizeChatMobileRaw); err == nil && v != nil {
		cfg.AutoMaximizeChatMobile = v
	}
	cfg.KeyboardShortcuts = normalizeKeyboardShortcuts(cfg.KeyboardShortcuts)
	return cfg, nil
}

// normalizeKeyboardShortcuts returns nil when no binding is set, so the
// field is omitted from API responses.
func normalizeKeyboardShortcuts(ks *types.KeyboardShortcuts) *types.KeyboardShortcuts {
	if ks == nil || (ks.CreateTask == nil && ks.CreateTaskSubmit == nil && ks.OpenTerminal == nil && ks.StartTask == nil && ks.ToggleFullscreen == nil && ks.MultiSelectClick == nil && ks.HideHints == nil) {
		return nil
	}
	return ks
}

// UpdateConfig persists the full server configuration and updates the in-memory override if set.
// API keys are encrypted before DB storage.
func (cs *ConfigStore) UpdateConfig(c *types.Config) error {
	// Agent providers are registry-derived and must never be persisted.
	c.Providers = filterPersistedProviders(c.Providers)
	// Assign stable IDs to providers that don't have one yet (new providers).
	assignProviderIDs(c.Providers)
	storedKeys, err := cs.storedKeysByProviderID()
	if err != nil {
		return err
	}
	// Enforce minimum concurrency of 1, non-negative rate limit, and encrypt API keys
	for i := range c.Providers {
		if c.Providers[i].Concurrency < 1 {
			c.Providers[i].Concurrency = 1
		}
		if c.Providers[i].MaxRequestsPerSecond < 0 {
			c.Providers[i].MaxRequestsPerSecond = 0
		}
		if c.Providers[i].StreamIdleSeconds < 0 {
			c.Providers[i].StreamIdleSeconds = 0
		}
		// A value is a new key to seal, an omitted one or the masked placeholder keeps what is stored,
		// and an empty string clears it.
		switch {
		case c.Providers[i].APIKey == nil || *c.Providers[i].APIKey == maskedAPIKey:
			if key, ok := storedKeys[c.Providers[i].ID]; ok {
				c.Providers[i].APIKey = &key
			}
		case *c.Providers[i].APIKey == "":
			c.Providers[i].APIKey = nil
		default:
			if cs.box == nil {
				return fmt.Errorf("no encryption key configured: cannot store an API key for provider %q", c.Providers[i].Name)
			}
			encrypted, err := cs.box.Encrypt(providerKeyPurpose, strconv.Itoa(c.Providers[i].ID), *c.Providers[i].APIKey)
			if err != nil {
				return fmt.Errorf("failed to encrypt API key for provider %q: %w", c.Providers[i].Name, err)
			}
			c.Providers[i].APIKey = &encrypted
		}
		// Display names are derived dynamically from the URL on the frontend
		if !types.IsAgentURL(c.Providers[i].URL) && c.Providers[i].Kind != "agent" {
			c.Providers[i].DisplayName = ""
		}
	}
	providersJSON, err := json.Marshal(c.Providers)
	if err != nil {
		return err
	}
	if err := cs.db.SetConfigValue("providers", string(providersJSON)); err != nil {
		return err
	}
	if err := cs.db.SetConfigValue("worker_provider", c.WorkerProvider); err != nil {
		return err
	}
	if err := cs.db.SetConfigValue("worker_model", c.WorkerModel); err != nil {
		return err
	}
	if err := cs.db.SetConfigValue("utility_provider", c.UtilityProvider); err != nil {
		return err
	}
	if err := cs.db.SetConfigValue("utility_model", c.UtilityModel); err != nil {
		return err
	}
	if c.DefaultAutoCommit != nil {
		if err := cs.db.SetConfigValue("default_auto_commit", fmt.Sprintf("%t", *c.DefaultAutoCommit)); err != nil {
			return err
		}
	}
	if c.DefaultAutoMerge != nil {
		if err := cs.db.SetConfigValue("default_auto_merge", fmt.Sprintf("%t", *c.DefaultAutoMerge)); err != nil {
			return err
		}
	}
	if c.DefaultSubtasksAutoMerge != nil {
		if err := cs.db.SetConfigValue("default_subtasks_auto_merge", fmt.Sprintf("%t", *c.DefaultSubtasksAutoMerge)); err != nil {
			return err
		}
	}
	if c.DefaultOperatingMode != nil {
		if err := cs.db.SetConfigValue("default_operating_mode", string(*c.DefaultOperatingMode)); err != nil {
			return err
		}
	}
	if c.DefaultMaxFailedTurns != nil {
		if err := cs.db.SetConfigValue("default_max_failed_turns", fmt.Sprintf("%d", *c.DefaultMaxFailedTurns)); err != nil {
			return err
		}
	}
	if c.DefaultAutoSummarizeThreshold != nil {
		if err := cs.db.SetConfigValue("default_auto_summarize_threshold", fmt.Sprintf("%d", *c.DefaultAutoSummarizeThreshold)); err != nil {
			return err
		}
	}
	if c.HideModelBadge != nil {
		if err := cs.db.SetConfigValue("hide_model_badge", fmt.Sprintf("%t", *c.HideModelBadge)); err != nil {
			return err
		}
	}
	if c.UpdateCheckEnabled != nil {
		if err := cs.db.SetConfigValue("update_check_enabled", fmt.Sprintf("%t", *c.UpdateCheckEnabled)); err != nil {
			return err
		}
	}
	if c.RunSiblingsParallel != nil {
		if err := cs.db.SetConfigValue("run_siblings_parallel", fmt.Sprintf("%t", *c.RunSiblingsParallel)); err != nil {
			return err
		}
	}
	if c.AutoMaximizeChatMobile != nil {
		if err := cs.db.SetConfigValue("auto_maximize_chat_mobile", fmt.Sprintf("%t", *c.AutoMaximizeChatMobile)); err != nil {
			return err
		}
	}
	if c.SortOldestFirst != nil {
		if err := cs.db.SetConfigValue("sort_oldest_first", fmt.Sprintf("%t", *c.SortOldestFirst)); err != nil {
			return err
		}
	}
	if c.KeyboardShortcuts != nil {
		ksJSON, err := json.Marshal(c.KeyboardShortcuts)
		if err != nil {
			return err
		}
		if err := cs.db.SetConfigValue("keyboard_shortcuts", string(ksJSON)); err != nil {
			return err
		}
	}
	return nil
}

// configResponse wraps Config with additional metadata for API responses.
type configResponse struct {
	WorkspaceDir                  string                   `json:"workspace_dir"`
	DatabasePath                  string                   `json:"database_path"`
	Version                       string                   `json:"version"`
	Providers                     []types.Provider         `json:"providers"`
	WorkerProvider                string                   `json:"worker_provider"`
	WorkerModel                   string                   `json:"worker_model"`
	UtilityProvider               string                   `json:"utility_provider"`
	UtilityModel                  string                   `json:"utility_model"`
	DefaultAutoCommit             *bool                    `json:"default_auto_commit"`
	DefaultAutoMerge              *bool                    `json:"default_auto_merge"`
	DefaultSubtasksAutoMerge      *bool                    `json:"default_subtasks_auto_merge"`
	DefaultOperatingMode          *string                  `json:"default_operating_mode"`
	DefaultMaxFailedTurns         *int                     `json:"default_max_failed_turns,omitempty"`
	DefaultAutoSummarizeThreshold *int                     `json:"default_auto_summarize_threshold,omitempty"`
	HideModelBadge                *bool                    `json:"hide_model_badge,omitempty"`
	UpdateCheckEnabled            *bool                    `json:"update_check_enabled,omitempty"`
	RunSiblingsParallel           *bool                    `json:"run_siblings_parallel,omitempty"`
	SortOldestFirst               *bool                    `json:"sort_oldest_first,omitempty"`
	AutoMaximizeChatMobile        *bool                    `json:"auto_maximize_chat_mobile,omitempty"`
	KeyboardShortcuts             *types.KeyboardShortcuts `json:"keyboard_shortcuts,omitempty"`
	EncryptionEnabled             bool                     `json:"encryption_enabled"`
	// TLSCAAvailable tells the UI to offer the CA download (GET /api/tls/ca). The
	// CA's path is not published: the route already knows where it lives.
	TLSCAAvailable bool `json:"tls_ca_available"`
	// MemoryBudgetMB is the ceiling every sandbox MIKE started shares, MemoryTotalMB the installed RAM
	// it was computed from and MemoryBudgetPct the configured percentage. A budget of 0 means the gate
	// is disabled and the UI says nothing about memory.
	MemoryBudgetMB  int64 `json:"memory_budget_mb"`
	MemoryTotalMB   int64 `json:"memory_total_mb"`
	MemoryBudgetPct int   `json:"memory_budget_pct"`
	// The extensions whose contributed tools reach the network from this process. Read-only: a project's
	// isolation bounds its container, and these calls never enter it.
	ServerNetworkExtensions []types.NetworkedExtension `json:"server_network_extensions,omitempty"`
}

// storedAPIKey is the stored key of the provider whose URL matches serverURL, plus the record identity
// it was sealed under, or "" for none. A trailing slash is ignored on both sides.
func storedAPIKey(providers []types.Provider, serverURL string) (sealed, record string) {
	serverURL = strings.TrimRight(serverURL, "/")
	for _, p := range providers {
		if strings.TrimRight(p.URL, "/") == serverURL {
			if v := p.APIKeyValue(); v != "" {
				return v, strconv.Itoa(p.ID)
			}
			return "", ""
		}
	}
	return "", ""
}

// maskedAPIKey stands in for a stored key in a response. The browser omits it from what it sends back,
// and one that comes back reads as "unchanged".
const maskedAPIKey = "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022" // 10 bullets

// providerKeyPurpose is the record kind a provider API key is sealed under, so a value from an
// extension or another provider cannot open here.
const providerKeyPurpose = "provider"

// maskAPIKeys masks the providers that hold a stored key. That list comes from the row, not from the
// list being masked, which may be the keyless config view.
func maskAPIKeys(providers []types.Provider, storedKeys map[int]string) {
	for i := range providers {
		if _, ok := storedKeys[providers[i].ID]; ok {
			masked := maskedAPIKey
			providers[i].APIKey = &masked
		}
	}
}

// storedKeysByProviderID maps provider id to its stored secret, so a response can say a key is set
// without carrying it and a write can keep a key the payload did not repeat.
func (cs *ConfigStore) storedKeysByProviderID() (map[int]string, error) {
	snap, err := cs.snapshot()
	if err != nil {
		return nil, err
	}
	out := make(map[int]string, len(snap.providers))
	for _, p := range snap.providers {
		if v := p.APIKeyValue(); v != "" {
			out[p.ID] = v
		}
	}
	return out, nil
}

// buildConfigResponse constructs the API response from stored config, merging
// dynamically-discovered agent providers (from enabled agent extensions) into
// the providers list. API keys are masked before sending to the client.
func (cs *ConfigStore) buildConfigResponse(cfg *types.Config) configResponse {
	providers := cfg.Providers
	for _, ap := range cs.agentProviders() {
		found := false
		for _, ep := range providers {
			if ep.URL == ap.URL {
				found = true
				break
			}
		}
		if !found {
			providers = append(providers, ap)
		}
	}
	// Providers without a models list (legacy rows, agent providers from an
	// older server) must serialize as "models": [] rather than null — the
	// frontend assumes an array.
	for i := range providers {
		if providers[i].Models == nil {
			providers[i].Models = []types.ModelConfig{}
		}
	}
	// An unreadable row masks nothing: this list already carries no keys, so the worst case is a field
	// shown empty.
	stored, _ := cs.storedKeysByProviderID()
	maskAPIKeys(providers, stored)
	return configResponse{
		WorkspaceDir:                  cfg.WorkspaceDir,
		DatabasePath:                  cfg.DatabasePath,
		Version:                       cs.version,
		Providers:                     providers,
		WorkerProvider:                cfg.WorkerProvider,
		WorkerModel:                   cfg.WorkerModel,
		UtilityProvider:               cfg.UtilityProvider,
		UtilityModel:                  cfg.UtilityModel,
		DefaultAutoCommit:             cfg.DefaultAutoCommit,
		DefaultAutoMerge:              cfg.DefaultAutoMerge,
		DefaultSubtasksAutoMerge:      cfg.DefaultSubtasksAutoMerge,
		DefaultOperatingMode:          operatingModePtr(cfg.DefaultOperatingMode),
		DefaultMaxFailedTurns:         cfg.DefaultMaxFailedTurns,
		DefaultAutoSummarizeThreshold: cfg.DefaultAutoSummarizeThreshold,
		HideModelBadge:                cfg.HideModelBadge,
		UpdateCheckEnabled:            cfg.UpdateCheckEnabled,
		RunSiblingsParallel:           cfg.RunSiblingsParallel,
		SortOldestFirst:               cfg.SortOldestFirst,
		AutoMaximizeChatMobile:        cfg.AutoMaximizeChatMobile,
		KeyboardShortcuts:             normalizeKeyboardShortcuts(cfg.KeyboardShortcuts),
		EncryptionEnabled:             cs.box != nil,
		TLSCAAvailable:                cs.tlsCAAvailable,
		MemoryBudgetMB:                cs.memoryBudgetMB,
		MemoryTotalMB:                 cs.memoryTotalMB,
		MemoryBudgetPct:               cs.memoryBudgetPct,
		ServerNetworkExtensions:       cs.serverNetworkExtensions(),
	}
}

func (cs *ConfigStore) serverNetworkExtensions() []types.NetworkedExtension {
	if cs.serverTools == nil {
		return nil
	}
	return cs.serverTools.NetworkedExtensions()
}

// HandleConfig handles GET and PUT /api/config.
func (cs *ConfigStore) HandleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := cs.GetConfig()
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to get config")
			return
		}
		resp := cs.buildConfigResponse(cfg)
		httputil.JSONResponse(w, http.StatusOK, resp)
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "could not read request body")
			return
		}
		updates, err := newConfigUpdates(body)
		if err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		current, err := cs.GetConfig()
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to get current config")
			return
		}
		// Providers and keyboard shortcuts carry their own shape, so they decode straight into
		// their types; every other field is one of the three scalars this endpoint accepts.
		var incoming []types.Provider
		if updates.field("providers", &incoming) {
			// agent:// rows in the payload are dropped by UpdateConfig (agent providers
			// are registry-derived); registry agents are merged in buildConfigResponse.
			current.Providers = incoming
		}
		current.WorkerProvider = updates.strOr("worker_provider", current.WorkerProvider)
		current.WorkerModel = updates.strOr("worker_model", current.WorkerModel)
		current.UtilityProvider = updates.strOr("utility_provider", current.UtilityProvider)
		current.UtilityModel = updates.strOr("utility_model", current.UtilityModel)
		current.DefaultAutoCommit = updates.boolPtrOr("default_auto_commit", current.DefaultAutoCommit)
		current.DefaultAutoMerge = updates.boolPtrOr("default_auto_merge", current.DefaultAutoMerge)
		current.DefaultSubtasksAutoMerge = updates.boolPtrOr("default_subtasks_auto_merge", current.DefaultSubtasksAutoMerge)
		current.HideModelBadge = updates.boolPtrOr("hide_model_badge", current.HideModelBadge)
		current.UpdateCheckEnabled = updates.boolPtrOr("update_check_enabled", current.UpdateCheckEnabled)
		current.RunSiblingsParallel = updates.boolPtrOr("run_siblings_parallel", current.RunSiblingsParallel)
		current.SortOldestFirst = updates.boolPtrOr("sort_oldest_first", current.SortOldestFirst)
		current.AutoMaximizeChatMobile = updates.boolPtrOr("auto_maximize_chat_mobile", current.AutoMaximizeChatMobile)
		current.DefaultMaxFailedTurns = updates.intPtrOr("default_max_failed_turns", current.DefaultMaxFailedTurns)
		current.DefaultAutoSummarizeThreshold = updates.intPtrOr("default_auto_summarize_threshold", current.DefaultAutoSummarizeThreshold)
		if threshold := current.DefaultAutoSummarizeThreshold; threshold != nil {
			clamped := clamp(*threshold, 1, 100)
			current.DefaultAutoSummarizeThreshold = &clamped
		}
		current.DefaultOperatingMode = updates.modeOr("default_operating_mode", current.DefaultOperatingMode)
		var ks types.KeyboardShortcuts
		if updates.field("keyboard_shortcuts", &ks) {
			current.KeyboardShortcuts = &ks
		}
		if updates.err != nil {
			httputil.JSONError(w, http.StatusBadRequest, updates.err.Error())
			return
		}
		if err := cs.UpdateConfig(current); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to update config")
			return
		}
		resp := cs.buildConfigResponse(current)
		httputil.JSONResponse(w, http.StatusOK, resp)
	default:
		httputil.MethodNotAllowed(w)
	}
}

// parsePtr parses a string to a *T using parse. Returns a nil pointer if the
// string is empty (field absent from the request).
func parsePtr[T any](s string, parse func(string) (T, error)) (*T, error) {
	if s == "" {
		return nil, nil
	}
	v, err := parse(s)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// parseBoolPtr parses a string to a *bool. Returns nil pointer if the string is empty.
func parseBoolPtr(s string) (*bool, error) {
	return parsePtr(s, strconv.ParseBool)
}

// parseIntPtr parses a string to a *int. Returns nil pointer if the string is empty.
func parseIntPtr(s string) (*int, error) {
	return parsePtr(s, strconv.Atoi)
}

// DefaultAutoSummarizeThreshold is the auto-summarization threshold (percent of the context window)
const DefaultAutoSummarizeThreshold = 90

// AutoSummarizeThreshold returns the configured auto-summarization threshold as a
// percent of the context window (1-100). Falls back to DefaultAutoSummarizeThreshold
// when unset or invalid.
func (cs *ConfigStore) AutoSummarizeThreshold() int {
	raw, err := cs.db.GetConfigValue("default_auto_summarize_threshold")
	if err != nil {
		return DefaultAutoSummarizeThreshold
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 || v > 100 {
		return DefaultAutoSummarizeThreshold
	}
	return v
}

// GetRunSiblingsParallel returns whether a parent's startable subtasks should run in
// parallel (all at once) rather than one at a time. Returns false (sequential)
// when the value is unset or invalid.
func (cs *ConfigStore) GetRunSiblingsParallel() bool {
	raw, _ := cs.db.GetConfigValue("run_siblings_parallel")
	if v, err := parseBoolPtr(raw); err == nil && v != nil {
		return *v
	}
	return false
}

// GetProviderAPIKey is the key stored for a provider URL, or "" when no provider matches or none is
// set. One of the two places a stored key is opened.
func (cs *ConfigStore) GetProviderAPIKey(serverURL string) string {
	sealed, record := cs.sealedKeyFor(serverURL)
	if sealed == "" || cs.box == nil {
		return ""
	}
	plain, err := cs.box.Decrypt(providerKeyPurpose, record, sealed)
	if err != nil {
		// An unreadable key is no key: the run continues as if none were set, and Settings is where it
		// gets typed again.
		logging.For(logging.LLM).Warnf("provider API key for %s did not open: %s", serverURL, err)
		return ""
	}
	return plain
}

// sealedKeyFor is the stored key for a provider URL and the record it was sealed under.
func (cs *ConfigStore) sealedKeyFor(serverURL string) (sealed, record string) {
	snap, err := cs.snapshot()
	if err != nil {
		return "", ""
	}
	return storedAPIKey(snap.providers, serverURL)
}

// ProvidersWithUnreadableKeys names the providers whose stored secret this build cannot open. Startup
// says it once, so a run with no key is not silent.
func (cs *ConfigStore) ProvidersWithUnreadableKeys() []string {
	providers, err := cs.storedProviders()
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range providers {
		v := p.APIKeyValue()
		if v == "" {
			continue
		}
		if cs.box == nil || !cs.box.Opens(providerKeyPurpose, strconv.Itoa(p.ID), v) {
			out = append(out, p.Name)
		}
	}
	return out
}

// SaveModelContextWindow persists a discovered context window for a model in the config.
func (cs *ConfigStore) SaveModelContextWindow(modelName, providerURL string, contextWindow int) {
	// The stored row, not the config view: this writes the list back, and the view has no keys to write.
	providers, err := cs.storedProviders()
	if err != nil {
		return
	}

	for i := range providers {
		if providers[i].URL == providerURL {
			for j := range providers[i].Models {
				if providers[i].Models[j].Name == modelName {
					providers[i].Models[j].ContextWindow = &contextWindow
					break
				}
			}
			break
		}
	}

	providersJSON, _ := json.Marshal(providers)
	_ = cs.db.SetConfigValue("providers", string(providersJSON))
}
