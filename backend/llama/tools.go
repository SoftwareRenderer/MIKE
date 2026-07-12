package llama

import (
	"encoding/json"

	"kanban/backend/types"
)

// CreateTaskArgs holds the parsed arguments for the create_task tool.
type CreateTaskArgs struct {
	Title        string `json:"title"`
	Prompt       string `json:"prompt"`
	ParentTaskID string `json:"parent_task_id,omitempty"` // optional: for nested subtasks
}

// toolDisplayNames maps tool names to their UI display names.
var toolDisplayNames = map[string]string{
	"read_file":      "Read",
	"write_file":     "Write",
	"edit_file":      "Edit",
	"bash":           "Execute",
	"request_review": "Done",
	"create_task":    "Create Task",
}

// GetToolDisplayName returns the display name for a tool, or the raw name if unknown.
func GetToolDisplayName(toolName string) string {
	if name, ok := toolDisplayNames[toolName]; ok {
		return name
	}
	return toolName
}

// GetPlanModeToolDefinitions returns the restricted tool set for plan mode.
// In plan mode, the LLM can read files, run safe read-only commands, create tasks, and signal completion.
// Write, edit tools are removed to prevent unauthorized changes.
func GetPlanModeToolDefinitions() []types.ChatTool {
	return []types.ChatTool{
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "read_file",
				DisplayName: "Read",
				Description: "Read the contents of a file. Supports offset (1-indexed start line) and limit (max lines to return) for large files. Omit both for full file.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":   map[string]any{"type": "string", "description": "Path to the file"},
						"offset": map[string]any{"type": "integer", "description": "Line number to start reading from (1-indexed)"},
						"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "bash",
				DisplayName: "Execute",
				Description: "Execute a read-only shell command. Only safe, non-destructive commands are allowed: ls, cat, grep, find, head, tail, wc, diff, git log, git status, git diff, git show, git branch, git remote, env, echo, pwd, tree, file, stat, du, which, man, help. Do NOT use write commands (rm, mv, cp, chmod, chown, touch, install, build, test, etc.). Always set timeout: 2s for fast commands.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string", "description": "Read-only shell command to execute"},
						"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds (max 300)"},
					},
					"required": []string{"command"},
				},
			},
		},
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "create_task",
				DisplayName: "Create Task",
				Description: "Create a new task on the Kanban board. Use this to decompose work into subtasks. The new task inherits the parent's settings (model, provider, auto_commit). Returns the created task_id, which can be used as parent_task_id for nested subtasks.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":          map[string]any{"type": "string", "description": "Short, descriptive title for the task (max 80 characters)"},
						"prompt":         map[string]any{"type": "string", "description": "Detailed description of what the task should accomplish"},
						"parent_task_id": map[string]any{"type": "string", "description": "Optional: ID of a parent task to nest this task under. If omitted, creates a primary task. Use a previously created task_id to create nested subtasks."},
					},
					"required": []string{"title", "prompt"},
				},
			},
		},
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "request_review",
				DisplayName: "Done",
				Description: "If the task is complete, use this tool.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"summary": map[string]any{"type": "string", "description": "Optional: Concise summary of accomplishments, any issues encountered"},
					},
					"required": []string{"summary"},
				},
			},
		},
	}
}

// GetToolDefinitions returns the standard tool definitions.
func GetToolDefinitions() []types.ChatTool {
	return []types.ChatTool{
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "read_file",
				DisplayName: "Read",
				Description: "Read the contents of a file. Supports offset (1-indexed start line) and limit (max lines to return) for large files. Omit both for full file.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":   map[string]any{"type": "string", "description": "Path to the file"},
						"offset": map[string]any{"type": "integer", "description": "Line number to start reading from (1-indexed)"},
						"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "write_file",
				DisplayName: "Write",
				Description: "Write content to a file (creates directories as needed)",
				Parameters: map[string]any{
					"type": "object",
					// ensure that the LLM sees path before content
					"properties": json.RawMessage(`{"path":{"type":"string","description":"Relative path to the file"},"content":{"type":"string","description":"Content to write"}}`),
					"required":   []string{"path", "content"},
				},
			},
		},
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "edit_file",
				DisplayName: "Edit",
				Description: "Make precise text replacements in a file. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Each oldText must match exactly (whitespace, indentation)",
				Parameters: map[string]any{
					"type":       "object",
					"properties": json.RawMessage(`{"path":{"type":"string","description":"Relative path to the file"},"edits":{"type":"array","items":{"type":"object","properties":{"oldText":{"type":"string","description":"Exact text to replace. Must be unique in the original file and must not overlap with any other edit's oldText."},"newText":{"type":"string","description":"Replacement text for this targeted edit."}},"required":["oldText","newText"]}}}`),
					"required":   []string{"path", "edits"},
				},
			},
		},
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "bash",
				DisplayName: "Execute",
				Description: "Execute a shell command. Always set timeout: 2s for fast commands (ls, cat, grep, git status), 10s for package installs (npm install, pip install), 30s for builds and tests (npm run build, npm test, make), 60s for long-running operations (migrations, large downloads). Max 300s.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string", "description": "Shell command to execute"},
						"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds (max 300)"},
					},
					"required": []string{"command"},
				},
			},
		},
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "request_review",
				DisplayName: "Done",
				Description: "Request the user to review completed changes",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"summary": map[string]any{"type": "string", "description": "Brief summary of the changes made or issues encountered. Explain key decisions ONLY IF changes include complex logic."},
					},
					"required": []string{"summary"},
				},
			},
		},
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "create_task",
				DisplayName: "Create Task",
				Description: "This creates a new task on the Kanban board. You MUST ask the user for explicit approval before using this tool.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":          map[string]any{"type": "string", "description": "Short, descriptive title for the task (max 80 characters)"},
						"prompt":         map[string]any{"type": "string", "description": "Detailed description of what the task should accomplish"},
						"parent_task_id": map[string]any{"type": "string", "description": "Optional: ID of a parent task to nest this task under. If omitted, creates a parent task. Use a previously created task_id to create nested subtasks."},
					},
					"required": []string{"title", "prompt"},
				},
			},
		},
	}
}
