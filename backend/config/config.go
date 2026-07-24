package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"kanban/backend/crypto"
	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/types"
)

// ConfigStore wraps Store for config-specific operations.
type ConfigStore struct {
	store   *store.Store
	dbPath  string
	version string
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

	defaultProvider, err := cs.store.GetConfigValue("default_provider")
	if err != nil {
		return nil, err
	}
	registryURL, _ := cs.store.GetConfigValue("registry_url")
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
		DefaultProvider: defaultProvider,
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
	}
	providersJSON, err := json.Marshal(c.Providers)
	if err != nil {
		return err
	}
	// workspace_dir is set at startup via CLI flag / env var — not modifiable at runtime
	if err := cs.store.SetConfigValue("providers", string(providersJSON)); err != nil {
		return err
	}
	if err := cs.store.SetConfigValue("default_provider", c.DefaultProvider); err != nil {
		return err
	}
	if err := cs.store.SetConfigValue("registry_url", c.RegistryURL); err != nil {
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
	WorkspaceDir           string           `json:"workspace_dir"`
	DatabasePath           string           `json:"database_path"`
	RegistryURL            string           `json:"registry_url"`
	Version                string           `json:"version"`
	Providers              []types.Provider `json:"providers"`
	DefaultProvider        string           `json:"default_provider"`
	DefaultAutoCommit      *bool            `json:"default_auto_commit"`
	DefaultAutoMerge       *bool            `json:"default_auto_merge"`
	DefaultSubtasksAutoMerge *bool          `json:"default_subtasks_auto_merge"`
	DefaultOperatingMode   *string          `json:"default_operating_mode"`
	DefaultMaxFailedTurns  *int             `json:"default_max_failed_turns,omitempty"`
	EncryptionEnabled      bool             `json:"encryption_enabled"`
}

func maskedToken(token string) string {
	if token == "" {
		return ""
	}
	return maskedAPIKey
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

// HandleConfig handles GET and PUT /api/config.
func (cs *ConfigStore) HandleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := cs.GetConfig()
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to get config")
			return
		}
		// Mask API keys before sending to client
		maskAPIKeys(cfg.Providers)
		resp := configResponse{
			WorkspaceDir:           cfg.WorkspaceDir,
			DatabasePath:           cfg.DatabasePath,
			RegistryURL:            cfg.RegistryURL,
			Version:                cs.version,
			Providers:              cfg.Providers,
			DefaultProvider:        cfg.DefaultProvider,
			DefaultAutoCommit:      cfg.DefaultAutoCommit,
			DefaultAutoMerge:       cfg.DefaultAutoMerge,
			DefaultSubtasksAutoMerge: cfg.DefaultSubtasksAutoMerge,
			DefaultOperatingMode:   cfg.DefaultOperatingMode,
			DefaultMaxFailedTurns:  cfg.DefaultMaxFailedTurns,
			EncryptionEnabled:      crypto.IsEnabled(),
		}
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
		if rurl, ok := updates["registry_url"]; ok {
			current.RegistryURL = rurl.(string)
		}
		if providers, ok := updates["providers"]; ok {
			if providers != nil {
				providersJSON, _ := json.Marshal(providers)
				var incoming []types.Provider
				if err := json.Unmarshal(providersJSON, &incoming); err == nil {
					// Merge: preserve API keys from existing providers when not provided in update
					// nil = not provided (preserve), "" = explicitly clear, non-empty = new value.
					existingByKey := make(map[string]string)
					for _, p := range current.Providers {
						if p.APIKey != nil {
							existingByKey[p.Name] = *p.APIKey
						}
					}
					for i := range incoming {
						if incoming[i].APIKey == nil && existingByKey[incoming[i].Name] != "" {
							key := existingByKey[incoming[i].Name]
							incoming[i].APIKey = &key
						}
					}
					current.Providers = incoming
				}
			}
		}
		if dp, ok := updates["default_provider"]; ok {
			current.DefaultProvider = dp.(string)
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
		// Mask API keys before sending response
		maskAPIKeys(current.Providers)
		resp := configResponse{
			WorkspaceDir:           current.WorkspaceDir,
			DatabasePath:           current.DatabasePath,
			RegistryURL:            current.RegistryURL,
			Version:                cs.version,
			Providers:              current.Providers,
			DefaultProvider:        current.DefaultProvider,
			DefaultAutoCommit:      current.DefaultAutoCommit,
			DefaultAutoMerge:       current.DefaultAutoMerge,
			DefaultSubtasksAutoMerge: current.DefaultSubtasksAutoMerge,
			DefaultOperatingMode:   current.DefaultOperatingMode,
			DefaultMaxFailedTurns:  current.DefaultMaxFailedTurns,
			EncryptionEnabled:      crypto.IsEnabled(),
		}
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
