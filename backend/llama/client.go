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
	"sync/atomic"
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
	readIdleTimeout time.Duration // 0 means default 30s
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
		readIdleTimeout: 30 * time.Second,
	}
}

// SetReadIdleTimeout sets the read idle timeout for streaming responses.
// If no data arrives for this duration, the stream is aborted.
func (c *LlamaClient) SetReadIdleTimeout(d time.Duration) {
	c.readIdleTimeout = d
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
func toAPIMessage(m types.ChatMessage) map[string]any {
	result := map[string]any{
		"role": m.Role,
	}
	if len(m.ContentParts) > 0 {
		parts := make([]any, len(m.ContentParts))
		for i, p := range m.ContentParts {
			if p.Type == "text" {
				parts[i] = map[string]any{"type": "text", "text": p.Text}
			} else if p.ImageSource != nil {
				dataURL := "data:" + p.ImageSource.MediaType + ";base64," + p.ImageSource.Data
				parts[i] = map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": dataURL},
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
	if m.Role == "assistant" && m.ReasoningContent != "" {
		result["reasoning_content"] = m.ReasoningContent
	}
	return result
}

// toAPIMessages converts a slice of ChatMessages to the API format.
func toAPIMessages(messages []types.ChatMessage) []map[string]any {
	result := make([]map[string]any, len(messages))
	for i, m := range messages {
		result[i] = toAPIMessage(m)
	}
	return result
}

// defaultBudgetMessage is sent to the model when the reasoning budget is exhausted.
const defaultBudgetMessage = "... I'm over thinking. Answering now."

// setThinkingParams adds reasoning/thinking parameters to the request body.
func setThinkingParams(reqBody map[string]any, t *ThinkingParams) {
	reqBody["thinking_budget_tokens"] = t.BudgetTokens
	if t.BudgetTokens > 0 {
		reqBody["reasoning_control"] = true
	}
	msg := t.BudgetMessage
	if msg == "" {
		msg = defaultBudgetMessage
	}
	reqBody["reasoning_budget_message"] = msg
}

// ChatStream sends a chat completion request with streaming.
func (c *LlamaClient) ChatStream(ctx context.Context, messages []types.ChatMessage, tools []types.ChatTool, cb types.StreamCallback, onProgress types.PromptProgressCallback) (*types.ChatUsage, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("llama server URL not configured")
	}

	url := c.baseURL + "/chat/completions"

	reqBody := map[string]any{
		"model":           c.modelName,
		"messages":        toAPIMessages(messages),
		"stream":          true,
		"return_progress": true,
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
		reqBody["tool_choice"] = "auto"
	} else {
		reqBody["tool_choice"] = "none"
	}
	if c.reasoningEffort != "off" {
		if budget, ok := reasoningEffortTokens[c.reasoningEffort]; ok {
			reqBody["thinking_budget_tokens"] = budget
			reqBody["reasoning_control"] = true
		}
	} else {
		// Explicitly disable reasoning (server may enable it by default)
		reqBody["thinking_budget_tokens"] = 0
		reqBody["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
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

	callStart := time.Now()
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
	var firstTokenTime time.Time
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	// Read idle timeout: close resp.Body to unblock scanner.Scan().
	idleTimeout := c.readIdleTimeout
	if idleTimeout == 0 {
		idleTimeout = 30 * time.Second
	}
	var timedOut atomic.Bool
	readTimer := time.AfterFunc(idleTimeout, func() {
		timedOut.Store(true)
		_ = resp.Body.Close()
	})
	readTimer.Stop()

	for scanner.Scan() {
		line := scanner.Text()
		readTimer.Stop()
		readTimer.Reset(idleTimeout)
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
		}
		// Always process Timings (per-frame for llama.cpp) to get intermediate token counts.
		// Timings.PredictedN gives cumulative completion tokens generated so far.
		// This drives real-time stats events during streaming.
		if cr.Timings != nil {
			if usage == nil {
				usage = &types.ChatUsage{}
			}
			// Use Timings for per-frame token counts (updates every frame during streaming)
			usage.PromptTokens = cr.Timings.PromptN + cr.Timings.CacheN
			usage.CompletionTokens = cr.Timings.PredictedN
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			usage.PromptTokensDetails.CachedTokens = cr.Timings.CacheN
			usage.PromptMs = int64(cr.Timings.PromptMs)
			usage.PredictedMs = int64(cr.Timings.PredictedMs)
		}
		// Forward prompt processing progress (from llama.cpp return_progress).
		// Sent once per frame, before choices, so caller can emit before first token.
		if cr.PromptProgress != nil && onProgress != nil {
			onProgress(*cr.PromptProgress)
		}

		for _, choice := range cr.Choices {
			if firstTokenTime.IsZero() && hasContent(choice) {
				firstTokenTime = time.Now()
			}
			cb(choice)
		}
	}
	readTimer.Stop()

	if timedOut.Load() {
		return usage, context.DeadlineExceeded
	}

	// Estimate missing timing from wall-clock.
	if usage != nil {
		streamEnd := time.Now()
		if usage.PromptMs == 0 && !firstTokenTime.IsZero() {
			usage.PromptMs = firstTokenTime.Sub(callStart).Milliseconds()
		}
		if usage.PredictedMs == 0 && !firstTokenTime.IsZero() {
			usage.PredictedMs = streamEnd.Sub(firstTokenTime).Milliseconds()
		}
		log.Printf("[llama] final usage: prompt_tokens=%d, completion_tokens=%d, cache_tokens=%d, prompt_ms=%d, predicted_ms=%d",
			usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, usage.PromptMs, usage.PredictedMs)
	} else {
		log.Printf("[llama] no usage data received")
	}
	return usage, scanner.Err()
}

// ThinkingParams holds reasoning/thinking parameters for the LLM API.
type ThinkingParams struct {
	BudgetTokens  int
	BudgetMessage string
}

// ChatCompletionOptions holds optional parameters for ChatCompletion.
type ChatCompletionOptions struct {
	MaxTokens *int            // MaxTokens caps the response. nil = server default.
	Thinking  *ThinkingParams // Thinking overrides reasoning params. nil = client's reasoningEffort.
}

// ChatCompletion sends a non-streaming chat completion request and returns the usage data and full text response.
// Used for simple queries like title generation where streaming is not needed.
// Pass nil for opts to use all defaults.
func (c *LlamaClient) ChatCompletion(ctx context.Context, messages []types.ChatMessage, opts *ChatCompletionOptions) (*types.ChatUsage, string, error) {
	if c.baseURL == "" {
		return nil, "", fmt.Errorf("llama server URL not configured")
	}

	var maxTokens int
	var thinking *ThinkingParams
	if opts != nil {
		if opts.MaxTokens != nil {
			maxTokens = *opts.MaxTokens
		}
		thinking = opts.Thinking
	}

	url := c.baseURL + "/chat/completions"

	reqBody := map[string]any{
		"model":       c.modelName,
		"messages":    toAPIMessages(messages),
		"stream":      false,
		"tool_choice": "none",
	}
	if maxTokens > 0 {
		reqBody["max_tokens"] = maxTokens
	}
	if thinking != nil {
		setThinkingParams(reqBody, thinking)
	} else if c.reasoningEffort != "off" {
		if budget, ok := reasoningEffortTokens[c.reasoningEffort]; ok {
			reqBody["thinking_budget_tokens"] = budget
			reqBody["reasoning_control"] = true
		}
	} else {
		// Explicitly disable reasoning (server may enable it by default)
		reqBody["thinking_budget_tokens"] = 0
		reqBody["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
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

	callStart := time.Now()
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
		// Estimate missing timing from wall-clock (gen ~5x slower per token than prompt).
		totalDuration := time.Since(callStart).Milliseconds()
		if usage.PromptMs == 0 && usage.PredictedMs == 0 && totalDuration > 0 {
			promptWeight := float64(usage.PromptTokens)
			genWeight := float64(usage.CompletionTokens) * 5
			if promptWeight+genWeight > 0 {
				usage.PromptMs = int64(float64(totalDuration) * promptWeight / (promptWeight + genWeight))
				usage.PredictedMs = totalDuration - usage.PromptMs
			}
		}
		log.Printf("[llama] non-streaming usage: prompt_tokens=%d, completion_tokens=%d, cache_tokens=%d, prompt_ms=%d, predicted_ms=%d",
			usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, usage.PromptMs, usage.PredictedMs)
	}

	return usage, strings.TrimSpace(*content), nil
}

// hasContent reports whether the delta carries output (text, reasoning, or tool calls).
func hasContent(choice types.ChatChoice) bool {
	if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
		return true
	}
	if choice.Delta.Content != nil && *choice.Delta.Content != "" {
		return true
	}
	for _, tc := range choice.Delta.ToolCalls {
		if tc.ID != "" || tc.Function.Name != "" {
			return true
		}
	}
	return false
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
