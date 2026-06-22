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
	store    *store.Store
	dbPath   string
}

// NewConfigStore creates a new ConfigStore.
func NewConfigStore(store *store.Store, dbPath string) *ConfigStore {
	abs, _ := filepath.Abs(dbPath)
	return &ConfigStore{store: store, dbPath: abs}
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
			if err != nil {
				// If decryption fails, leave the value as-is (may be plain text from before encryption was added)
				providers[i].APIKey = &decrypted
			} else {
				providers[i].APIKey = &decrypted
			}
		}
	}

	defaultProvider, err := cs.store.GetConfigValue("default_provider")
	if err != nil {
		return nil, err
	}
	defaultAutoCommitRaw, _ := cs.store.GetConfigValue("default_auto_commit")
	defaultPlanModeRaw, _ := cs.store.GetConfigValue("default_plan_mode")
	defaultMaxFailedTurnsRaw, _ := cs.store.GetConfigValue("default_max_failed_turns")
	defaultSandboxImageRaw, _ := cs.store.GetConfigValue("default_sandbox_image")

	cfg := &types.Config{
		WorkspaceDir:    workspaceDir,
		DatabasePath:    cs.dbPath,
		Providers:       providers,
		DefaultProvider: defaultProvider,
	}
	if v, err := parseBoolPtr(defaultAutoCommitRaw); err == nil && v != nil {
		cfg.DefaultAutoCommit = v
	}
	if v, err := parseBoolPtr(defaultPlanModeRaw); err == nil && v != nil {
		cfg.DefaultPlanMode = v
	}
	if v, err := parseIntPtr(defaultMaxFailedTurnsRaw); err == nil && v != nil {
		cfg.DefaultMaxFailedTurns = v
	}
	if defaultSandboxImageRaw != "" {
		cfg.DefaultSandboxImage = defaultSandboxImageRaw
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
	if err := cs.store.SetConfigValue("workspace_dir", c.WorkspaceDir); err != nil {
		return err
	}
	if err := cs.store.SetConfigValue("providers", string(providersJSON)); err != nil {
		return err
	}
	if err := cs.store.SetConfigValue("default_provider", c.DefaultProvider); err != nil {
		return err
	}
	if c.DefaultAutoCommit != nil {
		if err := cs.store.SetConfigValue("default_auto_commit", fmt.Sprintf("%t", *c.DefaultAutoCommit)); err != nil {
			return err
		}
	}
	if c.DefaultPlanMode != nil {
		if err := cs.store.SetConfigValue("default_plan_mode", fmt.Sprintf("%t", *c.DefaultPlanMode)); err != nil {
			return err
		}
	}
	if c.DefaultMaxFailedTurns != nil {
		if err := cs.store.SetConfigValue("default_max_failed_turns", fmt.Sprintf("%d", *c.DefaultMaxFailedTurns)); err != nil {
			return err
		}
	}
	if err := cs.store.SetConfigValue("default_sandbox_image", c.DefaultSandboxImage); err != nil {
		return err
	}
	return nil
}

// configResponse wraps Config with additional metadata for API responses.
type configResponse struct {
	WorkspaceDir        string     `json:"workspace_dir"`
	DatabasePath        string     `json:"database_path"`
	Providers           []types.Provider `json:"providers"`
	DefaultProvider     string     `json:"default_provider"`
	DefaultAutoCommit   *bool      `json:"default_auto_commit,omitempty"`
	DefaultPlanMode     *bool      `json:"default_plan_mode,omitempty"`
	DefaultMaxFailedTurns *int     `json:"default_max_failed_turns,omitempty"`
	DefaultSandboxImage string     `json:"default_sandbox_image,omitempty"`
	EncryptionEnabled   bool       `json:"encryption_enabled"`
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
			WorkspaceDir:        cfg.WorkspaceDir,
			DatabasePath:        cfg.DatabasePath,
			Providers:           cfg.Providers,
			DefaultProvider:     cfg.DefaultProvider,
			DefaultAutoCommit:   cfg.DefaultAutoCommit,
			DefaultPlanMode:     cfg.DefaultPlanMode,
			DefaultMaxFailedTurns: cfg.DefaultMaxFailedTurns,
			DefaultSandboxImage: cfg.DefaultSandboxImage,
			EncryptionEnabled:   crypto.IsEnabled(),
		}
		httputil.JSONResponse(w, http.StatusOK, resp)
	case http.MethodPut:
		// Partial update: only update fields that are provided
		var updates map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		current, err := cs.GetConfig()
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to get current config")
			return
		}
		if workspace, ok := updates["workspace_dir"]; ok {
			current.WorkspaceDir = workspace.(string)
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
		if dpm, ok := updates["default_plan_mode"]; ok {
			if v, ok := dpm.(bool); ok {
				current.DefaultPlanMode = &v
			}
		}
		if dmft, ok := updates["default_max_failed_turns"]; ok {
			if f, ok := dmft.(float64); ok {
				v := int(f)
				current.DefaultMaxFailedTurns = &v
			}
		}
		if dsi, ok := updates["default_sandbox_image"]; ok {
			if s, ok := dsi.(string); ok {
				current.DefaultSandboxImage = s
			}
		}
		if err := cs.UpdateConfig(current); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to update config")
			return
		}
		// Mask API keys before sending response
		maskAPIKeys(current.Providers)
		resp := configResponse{
			WorkspaceDir:        current.WorkspaceDir,
			DatabasePath:        current.DatabasePath,
			Providers:           current.Providers,
			DefaultProvider:     current.DefaultProvider,
			DefaultAutoCommit:   current.DefaultAutoCommit,
			DefaultPlanMode:     current.DefaultPlanMode,
			DefaultMaxFailedTurns: current.DefaultMaxFailedTurns,
			DefaultSandboxImage: current.DefaultSandboxImage,
			EncryptionEnabled:   crypto.IsEnabled(),
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
