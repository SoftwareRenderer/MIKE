package backend

import (
	"bufio"
	"kanban/backend/prompts"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
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
	reasoningEffort string // "off", "low", "medium", "high", "max"
	httpClient      *http.Client
}

// NewLlamaClient creates a new LlamaClient.
// reasoningEffort controls the thinking budget: "off", "low", "medium", "high", "max".
// If empty, defaults to "medium".
func NewLlamaClient(baseURL, modelName, reasoningEffort string) *LlamaClient {
	if reasoningEffort == "" {
		reasoningEffort = DefaultReasoningEffort
	}
	return &LlamaClient{
		baseURL:         strings.TrimRight(baseURL, "/"),
		modelName:       modelName,
		reasoningEffort: reasoningEffort,
		httpClient:      &http.Client{Timeout: 0}, // No timeout for streaming
	}
}

// ChatMessage represents a message in the conversation.
type ChatMessage struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
}

// ChatTool represents a tool definition.
type ChatTool struct {
	Type     string   `json:"type"`
	Function *ToolDef `json:"function"`
}

// ToolDef defines a tool's schema.
type ToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ChatToolCall represents a tool call (or delta) from the model.
// In streaming responses, each chunk has an Index field to identify
// which tool call the delta belongs to. The client must merge deltas
// by index to reconstruct complete tool calls.
type ChatToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	Type string `json:"type"`
}

// MergeToolCalls merges streaming tool call deltas by index into complete tool calls.
// Each delta chunk may have partial data (id in first chunk, arguments split across chunks).
// This function accumulates fields by index and returns the merged tool calls.
func MergeToolCalls(existing []ChatToolCall, deltas []ChatToolCall) []ChatToolCall {
	// Build a map of index -> position in existing slice
	idxMap := make(map[int]int) // index -> position in existing slice
	for i, tc := range existing {
		idxMap[tc.Index] = i
	}

	for _, delta := range deltas {
		pos, ok := idxMap[delta.Index]
		isNew := !ok
		if isNew {
			// New tool call - append it
			pos = len(existing)
			idxMap[delta.Index] = pos
			existing = append(existing, delta)
		}
		// Merge non-empty fields from delta into existing.
		// For new tool calls, the fields are already set by the append above,
		// so we skip merging to avoid doubling the values.
		if !isNew {
			if delta.ID != "" {
				existing[pos].ID = delta.ID
			}
			// Name is streamed as fragments - concatenate them (like arguments)
			if delta.Function.Name != "" {
				existing[pos].Function.Name += delta.Function.Name
			}
			// Arguments are streamed as fragments - concatenate them
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

// ChatChoiceDelta is the delta in a streaming response.
type ChatChoiceDelta struct {
	Content          *string        `json:"content"`
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	ToolCalls        []ChatToolCall `json:"tool_calls,omitempty"`
}

// ChatChoice is a single choice in the response.
type ChatChoice struct {
	Delta        ChatChoiceDelta `json:"delta"`
	FinishReason string          `json:"finish_reason"`
}

// ChatUsage token usage stats.
type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptMs         int `json:"prompt_ms"`
	PredictedMs      int `json:"predicted_ms"`
}

// ChatResponse is a full streaming response frame.
type ChatResponse struct {
	Choices []ChatChoice `json:"choices"`
	Usage   *ChatUsage   `json:"usage,omitempty"`
}

// StreamCallback is called for each streaming token/tool_call.
type StreamCallback func(choice ChatChoice)

// ChatStream sends a chat completion request with streaming.
func (c *LlamaClient) ChatStream(ctx context.Context, messages []ChatMessage, tools []ChatTool, cb StreamCallback) (*ChatUsage, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("llama server URL not configured")
	}

	url := c.baseURL + "/chat/completions"

	toolChoice := map[string]interface{}{"type": "auto"}
	if len(tools) == 0 {
		toolChoice = map[string]interface{}{"type": "none"}
	}

	reqBody := map[string]interface{}{
		"model":       c.modelName,
		"messages":    messages,
		"stream":      true,
		"temperature": 0.7,
		"max_tokens":  8192,
		"reasoning_format": "auto",
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
		reqBody["tool_choice"] = toolChoice
	}

	// Reasoning effort / thinking budget
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

	var usage *ChatUsage
	scanner := bufio.NewScanner(resp.Body)
	// Increase buffer for large tokens
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

		var cr ChatResponse
		if err := json.Unmarshal([]byte(data), &cr); err != nil {
			continue
		}

		if cr.Usage != nil {
			usage = cr.Usage
			log.Printf("[llama] usage: prompt_tokens=%d, completion_tokens=%d, prompt_ms=%d, predicted_ms=%d", usage.PromptTokens, usage.CompletionTokens, usage.PromptMs, usage.PredictedMs)
		}

		for _, choice := range cr.Choices {
			cb(choice)
		}
	}

	if usage != nil {
		log.Printf("[llama] final usage: prompt_tokens=%d, completion_tokens=%d, prompt_ms=%d, predicted_ms=%d", usage.PromptTokens, usage.CompletionTokens, usage.PromptMs, usage.PredictedMs)
	} else {
		log.Printf("[llama] no usage data received")
	}
	return usage, scanner.Err()
}

// HealthCheck pings the llama server.
func (c *LlamaClient) HealthCheck() error {
	url := c.baseURL + "/chat/completions"
	// Quick test with minimal request
	reqBody := map[string]interface{}{
		"model":    c.modelName,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
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

// GetToolDefinitions returns the standard tool definitions.
func GetToolDefinitions() []ChatTool {
	return []ChatTool{
		{
			Type: "function",
			Function: &ToolDef{
				Name:        "read_file",
				Description: "Read the contents of a file from the worktree",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{"type": "string", "description": "Path to the file relative to worktree root"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: &ToolDef{
				Name:        "write_file",
				Description: "Write content to a file in the worktree (creates directories as needed)",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path":    map[string]interface{}{"type": "string", "description": "Path to the file"},
						"content": map[string]interface{}{"type": "string", "description": "Content to write"},
					},
					"required": []string{"path", "content"},
				},
			},
		},
		{
			Type: "function",
			Function: &ToolDef{
				Name:        "execute_command",
				Description: "Execute a shell command in the worktree directory. Always set timeout: 2s for fast commands (ls, cat, grep, git status), 10s for package installs (npm install, pip install), 30s for builds and tests (npm run build, npm test, make), 60s for long-running operations (migrations, large downloads). Max 300s.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"command": map[string]interface{}{"type": "string", "description": "Shell command to execute"},
						"timeout": map[string]interface{}{"type": "integer", "description": "Timeout in seconds (max 300)"},
					},
					"required": []string{"command"},
				},
			},
		},
		{
			Type: "function",
			Function: &ToolDef{
				Name:        "task_complete",
				Description: "Signal that the task is complete with a summary of work done",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"summary": map[string]interface{}{"type": "string", "description": "Summary of work completed"},
					},
					"required": []string{"summary"},
				},
			},
		},
	}
}

// BuildSystemPrompt creates the system prompt for task execution.
func BuildSystemPrompt(worktreePath string) string {
	return buildSystemPrompt(worktreePath, false)
}

// BuildPlanModeSystemPrompt creates the system prompt for plan-mode task execution.
// In plan mode, the AI follows a structured planning workflow: analyze the project,
// ask clarifying questions, create a TODO.md plan, get user approval, then execute.
func BuildPlanModeSystemPrompt(worktreePath string) string {
	return buildSystemPrompt(worktreePath, true)
}

func buildSystemPrompt(worktreePath string, planMode bool) string {
	tools := GetToolDefinitions()
	toolInfos := make([]prompts.ToolInfo, len(tools))
	for i, t := range tools {
		toolInfos[i] = prompts.ToolInfo{
			Name:        t.Function.Name,
			Description: t.Function.Description,
		}
	}

	data := prompts.PromptData{
		WorktreePath: worktreePath,
		PlanMode:     planMode,
		Tools:        toolInfos,
	}

	result, err := prompts.RenderSystemPrompt(data)
	if err != nil {
		log.Printf("warning: rendering system prompt failed: %v", err)
		return ""
	}
	return result
}