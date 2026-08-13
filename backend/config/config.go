package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"kanban/backend/crypto"
	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/types"
)

// AgentProviderLister provides agent provider entries discovered from extensions.
// The agent.AgentRegistry satisfies this interface (ToProviders() []types.Provider).
// Defined as an interface to avoid a circular import between config and agent packages.
type AgentProviderLister interface {
	ToProviders() []types.Provider
}

// RegistryURL returns the extension registry index URL. It is sourced
// exclusively from the MIKE_REGISTRY_URL environment variable (which takes
// precedence) and otherwise falls back to the hosted online registry. It is no
// longer persisted to the database.
func RegistryURL() string {
	if v := os.Getenv("MIKE_REGISTRY_URL"); v != "" {
		return v
	}
	return "https://getmike.dev/extensions/registry.json"
}

// ConfigStore wraps Store for config-specific operations.
type ConfigStore struct {
	store                *store.Store
	dbPath               string
	version              string
	agentRegistry        AgentProviderLister // optional — set via SetAgentRegistry
	workspaceDirOverride string              // runtime override (--worktrees-dir / env), not persisted
}

// SetWorkspaceDirOverride sets a runtime override for the worktrees directory
func (cs *ConfigStore) SetWorkspaceDirOverride(dir string) {
	cs.workspaceDirOverride = dir
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

// filterPersistedProviders removes agent:// providers from the list so they
// are not persisted to the database. Agent providers are discovered dynamically
// at runtime from enabled agent extensions and should not be stored alongside
// user-configured LLM providers.
func filterPersistedProviders(providers []types.Provider) []types.Provider {
	filtered := make([]types.Provider, 0, len(providers))
	for _, p := range providers {
		if strings.HasPrefix(p.URL, "agent://") {
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
	providersRaw, err := cs.store.GetConfigValue("providers")
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
		if !strings.HasPrefix(p.URL, "agent://") && p.ID == 0 {
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
		raw, _ := cs.store.GetConfigValue(key)
		if id, ok := slugToID[raw]; ok {
			if err := cs.store.SetConfigValue(key, id); err != nil {
				return err
			}
		}
	}

	// Remap provider_name rows in the message/token tables from slug to ID.
	for _, table := range []string{"task_messages", "project_token_stats", "project_token_tasks"} {
		for slug, id := range slugToID {
			if _, err := cs.store.Exec(`UPDATE `+table+` SET provider_name = ? WHERE provider_name = ?`, id, slug); err != nil {
				return err
			}
		}
	}

	providersJSON, err := json.Marshal(providers)
	if err != nil {
		return err
	}
	return cs.store.SetConfigValue("providers", string(providersJSON))
}

// NewConfigStore creates a new ConfigStore.
func NewConfigStore(store *store.Store, dbPath, version string) *ConfigStore {
	abs, _ := filepath.Abs(dbPath)
	return &ConfigStore{store: store, dbPath: abs, version: version}
}

// GetConfig retrieves the full server configuration.
// API keys are decrypted from the DB before returning.
func (cs *ConfigStore) GetConfig() (*types.Config, error) {
	workspaceDir, err := cs.store.GetConfigValue("workspace_dir")
	if err != nil {
		return nil, err
	}

	if cs.workspaceDirOverride != "" {
		workspaceDir = cs.workspaceDirOverride
	}
	// Resolve relative paths to absolute so the frontend can display them correctly
	if workspaceDir != "" && !filepath.IsAbs(workspaceDir) {
		if abs, err := filepath.Abs(workspaceDir); err == nil {
			workspaceDir = abs
		}
	}
	providersRaw, err := cs.store.GetConfigValue("providers")
	if err != nil {
		return nil, err
	}
	providers := make([]types.Provider, 0)
	if providersRaw != "" {
		if err := json.Unmarshal([]byte(providersRaw), &providers); err != nil {
			return nil, err
		}
	}
	// Display names are derived dynamically from the URL on the frontend
	for i := range providers {
		if !strings.HasPrefix(providers[i].URL, "agent://") && providers[i].Kind != "agent" {
			providers[i].DisplayName = ""
		}
	}
	// Decrypt API keys
	for i := range providers {
		if providers[i].APIKey != nil && *providers[i].APIKey != "" {
			decrypted, err := crypto.Decrypt(*providers[i].APIKey)
			if err == nil {
				providers[i].APIKey = &decrypted
			}
			// If decryption fails, leave the value as-is (may be plain text from before encryption was added)
		}
	}

	workerProvider, err := cs.store.GetConfigValue("worker_provider")
	if err != nil {
		return nil, err
	}
	workerModel, err := cs.store.GetConfigValue("worker_model")
	if err != nil {
		return nil, err
	}
	utilityProvider, err := cs.store.GetConfigValue("utility_provider")
	if err != nil {
		return nil, err
	}
	utilityModel, err := cs.store.GetConfigValue("utility_model")
	if err != nil {
		return nil, err
	}
	registryURL := RegistryURL()
	defaultAutoCommitRaw, _ := cs.store.GetConfigValue("default_auto_commit")
	defaultAutoMergeRaw, _ := cs.store.GetConfigValue("default_auto_merge")
	defaultSubtasksAutoMergeRaw, _ := cs.store.GetConfigValue("default_subtasks_auto_merge")
	defaultOperatingModeRaw, _ := cs.store.GetConfigValue("default_operating_mode")
	defaultMaxFailedTurnsRaw, _ := cs.store.GetConfigValue("default_max_failed_turns")

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
		mode := defaultOperatingModeRaw
		if mode != "act" && mode != "plan" && mode != "explore" {
			mode = "act"
		}
		cfg.DefaultOperatingMode = &mode
	}
	if v, err := parseIntPtr(defaultMaxFailedTurnsRaw); err == nil && v != nil {
		cfg.DefaultMaxFailedTurns = v
	}
	return cfg, nil
}

// UpdateConfig persists the full server configuration and updates the in-memory override if set.
// API keys are encrypted before DB storage.
func (cs *ConfigStore) UpdateConfig(c *types.Config) error {
	// Assign stable IDs to providers that don't have one yet (new providers).
	assignProviderIDs(c.Providers)
	// Enforce minimum concurrency of 1 and encrypt API keys for all providers
	for i := range c.Providers {
		if c.Providers[i].Concurrency < 1 {
			c.Providers[i].Concurrency = 1
		}
		if c.Providers[i].APIKey != nil && *c.Providers[i].APIKey != "" {
			encrypted, err := crypto.Encrypt(*c.Providers[i].APIKey)
			if err != nil {
				return fmt.Errorf("failed to encrypt API key for provider %q: %w", c.Providers[i].Name, err)
			}
			c.Providers[i].APIKey = &encrypted
		}
		// Display names are derived dynamically from the URL on the frontend
		if !strings.HasPrefix(c.Providers[i].URL, "agent://") && c.Providers[i].Kind != "agent" {
			c.Providers[i].DisplayName = ""
		}
	}
	providersJSON, err := json.Marshal(c.Providers)
	if err != nil {
		return err
	}
	// workspace_dir is set at startup via CLI flag / env var — not modifiable at runtime
	if err := cs.store.SetConfigValue("providers", string(providersJSON)); err != nil {
		return err
	}
	if err := cs.store.SetConfigValue("worker_provider", c.WorkerProvider); err != nil {
		return err
	}
	if err := cs.store.SetConfigValue("worker_model", c.WorkerModel); err != nil {
		return err
	}
	if err := cs.store.SetConfigValue("utility_provider", c.UtilityProvider); err != nil {
		return err
	}
	if err := cs.store.SetConfigValue("utility_model", c.UtilityModel); err != nil {
		return err
	}
	if c.DefaultAutoCommit != nil {
		if err := cs.store.SetConfigValue("default_auto_commit", fmt.Sprintf("%t", *c.DefaultAutoCommit)); err != nil {
			return err
		}
	}
	if c.DefaultAutoMerge != nil {
		if err := cs.store.SetConfigValue("default_auto_merge", fmt.Sprintf("%t", *c.DefaultAutoMerge)); err != nil {
			return err
		}
	}
	if c.DefaultSubtasksAutoMerge != nil {
		if err := cs.store.SetConfigValue("default_subtasks_auto_merge", fmt.Sprintf("%t", *c.DefaultSubtasksAutoMerge)); err != nil {
			return err
		}
	}
	if c.DefaultOperatingMode != nil {
		if err := cs.store.SetConfigValue("default_operating_mode", *c.DefaultOperatingMode); err != nil {
			return err
		}
	}
	if c.DefaultMaxFailedTurns != nil {
		if err := cs.store.SetConfigValue("default_max_failed_turns", fmt.Sprintf("%d", *c.DefaultMaxFailedTurns)); err != nil {
			return err
		}
	}
	return nil
}

// configResponse wraps Config with additional metadata for API responses.
type configResponse struct {
	WorkspaceDir             string           `json:"workspace_dir"`
	DatabasePath             string           `json:"database_path"`
	RegistryURL              string           `json:"registry_url"`
	Version                  string           `json:"version"`
	Providers                []types.Provider `json:"providers"`
	WorkerProvider           string           `json:"worker_provider"`
	WorkerModel              string           `json:"worker_model"`
	UtilityProvider          string           `json:"utility_provider"`
	UtilityModel             string           `json:"utility_model"`
	DefaultAutoCommit        *bool            `json:"default_auto_commit"`
	DefaultAutoMerge         *bool            `json:"default_auto_merge"`
	DefaultSubtasksAutoMerge *bool            `json:"default_subtasks_auto_merge"`
	DefaultOperatingMode     *string          `json:"default_operating_mode"`
	DefaultMaxFailedTurns    *int             `json:"default_max_failed_turns,omitempty"`
	EncryptionEnabled        bool             `json:"encryption_enabled"`
}

// maskAPIKeys replaces non-empty API keys with a masked placeholder.

const maskedAPIKey = "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022" // 10 bullets

func maskAPIKeys(providers []types.Provider) {
	for i := range providers {
		if providers[i].APIKey != nil && *providers[i].APIKey != "" {
			masked := maskedAPIKey
			providers[i].APIKey = &masked
		}
	}
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
	maskAPIKeys(providers)
	return configResponse{
		WorkspaceDir:             cfg.WorkspaceDir,
		DatabasePath:             cfg.DatabasePath,
		RegistryURL:              cfg.RegistryURL,
		Version:                  cs.version,
		Providers:                providers,
		WorkerProvider:           cfg.WorkerProvider,
		WorkerModel:              cfg.WorkerModel,
		UtilityProvider:          cfg.UtilityProvider,
		UtilityModel:             cfg.UtilityModel,
		DefaultAutoCommit:        cfg.DefaultAutoCommit,
		DefaultAutoMerge:         cfg.DefaultAutoMerge,
		DefaultSubtasksAutoMerge: cfg.DefaultSubtasksAutoMerge,
		DefaultOperatingMode:     cfg.DefaultOperatingMode,
		DefaultMaxFailedTurns:    cfg.DefaultMaxFailedTurns,
		EncryptionEnabled:        crypto.IsEnabled(),
	}
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
		// Partial update: only update fields that are provided
		var updates map[string]any
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		current, err := cs.GetConfig()
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to get current config")
			return
		}
		// workspace_dir is set at startup via CLI flag / env var — not modifiable at runtime
		if providers, ok := updates["providers"]; ok {
			if providers != nil {
				providersJSON, _ := json.Marshal(providers)
				var incoming []types.Provider
				if err := json.Unmarshal(providersJSON, &incoming); err == nil {
					// Merge: preserve API keys from existing providers when not provided in update
					// nil = not provided (preserve), "" = explicitly clear, non-empty = new value.
					existingByKey := make(map[string]string)
					for _, p := range current.Providers {
						if p.APIKey != nil && p.ID != 0 {
							existingByKey[strconv.Itoa(p.ID)] = *p.APIKey
						}
					}
					for i := range incoming {
						if incoming[i].APIKey == nil && existingByKey[strconv.Itoa(incoming[i].ID)] != "" {
							key := existingByKey[strconv.Itoa(incoming[i].ID)]
							incoming[i].APIKey = &key
						}
					}
					current.Providers = incoming // agent:// providers may be user-configured (ConfigModal); registry providers are merged in buildConfigResponse
				}
			}
		}
		if wp, ok := updates["worker_provider"]; ok {
			current.WorkerProvider = wp.(string)
		}
		if wm, ok := updates["worker_model"]; ok {
			current.WorkerModel = wm.(string)
		}
		if up, ok := updates["utility_provider"]; ok {
			current.UtilityProvider = up.(string)
		}
		if um, ok := updates["utility_model"]; ok {
			current.UtilityModel = um.(string)
		}
		if dac, ok := updates["default_auto_commit"]; ok {
			if v, ok := dac.(bool); ok {
				current.DefaultAutoCommit = &v
			}
		}
		if dam, ok := updates["default_auto_merge"]; ok {
			if v, ok := dam.(bool); ok {
				current.DefaultAutoMerge = &v
			}
		}
		if dsam, ok := updates["default_subtasks_auto_merge"]; ok {
			if v, ok := dsam.(bool); ok {
				current.DefaultSubtasksAutoMerge = &v
			}
		}
		if dom, ok := updates["default_operating_mode"]; ok {
			if v, ok := dom.(string); ok && (v == "act" || v == "plan" || v == "explore") {
				current.DefaultOperatingMode = &v
			}
		}
		if dmft, ok := updates["default_max_failed_turns"]; ok {
			if f, ok := dmft.(float64); ok {
				v := int(f)
				current.DefaultMaxFailedTurns = &v
			}
		}
		if err := cs.UpdateConfig(current); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to update config")
			return
		}
		resp := cs.buildConfigResponse(current)
		httputil.JSONResponse(w, http.StatusOK, resp)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// parseBoolPtr parses a string to a *bool. Returns nil pointer if the string is empty.
func parseBoolPtr(s string) (*bool, error) {
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// parseIntPtr parses a string to a *int. Returns nil pointer if the string is empty.
func parseIntPtr(s string) (*int, error) {
	if s == "" {
		return nil, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// GetProviderAPIKey looks up and returns the decrypted API key for a provider by URL.
// Returns empty string if no matching provider is found or no key is set.
func (cs *ConfigStore) GetProviderAPIKey(serverURL string) string {
	cfg, err := cs.GetConfig()
	if err != nil {
		return ""
	}
	serverURL = strings.TrimRight(serverURL, "/")
	for _, provider := range cfg.Providers {
		if strings.TrimRight(provider.URL, "/") == serverURL {
			return provider.APIKeyValue()
		}
	}
	return ""
}

// SaveModelContextWindow persists a discovered context window for a model in the config.
func (cs *ConfigStore) SaveModelContextWindow(modelName, providerURL string, contextWindow int) {
	cfg, err := cs.GetConfig()
	if err != nil {
		return
	}

	for i := range cfg.Providers {
		if cfg.Providers[i].URL == providerURL {
			for j := range cfg.Providers[i].Models {
				if cfg.Providers[i].Models[j].Name == modelName {
					cfg.Providers[i].Models[j].ContextWindow = &contextWindow
					break
				}
			}
			break
		}
	}

	// Persist
	providersJSON, _ := json.Marshal(cfg.Providers)
	_ = cs.store.SetConfigValue("providers", string(providersJSON))
}
