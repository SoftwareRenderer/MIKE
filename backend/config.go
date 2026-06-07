package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	defaultModel, err := cs.store.GetConfigValue("default_model")
	if err != nil {
		return nil, err
	}

	return &Config{
		LlamaServerURL:  llamaURL,
		ModelName:       modelName,
		WorkspaceDir:    workspaceDir,
		Providers:       providers,
		DefaultProvider: defaultProvider,
		DefaultModel:    defaultModel,
	}, nil
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
	if err := cs.store.SetConfigValue("default_model", c.DefaultModel); err != nil {
		return err
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
		if dm, ok := updates["default_model"]; ok {
			current.DefaultModel = dm.(string)
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
// Resolution order: task override -> default provider.
// If the task has explicit llama_server_url/model_name set, those take priority.
// Otherwise, if default_provider is set, look up the provider and use its URL.
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

	// 2. Default provider
	if defaultProviderName != "" && len(cfg.Providers) > 0 {
		for _, p := range cfg.Providers {
			if p.Name == defaultProviderName {
				if serverURL == "" {
					serverURL = p.URL
				}
				// If no model name yet, use the default model if it belongs to this provider
				if modelName == "" && cfg.DefaultModel != "" {
					for _, m := range p.Models {
						if m == cfg.DefaultModel {
							modelName = m
							break
						}
					}
				}
				// Fallback: use the first available model
				if modelName == "" && len(p.Models) > 0 {
					modelName = p.Models[0]
				}
				break
			}
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