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
	endpointLimitsRaw, err := cs.store.GetConfigValue("endpoint_limits")
	if err != nil {
		return nil, err
	}

	endpointLimits := make(map[string]int)
	if endpointLimitsRaw != "" {
		if err := json.Unmarshal([]byte(endpointLimitsRaw), &endpointLimits); err != nil {
			return nil, err
		}
	}

	return &Config{
		LlamaServerURL: llamaURL,
		ModelName:      modelName,
		WorkspaceDir:   workspaceDir,
		EndpointLimits: endpointLimits,
	}, nil
}

// UpdateConfig persists the full server configuration.
func (cs *ConfigStore) UpdateConfig(c *Config) error {
	endpointLimitsJSON, err := json.Marshal(c.EndpointLimits)
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
	if err := cs.store.SetConfigValue("endpoint_limits", string(endpointLimitsJSON)); err != nil {
		return err
	}
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
		if limits, ok := updates["endpoint_limits"]; ok {
			if limits != nil {
				limitsJSON, _ := json.Marshal(limits)
				json.Unmarshal(limitsJSON, &current.EndpointLimits)
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

// HandleTestOverride handles POST /api/test/override.
// Sets or clears an in-memory config override without modifying the database.
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
		// Get current config to preserve workspace_dir and endpoint_limits
		current, err := cs.store.GetConfigValue("workspace_dir")
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "failed to get workspace_dir")
			return
		}
		cs.SetOverride(&Config{
			LlamaServerURL: req.LlamaServerURL,
			ModelName:      req.ModelName,
			WorkspaceDir:   current,
			EndpointLimits: make(map[string]int),
		})
		jsonResponse(w, http.StatusOK, map[string]string{"status": "override set"})
	} else if r.Method == http.MethodDelete {
		cs.ClearOverride()
		jsonResponse(w, http.StatusOK, map[string]string{"status": "override cleared"})
	} else {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}