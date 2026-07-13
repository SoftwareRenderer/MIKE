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
	"ask_question":   "Question",
	"request_review": "Done",
	"create_task":    "Create Task",
	"view_tasks":     "View Tasks",
	"edit_task":      "Edit Task",
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
				Description: "Create a new task on the Kanban board. Use this to decompose work into subtasks. Returns the created task_id, which can be used as parent_task_id for nested subtasks. Do not create duplicate tasks.",
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
				Name:        "ask_question",
				DisplayName: "Question",
				Description: "Ask the user a clarifying question when you need more information to proceed. Use this instead of making assumptions when requirements are unclear.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{"type": "string", "description": "The question to ask the user"},
					},
					"required": []string{"question"},
				},
			},
		},
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "view_tasks",
				DisplayName: "View Tasks",
				Description: "List all tasks with status 'backlog' in the current project. Returns an array of tasks with id, title, prompt, and parent_task_id fields.",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
					"required":   []string{},
				},
			},
		},
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "edit_task",
				DisplayName: "Edit Task",
				Description: "Edit a task's title, prompt, or parent_task_id. Only tasks with status 'backlog' in the current project can be edited. At least one field (title, prompt, parent_task_id) must be provided. If parent_task_id is set, the parent task must also have status 'backlog'.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"task_id":        map[string]any{"type": "string", "description": "ID of the task to edit"},
						"title":          map[string]any{"type": "string", "description": "New title for the task"},
						"prompt":         map[string]any{"type": "string", "description": "New prompt/description for the task"},
						"parent_task_id": map[string]any{"type": "string", "description": "New parent task ID to nest this task under. Must also be a backlog task. Omit to keep existing parent."},
					},
					"required": []string{"task_id"},
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

// GetIdeationModeToolDefinitions returns the tool set for ideation mode.
// Same as standard (act) mode plus task management tools (create_task, view_tasks, edit_task).
// This allows the LLM to brainstorm, prototype code, and organize work into tasks.
func GetIdeationModeToolDefinitions() []types.ChatTool {
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
				Name:        "ask_question",
				DisplayName: "Question",
				Description: "Ask the user a clarifying question when you need more information to proceed. Use this instead of making assumptions when requirements are unclear.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{"type": "string", "description": "The question to ask the user"},
					},
					"required": []string{"question"},
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
				Description: "Create a new task on the Kanban board. Use this to decompose work into subtasks. Returns the created task_id, which can be used as parent_task_id for nested subtasks. Do not create duplicate tasks.",
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
				Name:        "view_tasks",
				DisplayName: "View Tasks",
				Description: "List all tasks with status 'backlog' in the current project. Returns an array of tasks with id, title, prompt, and parent_task_id fields.",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
					"required":   []string{},
				},
			},
		},
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "edit_task",
				DisplayName: "Edit Task",
				Description: "Edit a task's title, prompt, or parent_task_id. Only tasks with status 'backlog' in the current project can be edited. At least one field (title, prompt, parent_task_id) must be provided. If parent_task_id is set, the parent task must also have status 'backlog'.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"task_id":        map[string]any{"type": "string", "description": "ID of the task to edit"},
						"title":          map[string]any{"type": "string", "description": "New title for the task"},
						"prompt":         map[string]any{"type": "string", "description": "New prompt/description for the task"},
						"parent_task_id": map[string]any{"type": "string", "description": "New parent task ID to nest this task under. Must also be a backlog task. Omit to keep existing parent."},
					},
					"required": []string{"task_id"},
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
				Name:        "ask_question",
				DisplayName: "Question",
				Description: "Ask the user a clarifying question when you need more information to proceed. Use this instead of making assumptions when requirements are unclear.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{"type": "string", "description": "The question to ask the user"},
					},
					"required": []string{"question"},
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
	}
}
