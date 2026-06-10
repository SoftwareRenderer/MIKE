package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"kanban/backend/httputil"
	"kanban/backend/types"
)

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
		"model":      "test",
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
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
		httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.LlamaServerURL == "" {
		httputil.JSONError(w, http.StatusBadRequest, "llama_server_url is required")
		return
	}

	result := cs.TestConnection(req)
	// Always return 200 so UI can display the message
	httputil.JSONResponse(w, http.StatusOK, result)
}

// FetchModelsRequest is the body for POST /api/config/fetch-models.
type FetchModelsRequest struct {
	LlamaServerURL string `json:"llama_server_url"`
}

// FetchModelsResponse is the result of fetching models from an LLM server.
type FetchModelsResponse struct {
	Models []string `json:"models"`
	Error  string   `json:"error,omitempty"`
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
		httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.LlamaServerURL == "" {
		httputil.JSONError(w, http.StatusBadRequest, "llama_server_url is required")
		return
	}

	result := cs.FetchModels(req.LlamaServerURL)
	httputil.JSONResponse(w, http.StatusOK, result)
}

// ResolveModel resolves a model selection to the actual server URL and model name.
// Resolution order: task override -> highest-priority provider's default model.
// If the task has explicit llama_server_url/model_name set, those take priority.
// Otherwise, use the first provider (highest priority) and its default model.
// Fallback: first available model of the first provider.
func ResolveModel(taskLlamaURL, taskModelName, defaultProviderName string, cfg *types.Config) (serverURL, modelName string) {
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

// GetModelReasoningEffort looks up the reasoning effort for a model by name across all providers.
// Returns types.DefaultReasoningEffort if the model is not found.
func (cs *ConfigStore) GetModelReasoningEffort(modelName string) string {
	cfg, err := cs.GetConfig()
	if err != nil {
		return types.DefaultReasoningEffort
	}
	for _, provider := range cfg.Providers {
		for _, m := range provider.Models {
			if m.Name == modelName {
				if m.ReasoningEffort == "" {
					return types.DefaultReasoningEffort
				}
				return m.ReasoningEffort
			}
		}
	}
	return types.DefaultReasoningEffort
}

// GetProviderNameByURL returns the display name of the provider matching the given server URL.
// Returns empty string if no matching provider is found.
func (cs *ConfigStore) GetProviderNameByURL(serverURL string) string {
	cfg, err := cs.GetConfig()
	if err != nil {
		return ""
	}
	serverURL = strings.TrimRight(serverURL, "/")
	for _, provider := range cfg.Providers {
		if strings.TrimRight(provider.URL, "/") == serverURL {
			if provider.DisplayName != "" {
				return provider.DisplayName
			}
			return provider.Name
		}
	}
	return ""
}

