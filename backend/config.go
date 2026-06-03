package backend

import (
	"encoding/json"
	"net/http"
)

// ConfigStore wraps Store for config-specific operations.
type ConfigStore struct {
	store *Store
}

// NewConfigStore creates a new ConfigStore.
func NewConfigStore(store *Store) *ConfigStore {
	return &ConfigStore{store: store}
}

// GetConfig retrieves the full server configuration.
func (cs *ConfigStore) GetConfig() (*Config, error) {
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