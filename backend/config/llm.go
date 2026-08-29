package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
	baseURL := strings.TrimRight(req.LlamaServerURL, "/")
	url := baseURL + "/chat/completions"

	model := req.ModelName
	if model == "" {
		model = "test"
	}
	reqBody := map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1,
	}
	var adapterHeaders map[string]string
	if cs.llmAdapter != nil {
		if headers, _, ok := cs.llmAdapter.Adapt(baseURL, model, "", reqBody); ok {
			adapterHeaders = headers
		}
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
	for k, v := range adapterHeaders {
		httpReq.Header.Set(k, v)
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

// ResolveAPIKey returns the effective API key for a provider URL, substituting
// the stored key when the client sends the masked placeholder or an empty value.
func (cs *ConfigStore) ResolveAPIKey(serverURL string, providedKey string) string {
	if providedKey != "" && providedKey != maskedAPIKey {
		return providedKey
	}
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

// HandleTestConnection handles POST /api/config/test-connection.
func (cs *ConfigStore) HandleTestConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req TestConnectionRequest
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	if req.LlamaServerURL == "" {
		httputil.JSONError(w, http.StatusBadRequest, "llama_server_url is required")
		return
	}

	// A masked/empty key means "use the stored key for this provider".
	req.APIKey = cs.ResolveAPIKey(req.LlamaServerURL, req.APIKey)

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
	baseURL := strings.TrimRight(req.LlamaServerURL, "/")
	url := baseURL + "/models"

	client := &http.Client{Timeout: 60 * time.Second}
	httpReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return FetchModelsResponse{Models: nil, Error: fmt.Sprintf("Failed to build request: %v", err)}
	}
	if req.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	}
	// Provider adaptation (headers only — /models has no request body).
	if cs.llmAdapter != nil {
		if headers, _, ok := cs.llmAdapter.Adapt(baseURL, "", "", map[string]any{}); ok {
			for k, v := range headers {
				httpReq.Header.Set(k, v)
			}
		}
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

// DiscoverContextWindow queries the llama-swap upstream endpoint for a model's
// context window; nil if the server is not llama-swap or the query fails.
// llama-swap's own /v1/models omits these fields, so the per-model upstream
// endpoint is the only source. Covers llama.cpp (meta.n_ctx) and vLLM
// (max_model_len).
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
			MaxModelLen int `json:"max_model_len"` // vLLM
			Meta        struct {
				NContext *int `json:"n_ctx"` // llama.cpp
			} `json:"meta"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &upstreamResp); err != nil {
		return nil
	}
	if len(upstreamResp.Data) > 0 {
		m := upstreamResp.Data[0]
		// n_ctx first: on llama.cpp it is the runtime context (-c flag), which
		// beats any advertised model maximum.
		if m.Meta.NContext != nil {
			return m.Meta.NContext
		}
		if m.MaxModelLen > 0 {
			return &m.MaxModelLen
		}
	}
	return nil
}

// extractContextWindow pulls the context window from LLM response fields.
// Priority: max_model_len > meta.n_ctx > max_tokens > metadata keys.
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

// GetModelReasoningEffort looks up the reasoning effort for a model by name across all providers.
// Returns types.DefaultReasoningEffort if the model is not found or has no explicit effort.
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

// GetProviderIDByURL returns the provider's stable ID (as a string) for a
// server URL, or "" if no match. Used to record the provider on token rows.
func (cs *ConfigStore) GetProviderIDByURL(serverURL string) string {
	p := cs.ResolveProviderByURL(serverURL)
	if p == nil || p.ID == 0 {
		return ""
	}
	return strconv.Itoa(p.ID)
}

// ResolveProviderByURL returns a copy of the provider matching a server URL, or nil.
func (cs *ConfigStore) ResolveProviderByURL(serverURL string) *types.Provider {
	cfg, err := cs.GetConfig()
	if err != nil {
		return nil
	}
	serverURL = strings.TrimRight(serverURL, "/")
	for i := range cfg.Providers {
		if strings.TrimRight(cfg.Providers[i].URL, "/") == serverURL {
			p := cfg.Providers[i]
			return &p
		}
	}
	return nil
}

// ResolveProvider returns a copy of the provider for a reference: the stable
// integer ID (as a string) for LLM providers, or the name for agent providers.
// Agent providers are registry-derived and not persisted, so name references
// also fall back to the agent registry (enabled agent extensions).
func (cs *ConfigStore) ResolveProvider(ref string) *types.Provider {
	if ref == "" {
		return nil
	}
	cfg, err := cs.GetConfig()
	if err != nil {
		return nil
	}
	if id, aerr := strconv.Atoi(ref); aerr == nil && id != 0 {
		for i := range cfg.Providers {
			if cfg.Providers[i].ID == id {
				p := cfg.Providers[i]
				return &p
			}
		}
		return nil
	}
	// Non-integer references are agent provider names.
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == ref {
			p := cfg.Providers[i]
			return &p
		}
	}
	for _, ap := range cs.agentProviders() {
		if ap.Name == ref {
			p := ap
			return &p
		}
	}
	return nil
}

// ResolveProviderPricing returns the effective per-1M-token prices (USD) for a
// provider and model: model overrides win over provider defaults; cache_write
// replaces input_price for non-cached input. Zeros for agent/unknown providers.
func ResolveProviderPricing(p *types.Provider, modelName string) (inputRate, cacheReadRate, outputRate float64) {
	if p == nil || p.EffectiveKind() != types.ProviderKindLLM {
		return 0, 0, 0
	}
	inputRate = p.InputPrice
	cacheReadRate = p.CacheReadPrice
	outputRate = p.OutputPrice
	cacheWrite := p.CacheWritePrice
	for i := range p.Models {
		m := &p.Models[i]
		if m.Name != modelName {
			continue
		}
		if m.InputPrice != nil {
			inputRate = *m.InputPrice
		}
		if m.CacheReadPrice != nil {
			cacheReadRate = *m.CacheReadPrice
		}
		if m.OutputPrice != nil {
			outputRate = *m.OutputPrice
		}
		if m.CacheWritePrice != nil {
			cacheWrite = *m.CacheWritePrice
		}
		break
	}
	// cache_write_price overrides input_price for non-cached input tokens.
	if cacheWrite > 0 {
		inputRate = cacheWrite
	}
	return inputRate, cacheReadRate, outputRate
}

// EstimateCallCost returns the API cost (USD) of one LLM call from token usage.
// Returns 0 when the provider has no per-token pricing, is an agent provider
// (they report their own cost), is unknown, or usage is nil.
func (cs *ConfigStore) EstimateCallCost(serverURL, modelName string, usage *types.ChatUsage) float64 {
	if usage == nil {
		return 0
	}
	inputRate, cacheReadRate, outputRate := ResolveProviderPricing(cs.ResolveProviderByURL(serverURL), modelName)
	if inputRate <= 0 && cacheReadRate <= 0 && outputRate <= 0 {
		return 0
	}
	cached := usage.PromptTokensDetails.CachedTokens
	nonCachedInput := usage.PromptTokens - cached
	if nonCachedInput < 0 {
		nonCachedInput = 0
	}
	return (float64(nonCachedInput)*inputRate +
		float64(cached)*cacheReadRate +
		float64(usage.CompletionTokens)*outputRate) / 1_000_000
}

// GetUtility returns the configured Utility provider name and model.
func (cs *ConfigStore) GetUtility() (providerName, model string) {
	cfg, err := cs.GetConfig()
	if err != nil {
		return "", ""
	}
	return cfg.UtilityProvider, cfg.UtilityModel
}

// providerInt returns the matching provider's value from get, or def if the
// provider is not found (matched by server URL, ignoring a trailing slash) or
// the value is invalid (< 1). If GetConfig fails, def is returned.
func (cs *ConfigStore) providerInt(serverURL string, get func(p types.Provider) int, def int) int {
	cfg, err := cs.GetConfig()
	if err != nil {
		return def
	}
	serverURL = strings.TrimRight(serverURL, "/")
	for _, provider := range cfg.Providers {
		if strings.TrimRight(provider.URL, "/") == serverURL {
			if v := get(provider); v >= 1 {
				return v
			}
			return def
		}
	}
	return def
}

// GetProviderConcurrency returns the concurrency limit for the provider matching the given server URL.
// Returns 1 (default minimum) if no matching provider is found or concurrency is not set.
func (cs *ConfigStore) GetProviderConcurrency(serverURL string) int {
	return cs.providerInt(serverURL, func(p types.Provider) int { return p.Concurrency }, 1)
}

// GetProviderMaxRequestsPerSecond returns the max new-request-start rate (requests/second)
// for the provider matching the given server URL. Returns 0 (unlimited) if the provider
// is not found or the value is unset/invalid.
func (cs *ConfigStore) GetProviderMaxRequestsPerSecond(serverURL string) int {
	return cs.providerInt(serverURL, func(p types.Provider) int { return p.MaxRequestsPerSecond }, 0)
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
