// Package config owns the effective server configuration: the config table, the two
// provider/model pairs that route work (worker_* for chat, utility_* for titles, summaries
// and commit messages), and the environment defaults used where a row is absent.
package config

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
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

// RegistryURL returns the extension registry index URL this store was wired with.
func (cs *ConfigStore) RegistryURL() string {
	if cs.registryURL == "" {
		return DefaultRegistryURL
	}
	return cs.registryURL
}

// configDB is config's whole persistence need: its own key/value table.
type configDB interface {
	GetConfigValue(key string) (string, error)
	GetConfigValues(keys ...string) (map[string]string, error)
	SetConfigValue(key, value string) error
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
	fileSizeLimitMB int64 // set at startup via SetFileSizeLimitMB

	// box opens stored provider keys. Built once at startup: a derivation costs hundreds of
	// milliseconds, so no read path may pay one.
	box *crypto.Box
	// mcpLocked is the resolved MIKE_RESTRICT_MCP decision. It is passed to NewConfigStore
	// to ensure the store is locked if the environment requires it.
	mcpLocked bool
	// statResetLocked is the resolved MIKE_RESTRICT_STAT_RESET decision. One field feeds both the route
	// that refuses the reset and the config view that hides it, so the two cannot disagree.
	statResetLocked bool

	snapMu sync.Mutex
	snap   *configSnapshot
}

// configSnapshot is the read-side cache. It holds providers exactly as stored, so no plaintext
// secret sits in it.
type configSnapshot struct {
	generation uint64
	rows       map[string]string
	providers  []types.Provider
	mcpServers []types.MCPServer
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

// SetFileSizeLimitMB records the file size this server was started with, so the browser caps what it offers
// a person at the size a refusal quotes: the byte it compares a File against is this << 20.
func (cs *ConfigStore) SetFileSizeLimitMB(megabytes int64) {
	cs.fileSizeLimitMB = megabytes
}

// WorkspaceDir returns the worktrees directory, made absolute. GetConfig carries the same value; ask
// here when only the directory is needed.
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

// NewConfigStore creates a new ConfigStore. box comes from crypto.New at startup, and the two locks are
// the resolved MIKE_RESTRICT_MCP and MIKE_RESTRICT_STAT_RESET decisions.
func NewConfigStore(s configDB, dbPath, version string, box *crypto.Box, mcpLocked, statResetLocked bool) *ConfigStore {
	abs, _ := filepath.Abs(dbPath)
	return &ConfigStore{db: s, dbPath: abs, version: version, box: box, mcpLocked: mcpLocked, statResetLocked: statResetLocked}
}

func (cs *ConfigStore) snapshot() (*configSnapshot, error) {
	generation := cs.db.ConfigGeneration()
	cs.snapMu.Lock()
	defer cs.snapMu.Unlock()
	if cs.snap != nil && cs.snap.generation == generation {
		return cs.snap, nil
	}
	rows, err := cs.db.GetConfigValues(append(append([]string{}, configRowKeys...), "providers", mcpServersRow)...)
	if err != nil {
		return nil, err
	}
	providers := make([]types.Provider, 0)
	if raw := rows["providers"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &providers); err != nil {
			return nil, err
		}
	}
	cs.snap = &configSnapshot{generation: generation, rows: rows, providers: providers, mcpServers: parseMCPServers(rows[mcpServersRow])}
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
	// The MCP registry goes out masked rather than emptied: Settings shows which servers exist and
	// which of them hold a token, and never the token.
	mcpServers := copyMCPServers(snap.mcpServers)
	maskMCPServerSecrets(mcpServers, snap.mcpServers)
	rows := snap.rows

	cfg := &types.Config{
		WorkspaceDir: workspaceDir,
		DatabasePath: cs.dbPath,
		RegistryURL:  cs.RegistryURL(),
		Providers:    providers,
		MCPServers:   mcpServers,
	}
	for _, p := range configPrefs {
		p.decode(rows[p.rowKey()], cfg)
	}
	cfg.KeyboardShortcuts = normalizeKeyboardShortcuts(cfg.KeyboardShortcuts)
	return cfg, nil
}

// normalizeKeyboardShortcuts returns nil when no binding is set, so the field is omitted from API
// responses. Empty() holds which fields decide that.
func normalizeKeyboardShortcuts(ks *types.KeyboardShortcuts) *types.KeyboardShortcuts {
	if ks.Empty() {
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
	// Refused before any row is written, so a rejected save leaves the config as it was.
	if err := validateModelSampling(c.Providers); err != nil {
		return err
	}
	storedKeys, err := cs.storedKeysByProviderID()
	if err != nil {
		return err
	}
	// The MCP registry validates and seals in one pass, before any row is written.
	mcpServers, err := cs.prepareMCPServers(c.MCPServers)
	if err != nil {
		return err
	}
	if err := cs.refuseMCPChange(mcpServers); err != nil {
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
	mcpJSON, err := json.Marshal(mcpServers)
	if err != nil {
		return err
	}
	if err := cs.db.SetConfigValue("providers", string(providersJSON)); err != nil {
		return err
	}
	// The whole list, every time: an MCP server absent from this write is a server that was deleted.
	if err := cs.db.SetConfigValue(mcpServersRow, string(mcpJSON)); err != nil {
		return err
	}
	// Every preference row the config answered. A field that held nothing is no row to write, which
	// leaves an earlier answer in place rather than erasing it.
	for _, p := range configPrefs {
		text, ok, err := p.encode(c)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := cs.db.SetConfigValue(p.rowKey(), text); err != nil {
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
	DefaultMaxFailedTurns         *int                     `json:"default_max_failed_turns,omitzero"`
	DefaultAutoSummarizeThreshold *int                     `json:"default_auto_summarize_threshold,omitzero"`
	HideModelBadge                *bool                    `json:"hide_model_badge,omitzero"`
	UpdateCheckEnabled            *bool                    `json:"update_check_enabled,omitzero"`
	RunSiblingsParallel           *bool                    `json:"run_siblings_parallel,omitzero"`
	SortOldestFirst               *bool                    `json:"sort_oldest_first,omitzero"`
	AutoMaximizeChatMobile        *bool                    `json:"auto_maximize_chat_mobile,omitzero"`
	CollapseReasoning             *bool                    `json:"collapse_reasoning,omitzero"`
	ReclaimIdleSandbox            *bool                    `json:"reclaim_idle_sandbox,omitzero"`
	LoopBreaker                   *bool                    `json:"loop_breaker,omitzero"`
	KeyboardShortcuts             *types.KeyboardShortcuts `json:"keyboard_shortcuts,omitzero"`
	// MCPServers is the registry with every header value masked. A project's own selection is on the
	// project row, not here.
	MCPServers []types.MCPServer `json:"mcp_servers,omitempty"`
	// MCPRestricted is MIKE_RESTRICT_MCP: the registry is listed and offered but frozen, so Settings
	// shows it read-only. It is a fact about the process, never a stored preference.
	MCPRestricted bool `json:"mcp_restricted"`
	// StatsResetRestricted is MIKE_RESTRICT_STAT_RESET: Settings hides the reset because the server
	// refuses it. A fact about the process, never a stored preference.
	StatsResetRestricted bool `json:"stats_reset_restricted"`
	EncryptionEnabled    bool `json:"encryption_enabled"`
	// TLSCAAvailable tells the UI to offer the CA download (GET /api/tls/ca). The
	// CA's path is not published: the route already knows where it lives.
	TLSCAAvailable bool `json:"tls_ca_available"`
	// MemoryBudgetMB is the ceiling every sandbox MIKE started shares, MemoryTotalMB the installed RAM
	// it was computed from and MemoryBudgetPct the configured percentage. A budget of 0 means the gate
	// is disabled and the UI says nothing about memory.
	MemoryBudgetMB  int64 `json:"memory_budget_mb"`
	MemoryTotalMB   int64 `json:"memory_total_mb"`
	MemoryBudgetPct int   `json:"memory_budget_pct"`
	// FileSizeLimitMB is the ceiling one file moves under here, in megabytes: the figure a too-large
	// refusal quotes and what the browser multiplies by 1<<20. A 0 means no decision was injected.
	FileSizeLimitMB int64 `json:"file_size_limit_mb"`
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
const maskedAPIKey = types.MaskedSecret

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
		if !slices.ContainsFunc(providers, func(ep types.Provider) bool { return ep.URL == ap.URL }) {
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
	// The MCP registry is masked here as well as in GetConfig, because a PUT responds from the list it
	// just received, which held plaintext headers on its way in.
	mcpServers := copyMCPServers(cfg.MCPServers)
	storedServers, _ := cs.storedMCPServers()
	maskMCPServerSecrets(mcpServers, storedServers)
	return configResponse{
		WorkspaceDir:                  cfg.WorkspaceDir,
		DatabasePath:                  cfg.DatabasePath,
		Version:                       cs.version,
		Providers:                     providers,
		MCPServers:                    mcpServers,
		MCPRestricted:                 cs.mcpLocked,
		StatsResetRestricted:          cs.statResetLocked,
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
		CollapseReasoning:             cfg.CollapseReasoning,
		ReclaimIdleSandbox:            cfg.ReclaimIdleSandbox,
		LoopBreaker:                   cfg.LoopBreaker,
		KeyboardShortcuts:             normalizeKeyboardShortcuts(cfg.KeyboardShortcuts),
		EncryptionEnabled:             cs.box != nil,
		TLSCAAvailable:                cs.tlsCAAvailable,
		FileSizeLimitMB:               cs.fileSizeLimitMB,
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
		// Providers and the MCP registry are lists this endpoint replaces wholesale, each with a shape of
		// its own, so they decode straight into their types here. Every preference row merges below, out
		// of the table.
		var incoming []types.Provider
		if updates.field("providers", &incoming) {
			// agent:// rows in the payload are dropped by UpdateConfig (agent providers
			// are registry-derived); registry agents are merged in buildConfigResponse.
			current.Providers = incoming
		}
		var incomingServers []types.MCPServer
		if updates.field("mcp_servers", &incomingServers) {
			// Whole-list replacement, like providers: a server this payload omits is a server that
			// was deleted. A header carrying the masked placeholder is a token the form never saw.
			current.MCPServers = incomingServers
		}
		for _, p := range configPrefs {
			p.merge(updates, current)
		}
		if threshold := current.DefaultAutoSummarizeThreshold; threshold != nil {
			// The reader treats a value outside 1..100 as unset, so clamping keeps the UI showing what is stored.
			clamped := min(max(*threshold, 1), 100)
			current.DefaultAutoSummarizeThreshold = &clamped
		}
		if updates.err != nil {
			httputil.JSONError(w, http.StatusBadRequest, updates.err.Error())
			return
		}
		if err := cs.UpdateConfig(current); err != nil {
			// A frozen registry is a person asking for something the operator forbade, so it is a refusal
			// that reads as one and not a fault.
			if errors.Is(err, ErrMCPLocked) {
				httputil.JSONError(w, http.StatusForbidden, err.Error())
				return
			}
			// Whoever typed the value can correct it, so name the field rather than log a fault.
			if errors.Is(err, ErrInvalidConfig) {
				httputil.JSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			httputil.JSONError(w, http.StatusInternalServerError, "failed to update config")
			return
		}
		resp := cs.buildConfigResponse(current)
		httputil.JSONResponse(w, http.StatusOK, resp)
	default:
		httputil.MethodNotAllowed(w)
	}
}

// DefaultAutoSummarizeThreshold is the auto-summarization threshold (percent of the context window)
const DefaultAutoSummarizeThreshold = 90

// AutoSummarizeThreshold returns the configured auto-summarization threshold as a
// percent of the context window (1-100). Falls back to DefaultAutoSummarizeThreshold
// when unset or invalid.
func (cs *ConfigStore) AutoSummarizeThreshold() int {
	v, ok := cs.storedInt(prefDefaultAutoSummarizeThreshold)
	if !ok || v < 1 || v > 100 {
		return DefaultAutoSummarizeThreshold
	}
	return v
}

// GetRunSiblingsParallel returns whether a parent's startable subtasks should run in
// parallel (all at once) rather than one at a time, which is what an unset row answers.
func (cs *ConfigStore) GetRunSiblingsParallel() bool {
	return cs.storedFlag(prefRunSiblingsParallel)
}

// GetReclaimIdleSandbox reports whether the memory supervisor may release the container of a task idle
// past the reclaim grace.
func (cs *ConfigStore) GetReclaimIdleSandbox() bool {
	return cs.storedFlag(prefReclaimIdleSandbox)
}

// GetCollapseReasoning reports whether a turn's reasoning text is kept off the wire while it streams.
func (cs *ConfigStore) GetCollapseReasoning() bool {
	return cs.storedFlag(prefCollapseReasoning)
}

// GetLoopBreaker reports whether a run that reaches its consecutive-failed-turn limit is recovered from
// instead of handed back.
func (cs *ConfigStore) GetLoopBreaker() bool {
	return cs.storedFlag(prefLoopBreaker)
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
