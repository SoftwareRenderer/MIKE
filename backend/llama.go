package backend

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LlamaClient handles communication with llama.cpp OpenAI-compatible API.
type LlamaClient struct {
	baseURL   string
	modelName string
	httpClient *http.Client
}

// NewLlamaClient creates a new LlamaClient.
func NewLlamaClient(baseURL, modelName string) *LlamaClient {
	return &LlamaClient{
		baseURL:   strings.TrimRight(baseURL, "/"),
		modelName: modelName,
		httpClient: &http.Client{Timeout: 0}, // No timeout for streaming
	}
}

// ChatMessage represents a message in the conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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

// ChatToolCall represents a tool call from the model.
type ChatToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	Type string `json:"type"`
}

// ChatChoiceDelta is the delta in a streaming response.
type ChatChoiceDelta struct {
	Content   *string        `json:"content"`
	ToolCalls []ChatToolCall `json:"tool_calls,omitempty"`
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
}

// ChatResponse is a full streaming response frame.
type ChatResponse struct {
	Choices []ChatChoice `json:"choices"`
	Usage   *ChatUsage   `json:"usage,omitempty"`
}

// StreamCallback is called for each streaming token/tool_call.
type StreamCallback func(choice ChatChoice)

// ChatStream sends a chat completion request with streaming.
func (c *LlamaClient) ChatStream(messages []ChatMessage, tools []ChatTool, cb StreamCallback) (*ChatUsage, error) {
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
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
		reqBody["tool_choice"] = toolChoice
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(data))
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
		}

		for _, choice := range cr.Choices {
			cb(choice)
		}
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
				Description: "Execute a shell command in the worktree directory",
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
	planInstructions := ``
	if planMode {
		planInstructions = `

## PLAN MODE — Follow this workflow strictly:

### Phase 1: Analyze the Project
Read the project structure silently. Check directory layout, config files (package.json, go.mod, etc.),
dependencies, build system, README, and any existing TODO or issue files.

### Phase 2: Ask Clarifying Questions
Identify gaps that would block correct implementation. Ask at most 5 critical questions in a single message.
Do not ask about things already answerable from the codebase.

### Phase 3: Create TODO.md
Write a TODO.md file in the project root with the following structure:

# TODO

## Goal
One sentence describing what will be built or fixed.

## Tasks

### 1. <Phase Name>
- [ ] <Concrete, measurable action>
- [ ] <Concrete, measurable action>

## Notes
Any constraints, decisions, or known risks.

Requirements:
- Tasks must be small and independently verifiable (one logical change each)
- Order tasks by dependency (prerequisites first)
- Each task must be checkable as done/not done
- No vague items like "fix things" or "improve code"

After writing TODO.md, call task_complete() with summary: "Plan created in TODO.md. Awaiting approval to proceed."

### Phase 4: Execute the Plan
Once the user approves the plan (by editing the task prompt to include "APPROVED" or similar),
work through tasks in order, one at a time. After completing each task, mark it done in TODO.md
(change - [ ] to - [x]). Do not start the next task until the current one is complete.

If you discover an unlisted task is required, add it to TODO.md and inform the user via text output.

When all tasks are marked [x], call task_complete() with a full summary of work done.`
	}

	return fmt.Sprintf(`You are an autonomous coding assistant working on a task in a git worktree.
Your working directory is: %s

You have access to the following tools via function calling:
- read_file(path): Read the contents of a file
- write_file(path, content): Write content to a file (creates directories as needed)
- execute_command(command, timeout): Execute a shell command in the worktree
- task_complete(summary): Signal that the task is complete with a summary of work done

When the task is complete, call task_complete() with a summary including:
- Files created or modified
- Commands executed
- Any issues encountered

For the execute_command tool, always set the 'timeout' parameter:
- 2s for fast commands (ls, cat, grep, git status)
- 10s for package installs (npm install, pip install)
- 30s for builds and tests (npm run build, npm test, make)
- 60s for long-running operations (migrations, large downloads)
Timeout is specified in seconds. The server will reject values above 300s.%s`, worktreePath, planInstructions)
}