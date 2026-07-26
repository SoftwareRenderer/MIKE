package config

import (
	"bytes"
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
	APIKey         string `json:"api_key"`
}

// TestConnectionResponse is the result of a connection test.
type TestConnectionResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// TestConnection sends a minimal chat request to the LLM server and returns the result.
func (cs *ConfigStore) TestConnection(req TestConnectionRequest) TestConnectionResponse {
	url := strings.TrimRight(req.LlamaServerURL, "/") + "/chat/completions"

	model := req.ModelName
	if model == "" {
		model = "test"
	}
	reqBody := map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return TestConnectionResponse{Success: false, Message: fmt.Sprintf("Failed to build request: %v", err)}
	}

	client := &http.Client{Timeout: 15 * time.Second}
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(payload))
	if err != nil {
		return TestConnectionResponse{Success: false, Message: fmt.Sprintf("Failed to build request: %v", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return TestConnectionResponse{Success: false, Message: fmt.Sprintf("Connection failed: %v", err)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return TestConnectionResponse{Success: true, Message: "Connection successful!"}
	}
	// Parse error response for better feedback
	if len(body) > 0 {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    int    `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
			return TestConnectionResponse{Success: false, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, errResp.Error.Message)}
		}
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
	APIKey         string `json:"api_key"`
}

// FetchedModel represents a model returned from the LLM's /models endpoint.
type FetchedModel struct {
	Name          string `json:"name"`
	ContextWindow *int   `json:"context_window,omitempty"` // nil if not reported by LLM
}

// FetchModelsResponse is the result of fetching models from an LLM server.
type FetchModelsResponse struct {
	Models []FetchedModel `json:"models"`
	Error  string         `json:"error,omitempty"`
}

// FetchModels calls GET {url}/v1/models on an OpenAI-compatible endpoint and returns model metadata.
func (cs *ConfigStore) FetchModels(req FetchModelsRequest) FetchModelsResponse {
	url := strings.TrimRight(req.LlamaServerURL, "/") + "/models"

	client := &http.Client{Timeout: 60 * time.Second}
	httpReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return FetchModelsResponse{Models: nil, Error: fmt.Sprintf("Failed to build request: %v", err)}
	}
	if req.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	}
	resp, err := client.Do(httpReq)
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
			ID          string         `json:"id"`
			MaxModelLen int            `json:"max_model_len"` // llama.cpp / vLLM
			MaxTokens   int            `json:"max_tokens"`    // some OpenAI-compatible servers
			Metadata    map[string]int `json:"metadata"`      // generic metadata map
			Meta        struct {
				NContext *int `json:"n_ctx"` // llama.cpp context window
			} `json:"meta"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return FetchModelsResponse{Models: nil, Error: fmt.Sprintf("Failed to parse response: %v", err)}
	}

	models := make([]FetchedModel, 0, len(oaiResp.Data))
	for _, m := range oaiResp.Data {
		if m.ID == "" {
			continue
		}
		ctx := extractContextWindow(m.MaxModelLen, m.MaxTokens, m.Meta.NContext, m.Metadata)
		models = append(models, FetchedModel{Name: m.ID, ContextWindow: ctx})
	}
	return FetchModelsResponse{Models: models}
}

// DiscoverContextWindow queries the llama-swap upstream endpoint to find a model's context window.
// Returns nil if the server is not llama-swap or the query fails/times out.
// This is called just-in-time before LLM calls when context window is not yet known.
func (cs *ConfigStore) DiscoverContextWindow(modelName, providerURL string) *int {
	// Check if this is a llama-swap server
	baseURL := strings.TrimRight(providerURL, "/")
	modelsURL := baseURL + "/models"

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(modelsURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var oaiResp struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return nil
	}

	// Only query upstream for llama-swap servers
	isLlamaSwap := false
	for _, m := range oaiResp.Data {
		if m.OwnedBy == "llama-swap" {
			isLlamaSwap = true
			break
		}
	}
	if !isLlamaSwap {
		return nil
	}

	// Query upstream for this specific model
	upstreamBase := strings.TrimSuffix(baseURL, "/v1")
	upstreamURL := fmt.Sprintf("%s/upstream/%s/v1/models", upstreamBase, modelName)

	resp, err = client.Get(upstreamURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)

	var upstreamResp struct {
		Data []struct {
			Meta struct {
				NContext *int `json:"n_ctx"`
			} `json:"meta"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &upstreamResp); err != nil {
		return nil
	}
	if len(upstreamResp.Data) > 0 && upstreamResp.Data[0].Meta.NContext != nil {
		return upstreamResp.Data[0].Meta.NContext
	}
	return nil
}

// extractContextWindow extracts the context window from various LLM response fields.
// Priority: max_model_len > meta.n_ctx > max_tokens > metadata["max_model_len"] > metadata["max_tokens"] > metadata["context_length"]
func extractContextWindow(maxModelLen, maxTokens int, nContext *int, metadata map[string]int) *int {
	if maxModelLen > 0 {
		return &maxModelLen
	}
	if nContext != nil && *nContext > 0 {
		return nContext
	}
	if maxTokens > 0 {
		return &maxTokens
	}
	for _, key := range []string{"max_model_len", "max_tokens", "context_length", "n_ctx"} {
		if v, ok := metadata[key]; ok && v > 0 {
			return &v
		}
	}
	return nil
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

	result := cs.FetchModels(req)
	httputil.JSONResponse(w, http.StatusOK, result)
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

// GetProviderConcurrency returns the concurrency limit for the provider matching the given server URL.
// Returns 1 (default minimum) if no matching provider is found or concurrency is not set.
func (cs *ConfigStore) GetProviderConcurrency(serverURL string) int {
	cfg, err := cs.GetConfig()
	if err != nil {
		return 1
	}
	serverURL = strings.TrimRight(serverURL, "/")
	for _, provider := range cfg.Providers {
		if strings.TrimRight(provider.URL, "/") == serverURL {
			if provider.Concurrency < 1 {
				return 1
			}
			return provider.Concurrency
		}
	}
	return 1
}

// IsAgentProvider returns true if the provider matching the given server URL is an agent (non-LLM kind).
// Also returns true for agent:// URLs (agent extensions).
// Returns false if no matching provider is found.
func (cs *ConfigStore) IsAgentProvider(serverURL string) bool {
	// Handle agent:// URLs (agent extensions)
	if strings.HasPrefix(serverURL, "agent://") {
		return true
	}
	cfg, err := cs.GetConfig()
	if err != nil {
		return false
	}
	serverURL = strings.TrimRight(serverURL, "/")
	for _, provider := range cfg.Providers {
		if strings.TrimRight(provider.URL, "/") == serverURL {
			return provider.IsAgent()
		}
	}
	return false
}

// GetModelContextWindow looks up the context window for a model by name and provider URL.
// Returns nil if the context window is not known (not reported by the LLM).
func (cs *ConfigStore) GetModelContextWindow(modelName, providerURL string) *int {
	cfg, err := cs.GetConfig()
	if err != nil {
		return nil
	}
	providerURL = strings.TrimRight(providerURL, "/")
	for _, provider := range cfg.Providers {
		if strings.TrimRight(provider.URL, "/") != providerURL {
			continue
		}
		for _, m := range provider.Models {
			if m.Name == modelName {
				return m.ContextWindow
			}
		}
	}
	return nil
}
