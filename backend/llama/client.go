package llama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"kanban/backend/types"
)

// Reasoning effort to token budget mapping.
var reasoningEffortTokens = map[string]int{
	"off":    -1,
	"low":    512,
	"medium": 2048,
	"high":   8192,
	"max":    -1, // unlimited
}

// LlamaClient handles communication with llama.cpp OpenAI-compatible API.
type LlamaClient struct {
	baseURL         string
	modelName       string
	reasoningEffort string
	apiKey          string
	httpClient      *http.Client
}

// NewLlamaClient creates a new LlamaClient.
func NewLlamaClient(baseURL, modelName, reasoningEffort, apiKey string) *LlamaClient {
	if reasoningEffort == "" {
		reasoningEffort = types.DefaultReasoningEffort
	}
	return &LlamaClient{
		baseURL:         strings.TrimRight(baseURL, "/"),
		modelName:       modelName,
		reasoningEffort: reasoningEffort,
		apiKey:          apiKey,
		httpClient:      &http.Client{Timeout: 0},
	}
}

// setAuthHeader sets the Authorization header on a request if an API key is configured.
func (c *LlamaClient) setAuthHeader(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

// toAPIMessage converts a ChatMessage to the format expected by the LLM API.
// If ContentParts is set, content is serialized as an array of parts (multi-modal).
// Uses standard OpenAI format: {type: "image_url", image_url: {url: "data:..."}}
func toAPIMessage(m types.ChatMessage) map[string]interface{} {
	result := map[string]interface{}{
		"role": m.Role,
	}
	if len(m.ContentParts) > 0 {
		// Multi-modal: serialize as array of parts
		parts := make([]interface{}, len(m.ContentParts))
		for i, p := range m.ContentParts {
			if p.Type == "text" {
				parts[i] = map[string]interface{}{"type": "text", "text": p.Text}
			} else if p.ImageSource != nil {
				// Convert internal format to OpenAI data: URI
				dataURL := "data:" + p.ImageSource.MediaType + ";base64," + p.ImageSource.Data
				parts[i] = map[string]interface{}{
					"type":      "image_url",
					"image_url": map[string]interface{}{"url": dataURL},
				}
			} else if p.ImageURL != nil {
				parts[i] = map[string]interface{}{
					"type":      "image_url",
					"image_url": map[string]interface{}{"url": p.ImageURL.URL},
				}
			}
		}
		result["content"] = parts
	} else {
		result["content"] = m.Content
	}
	if len(m.ToolCalls) > 0 {
		result["tool_calls"] = m.ToolCalls
	}
	if m.ToolCallID != "" {
		result["tool_call_id"] = m.ToolCallID
	}
	if m.ReasoningContent != "" {
		result["reasoning_content"] = m.ReasoningContent
	}
	return result
}

// toAPIMessages converts a slice of ChatMessages to the API format.
func toAPIMessages(messages []types.ChatMessage) []map[string]interface{} {
	result := make([]map[string]interface{}, len(messages))
	for i, m := range messages {
		result[i] = toAPIMessage(m)
	}
	return result
}

// ChatStream sends a chat completion request with streaming.
func (c *LlamaClient) ChatStream(ctx context.Context, messages []types.ChatMessage, tools []types.ChatTool, cb types.StreamCallback) (*types.ChatUsage, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("llama server URL not configured")
	}

	url := c.baseURL + "/chat/completions"

	reqBody := map[string]interface{}{
		"model":            c.modelName,
		"messages":         toAPIMessages(messages),
		"stream":           true,
		"temperature":      0.7,
		"max_tokens":       8192,
		"reasoning_format": "auto",
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
		reqBody["tool_choice"] = "auto"
	} else {
		reqBody["tool_choice"] = "none"
	}

	if c.reasoningEffort != "off" {
		budget, ok := reasoningEffortTokens[c.reasoningEffort]
		if ok {
			reqBody["thinking_budget_tokens"] = budget
			reqBody["reasoning_control"] = true
			reqBody["chat_template_kwargs"] = map[string]interface{}{
				"enable_thinking": true,
			}
		}
	} else {
		reqBody["chat_template_kwargs"] = map[string]interface{}{
			"enable_thinking": false,
		}
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post to llama: %w", err)
	}
	defer resp.Body.Close()

	// Close body on context cancellation to unblock the scanner.
	go func() {
		<-ctx.Done()
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("llama error %d: %s", resp.StatusCode, string(body))
	}

	var usage *types.ChatUsage
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return usage, ctx.Err()
		default:
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var cr types.ChatResponse
		if err := json.Unmarshal([]byte(data), &cr); err != nil {
			continue
		}

		if cr.Usage != nil {
			usage = cr.Usage
			if cr.Timings != nil {
				usage.PromptMs = int64(cr.Timings.PromptMs)
				usage.PredictedMs = int64(cr.Timings.PredictedMs)
				// Fill cache tokens from timings if not in usage
				if usage.PromptTokensDetails.CachedTokens == 0 && cr.Timings.CacheN > 0 {
					usage.PromptTokensDetails.CachedTokens = cr.Timings.CacheN
				}
			}
			log.Printf("[llama] usage: prompt_tokens=%d, completion_tokens=%d, cache_tokens=%d, prompt_ms=%d, predicted_ms=%d",
				usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, usage.PromptMs, usage.PredictedMs)
		} else if cr.Timings != nil {
			// Streaming response without usage object (e.g., llama.cpp) - build from timings
			if usage == nil {
				usage = &types.ChatUsage{}
			}
			// prompt_tokens = processed (prompt_n) + cached (cache_n)
			usage.PromptTokens = cr.Timings.PromptN + cr.Timings.CacheN
			usage.CompletionTokens = cr.Timings.PredictedN
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			usage.PromptTokensDetails.CachedTokens = cr.Timings.CacheN
			usage.PromptMs = int64(cr.Timings.PromptMs)
			usage.PredictedMs = int64(cr.Timings.PredictedMs)
		}

		for _, choice := range cr.Choices {
			cb(choice)
		}
	}

	if usage != nil {
		log.Printf("[llama] final usage: prompt_tokens=%d, completion_tokens=%d, cache_tokens=%d, prompt_ms=%d, predicted_ms=%d",
			usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, usage.PromptMs, usage.PredictedMs)
	} else {
		log.Printf("[llama] no usage data received")
	}
	return usage, scanner.Err()
}

// ChatCompletion sends a non-streaming chat completion request and returns the usage data and full text response.
// Used for simple queries like title generation where streaming is not needed.
func (c *LlamaClient) ChatCompletion(ctx context.Context, messages []types.ChatMessage) (*types.ChatUsage, string, error) {
	if c.baseURL == "" {
		return nil, "", fmt.Errorf("llama server URL not configured")
	}

	url := c.baseURL + "/chat/completions"

	reqBody := map[string]interface{}{
		"model":            c.modelName,
		"messages":         toAPIMessages(messages),
		"stream":           false,
		"temperature":      0.3,
		"max_tokens":       128,
		"tool_choice":      "none",
		"reasoning_format": "auto",
	}
	if c.reasoningEffort != "off" {
		budget, ok := reasoningEffortTokens[c.reasoningEffort]
		if ok {
			reqBody["thinking_budget_tokens"] = budget
			reqBody["reasoning_control"] = true
			reqBody["chat_template_kwargs"] = map[string]interface{}{
				"enable_thinking": true,
			}
		}
	} else {
		reqBody["chat_template_kwargs"] = map[string]interface{}{
			"enable_thinking": false,
		}
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("post to llama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("llama error %d: %s", resp.StatusCode, string(body))
	}

	var result types.ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, "", fmt.Errorf("decode response: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, "", fmt.Errorf("no choices in response")
	}

	content := result.Choices[0].Message.Content
	if content == nil {
		return nil, "", fmt.Errorf("empty content in response")
	}

	// Extract usage data
	var usage *types.ChatUsage
	if result.Usage != nil {
		usage = result.Usage
		if result.Timings != nil {
			usage.PromptMs = int64(result.Timings.PromptMs)
			usage.PredictedMs = int64(result.Timings.PredictedMs)
			if usage.PromptTokensDetails.CachedTokens == 0 && result.Timings.CacheN > 0 {
				usage.PromptTokensDetails.CachedTokens = result.Timings.CacheN
			}
		}
	} else if result.Timings != nil {
		usage = &types.ChatUsage{
			PromptTokens:     result.Timings.PromptN + result.Timings.CacheN,
			CompletionTokens: result.Timings.PredictedN,
			TotalTokens:      result.Timings.PromptN + result.Timings.CacheN + result.Timings.PredictedN,
			PromptMs:         int64(result.Timings.PromptMs),
			PredictedMs:      int64(result.Timings.PredictedMs),
		}
		usage.PromptTokensDetails.CachedTokens = result.Timings.CacheN
	}

	if usage != nil {
		log.Printf("[llama] non-streaming usage: prompt_tokens=%d, completion_tokens=%d, cache_tokens=%d, prompt_ms=%d, predicted_ms=%d",
			usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, usage.PromptMs, usage.PredictedMs)
	}

	return usage, strings.TrimSpace(*content), nil
}

// HealthCheck pings the llama server.
func (c *LlamaClient) HealthCheck() error {
	url := c.baseURL + "/chat/completions"
	reqBody := map[string]interface{}{
		"model":      c.modelName,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1,
	}
	data, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(req)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 500 {
		return fmt.Errorf("llama server error: %d", resp.StatusCode)
	}
	return nil
}

// MergeToolCalls merges streaming tool call deltas by index into complete tool calls.
func MergeToolCalls(existing []types.ChatToolCall, deltas []types.ChatToolCall) []types.ChatToolCall {
	idxMap := make(map[int]int)
	for i, tc := range existing {
		idxMap[tc.Index] = i
	}

	for _, delta := range deltas {
		pos, ok := idxMap[delta.Index]
		isNew := !ok
		if isNew {
			pos = len(existing)
			idxMap[delta.Index] = pos
			existing = append(existing, delta)
		}
		if !isNew {
			if delta.ID != "" {
				existing[pos].ID = delta.ID
			}
			if delta.Function.Name != "" {
				existing[pos].Function.Name += delta.Function.Name
			}
			if delta.Function.Arguments != "" {
				existing[pos].Function.Arguments += delta.Function.Arguments
			}
			if delta.Type != "" {
				existing[pos].Type = delta.Type
			}
		}
	}

	return existing
}
