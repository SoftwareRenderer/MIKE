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
	httpClient      *http.Client
}

// NewLlamaClient creates a new LlamaClient.
func NewLlamaClient(baseURL, modelName, reasoningEffort string) *LlamaClient {
	if reasoningEffort == "" {
		reasoningEffort = types.DefaultReasoningEffort
	}
	return &LlamaClient{
		baseURL:         strings.TrimRight(baseURL, "/"),
		modelName:       modelName,
		reasoningEffort: reasoningEffort,
		httpClient:      &http.Client{Timeout: 0},
	}
}

// ChatStream sends a chat completion request with streaming.
func (c *LlamaClient) ChatStream(ctx context.Context, messages []types.ChatMessage, tools []types.ChatTool, cb types.StreamCallback) (*types.ChatUsage, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("llama server URL not configured")
	}

	url := c.baseURL + "/chat/completions"

	toolChoice := map[string]interface{}{"type": "auto"}
	if len(tools) == 0 {
		toolChoice = map[string]interface{}{"type": "none"}
	}

	reqBody := map[string]interface{}{
		"model":            c.modelName,
		"messages":         messages,
		"stream":           true,
		"temperature":      0.7,
		"max_tokens":       8192,
		"reasoning_format": "auto",
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
		reqBody["tool_choice"] = toolChoice
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

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post to llama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("llama error %d: %s", resp.StatusCode, string(body))
	}

	var usage *types.ChatUsage
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
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
			log.Printf("[llama] usage: prompt_tokens=%d, completion_tokens=%d, prompt_ms=%d, predicted_ms=%d",
				usage.PromptTokens, usage.CompletionTokens, usage.PromptMs, usage.PredictedMs)
		}

		for _, choice := range cr.Choices {
			cb(choice)
		}
	}

	if usage != nil {
		log.Printf("[llama] final usage: prompt_tokens=%d, completion_tokens=%d, prompt_ms=%d, predicted_ms=%d",
			usage.PromptTokens, usage.CompletionTokens, usage.PromptMs, usage.PredictedMs)
	} else {
		log.Printf("[llama] no usage data received")
	}
	return usage, scanner.Err()
}

// ChatCompletion sends a non-streaming chat completion request and returns the full text response.
// Used for simple queries like title generation where streaming is not needed.
func (c *LlamaClient) ChatCompletion(ctx context.Context, messages []types.ChatMessage) (string, error) {
	if c.baseURL == "" {
		return "", fmt.Errorf("llama server URL not configured")
	}

	url := c.baseURL + "/chat/completions"

	reqBody := map[string]interface{}{
		"model":         c.modelName,
		"messages":      messages,
		"stream":        false,
		"temperature":   0.3,
		"max_tokens":    128,
		"tool_choice":   map[string]interface{}{"type": "none"},
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("post to llama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("llama error %d: %s", resp.StatusCode, string(body))
	}

	var result types.ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	content := result.Choices[0].Delta.Content
	if content == nil {
		return "", fmt.Errorf("empty content in response")
	}

	return strings.TrimSpace(*content), nil
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

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
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
