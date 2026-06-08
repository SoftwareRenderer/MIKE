package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ConfigStore wraps Store for config-specific operations.
type ConfigStore struct {
	store      *Store
	overrideMu sync.Mutex
	override   *Config // in-memory override (e.g. for tests), nil = no override
}

// NewConfigStore creates a new ConfigStore.
func NewConfigStore(store *Store) *ConfigStore {
	return &ConfigStore{store: store}
}

// GetConfig retrieves the full server configuration.
// If an in-memory override is set (e.g. for tests), it is returned instead.
func (cs *ConfigStore) GetConfig() (*Config, error) {
	cs.overrideMu.Lock()
	if cs.override != nil {
		o := *cs.override
		cs.overrideMu.Unlock()
		return &o, nil
	}
	cs.overrideMu.Unlock()

	llamaURL, err := cs.store.GetConfigValue("llama_server_url")
	if err != nil {
		return nil, err
	}
	modelName, err := cs.store.GetConfigValue("model_name")
	if err != nil {
		return nil, err
	}
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
	providers := make([]Provider, 0)
	if providersRaw != "" {
		if err := json.Unmarshal([]byte(providersRaw), &providers); err != nil {
			return nil, err
		}
	}

	defaultProvider, err := cs.store.GetConfigValue("default_provider")
	if err != nil {
		return nil, err
	}
	defaultAutoCommitRaw, _ := cs.store.GetConfigValue("default_auto_commit")
	defaultPlanModeRaw, _ := cs.store.GetConfigValue("default_plan_mode")
	defaultMaxFailedTurnsRaw, _ := cs.store.GetConfigValue("default_max_failed_turns")

	cfg := &Config{
		LlamaServerURL:  llamaURL,
		ModelName:       modelName,
		WorkspaceDir:    workspaceDir,
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
	return cfg, nil
}

// UpdateConfig persists the full server configuration and updates the in-memory override if set.
func (cs *ConfigStore) UpdateConfig(c *Config) error {
	providersJSON, err := json.Marshal(c.Providers)
	if err != nil {
		return err
	}
	if err := cs.store.SetConfigValue("llama_server_url", c.LlamaServerURL); err != nil {
		return err
	}
	if err := cs.store.SetConfigValue("model_name", c.ModelName); err != nil {
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
	// Also update in-memory override if set (so tests see the new values)
	cs.overrideMu.Lock()
	if cs.override != nil {
		*cs.override = *c
	}
	cs.overrideMu.Unlock()
	return nil
}

// HandleConfig handles GET and PUT /api/config.
func (cs *ConfigStore) HandleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := cs.GetConfig()
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "failed to get config")
			return
		}
		jsonResponse(w, http.StatusOK, cfg)
	case http.MethodPut:
		// Partial update: only update fields that are provided
		var updates map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		current, err := cs.GetConfig()
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "failed to get current config")
			return
		}
		if url, ok := updates["llama_server_url"]; ok {
			current.LlamaServerURL = url.(string)
		}
		if model, ok := updates["model_name"]; ok {
			current.ModelName = model.(string)
		}
		if workspace, ok := updates["workspace_dir"]; ok {
			current.WorkspaceDir = workspace.(string)
		}
		if providers, ok := updates["providers"]; ok {
			if providers != nil {
				providersJSON, _ := json.Marshal(providers)
				var p []Provider
				if err := json.Unmarshal(providersJSON, &p); err == nil {
					current.Providers = p
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
		if err := cs.UpdateConfig(current); err != nil {
			jsonError(w, http.StatusInternalServerError, "failed to update config")
			return
		}
		jsonResponse(w, http.StatusOK, current)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// TestConnectionRequest is the body for POST /api/config/test-connection.
type TestConnectionRequest struct {
	LlamaServerURL string `json:"llama_server_url"`
	ModelName      string `json:"model_name"`
}

// TestConnectionResponse is the result of a connection test.
type TestConnectionResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// TestConnection sends a minimal chat request to the LLM server and returns the result.
func (cs *ConfigStore) TestConnection(req TestConnectionRequest) TestConnectionResponse {
	url := strings.TrimRight(req.LlamaServerURL, "/") + "/chat/completions"

	body := map[string]interface{}{
		"model":    "test",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return TestConnectionResponse{Success: false, Message: fmt.Sprintf("Failed to build request: %v", err)}
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(url, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return TestConnectionResponse{Success: false, Message: fmt.Sprintf("Connection failed: %v", err)}
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // drain body

	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		return TestConnectionResponse{Success: true, Message: "Connection successful!"}
	}
	return TestConnectionResponse{Success: false, Message: fmt.Sprintf("Connection failed: HTTP %d", resp.StatusCode)}
}

// HandleTestConnection handles POST /api/config/test-connection.
func (cs *ConfigStore) HandleTestConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req TestConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.LlamaServerURL == "" {
		jsonError(w, http.StatusBadRequest, "llama_server_url is required")
		return
	}

	result := cs.TestConnection(req)
	if result.Success {
		jsonResponse(w, http.StatusOK, result)
	} else {
		jsonResponse(w, http.StatusOK, result) // return 200 so UI can display the message
	}
}

// SetOverride sets an in-memory config override (e.g. for tests).
// Does not persist to the database. Cleared on server restart.
func (cs *ConfigStore) SetOverride(cfg *Config) {
	cs.overrideMu.Lock()
	defer cs.overrideMu.Unlock()
	if cfg == nil {
		cs.override = nil
	} else {
		c := *cfg
		cs.override = &c
	}
}

// ClearOverride removes any in-memory config override.
func (cs *ConfigStore) ClearOverride() {
	cs.overrideMu.Lock()
	defer cs.overrideMu.Unlock()
	cs.override = nil
}

// FetchModelsRequest is the body for POST /api/config/fetch-models.
type FetchModelsRequest struct {
	LlamaServerURL string `json:"llama_server_url"`
}

// FetchModelsResponse is the result of fetching models from an LLM server.
type FetchModelsResponse struct {
	Models  []string `json:"models"`
	Error   string   `json:"error,omitempty"`
}

// FetchModels calls GET {url}/v1/models on an OpenAI-compatible endpoint and returns model IDs.
func (cs *ConfigStore) FetchModels(url string) FetchModelsResponse {
	url = strings.TrimRight(url, "/") + "/models"

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return FetchModelsResponse{Models: nil, Error: fmt.Sprintf("Connection failed: %v", err)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return FetchModelsResponse{Models: nil, Error: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body))}
	}

	// Parse OpenAI-compatible response: { "object": "list", "data": [{ "id": "model-name", ... }] }
	var oaiResp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return FetchModelsResponse{Models: nil, Error: fmt.Sprintf("Failed to parse response: %v", err)}
	}

	models := make([]string, 0, len(oaiResp.Data))
	for _, m := range oaiResp.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return FetchModelsResponse{Models: models}
}

// HandleFetchModels handles POST /api/config/fetch-models.
func (cs *ConfigStore) HandleFetchModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req FetchModelsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.LlamaServerURL == "" {
		jsonError(w, http.StatusBadRequest, "llama_server_url is required")
		return
	}

	result := cs.FetchModels(req.LlamaServerURL)
	jsonResponse(w, http.StatusOK, result)
}

// ResolveModel resolves a model selection to the actual server URL and model name.
// Resolution order: task override -> highest-priority provider's default model.
// If the task has explicit llama_server_url/model_name set, those take priority.
// Otherwise, use the first provider (highest priority) and its default model.
// Fallback: first available model of the first provider.
func ResolveModel(taskLlamaURL, taskModelName, defaultProviderName string, cfg *Config) (serverURL, modelName string) {
	// 1. Task-level override (explicit values set on the task)
	if taskLlamaURL != "" {
		serverURL = taskLlamaURL
	}
	if taskModelName != "" {
		modelName = taskModelName
	}
	// If both are set on the task, use them directly
	if serverURL != "" && modelName != "" {
		return serverURL, modelName
	}

	// 2. Use highest-priority provider (first in list)
	if len(cfg.Providers) > 0 {
		p := cfg.Providers[0]
		if serverURL == "" {
			serverURL = p.URL
		}
		// Use provider's default model if set
		if modelName == "" && p.DefaultModel != "" {
			for _, m := range p.Models {
				if m.Name == p.DefaultModel {
					modelName = m.Name
					break
				}
			}
		}
		// Fallback: use the first available model
		if modelName == "" && len(p.Models) > 0 {
			modelName = p.Models[0].Name
		}
	}

	return serverURL, modelName
}

// HandleTestOverride handles POST /api/test/override.
// Sets or clears an in-memory config override without modifying the database.
// Preserves providers and default_provider from the database.
func (cs *ConfigStore) HandleTestOverride(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			LlamaServerURL string `json:"llama_server_url"`
			ModelName      string `json:"model_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// Get current config to preserve workspace_dir and providers
		current, err := cs.GetConfig()
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "failed to get current config")
			return
		}
		cs.SetOverride(&Config{
			LlamaServerURL:  req.LlamaServerURL,
			ModelName:       req.ModelName,
			WorkspaceDir:    current.WorkspaceDir,
			Providers:       current.Providers,
			DefaultProvider: current.DefaultProvider,
		})
		jsonResponse(w, http.StatusOK, map[string]string{"status": "override set"})
	} else if r.Method == http.MethodDelete {
		cs.ClearOverride()
		jsonResponse(w, http.StatusOK, map[string]string{"status": "override cleared"})
	} else {
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

// DefaultReasoningEffort is the default reasoning effort for models without an explicit setting.
const DefaultReasoningEffort = "medium"

// UnmarshalJSON implements backward-compatible JSON unmarshaling for Provider.
// Accepts both the new format (models as []ModelConfig) and the old format (models as []string).
func (p *Provider) UnmarshalJSON(data []byte) error {
	// Use an alias type to avoid infinite recursion
	type providerAlias Provider
	var raw struct {
		*providerAlias
		Models json.RawMessage `json:"models"`
	}
	raw.providerAlias = (*providerAlias)(p)

	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Try new format: []ModelConfig
	var newModels []ModelConfig
	if err := json.Unmarshal(raw.Models, &newModels); err == nil && len(newModels) > 0 {
		// Check if at least one entry has a name field (distinguish from []string)
		if newModels[0].Name != "" || len(newModels) == 0 {
			p.Models = newModels
			// Set default reasoning effort for models without one
			for i := range p.Models {
				if p.Models[i].ReasoningEffort == "" {
					p.Models[i].ReasoningEffort = DefaultReasoningEffort
				}
			}
			return nil
		}
	}

	// Try old format: []string
	var oldModels []string
	if err := json.Unmarshal(raw.Models, &oldModels); err == nil {
		p.Models = make([]ModelConfig, len(oldModels))
		for i, name := range oldModels {
			p.Models[i] = ModelConfig{Name: name, ReasoningEffort: DefaultReasoningEffort}
		}
		return nil
	}

	// Fallback: empty array
	if err := json.Unmarshal(raw.Models, &[]ModelConfig{}); err == nil {
		p.Models = []ModelConfig{}
		return nil
	}

	return fmt.Errorf("invalid models format")
}

// GetModelReasoningEffort looks up the reasoning effort for a model by name across all providers.
// Returns DefaultReasoningEffort if the model is not found.
func (cs *ConfigStore) GetModelReasoningEffort(modelName string) string {
	cfg, err := cs.GetConfig()
	if err != nil {
		return DefaultReasoningEffort
	}
	for _, provider := range cfg.Providers {
		for _, m := range provider.Models {
			if m.Name == modelName {
				if m.ReasoningEffort == "" {
					return DefaultReasoningEffort
				}
				return m.ReasoningEffort
			}
		}
	}
	return DefaultReasoningEffort
}