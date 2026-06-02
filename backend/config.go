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
		var cfg Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := cs.UpdateConfig(&cfg); err != nil {
			jsonError(w, http.StatusInternalServerError, "failed to update config")
			return
		}
		jsonResponse(w, http.StatusOK, cfg)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}