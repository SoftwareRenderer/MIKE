package llama

import (
	"kanban/backend/types"
)

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
	"lsp_references": "LSP References",
	"lsp_definition": "LSP Definition",
	"lsp_hover":      "LSP Hover",
	"lsp_rename":     "LSP Rename",
}

// GetToolDisplayName returns the display name for a tool, or the raw name if unknown.
func GetToolDisplayName(toolName string) string {
	if name, ok := toolDisplayNames[toolName]; ok {
		return name
	}
	return toolName
}

// ─── JSON Schema structs (fields serialized in declaration order) ───

type paramSchema struct {
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Items       *editItemSchema `json:"items,omitempty"`
}

type editItemSchema struct {
	Type       string             `json:"type"`
	Properties editPropertyParams `json:"properties"`
	Required   []string           `json:"required"`
}

type editPropertyParams struct {
	OldText paramSchema `json:"oldText"`
	NewText paramSchema `json:"newText"`
}

// Per-tool property structs — field order is the order the LLM sees them.

type readFileProps struct {
	Path   paramSchema `json:"path"`
	Offset paramSchema `json:"offset"`
	Limit  paramSchema `json:"limit"`
}

type writeFileProps struct {
	Path    paramSchema `json:"path"`
	Content paramSchema `json:"content"`
}

type editFileProps struct {
	Path  paramSchema `json:"path"`
	Edits paramSchema `json:"edits"`
}

type bashProps struct {
	Command paramSchema `json:"command"`
	Timeout paramSchema `json:"timeout"`
}

type askQuestionProps struct {
	Question paramSchema `json:"question"`
}

type requestReviewProps struct {
	Summary paramSchema `json:"summary"`
}

type createTaskProps struct {
	Title        paramSchema `json:"title"`
	Prompt       paramSchema `json:"prompt"`
	ParentTaskID paramSchema `json:"parent_task_id"`
}

type editTaskProps struct {
	TaskID       paramSchema `json:"task_id"`
	Title        paramSchema `json:"title"`
	Prompt       paramSchema `json:"prompt"`
	ParentTaskID paramSchema `json:"parent_task_id"`
}

// ─── Shared tool definitions (single source of truth) ───

var toolReadFile = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolReadFile,
		DisplayName: "Read",
		Description: "Read the contents of a file. Supports offset (1-indexed start line) and limit (max lines to return) for large files. Omit both for full file.",
		Parameters: map[string]any{
			"type": "object",
			"properties": readFileProps{
				Path:   paramSchema{Type: "string", Description: "Path to the file"},
				Offset: paramSchema{Type: "integer", Description: "Line number to start reading from (1-indexed)"},
				Limit:  paramSchema{Type: "integer", Description: "Maximum number of lines to read"},
			},
			"required": []string{"path"},
		},
	},
}

var toolWriteFile = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolWriteFile,
		DisplayName: "Write",
		Description: "Write content to a file (creates directories as needed)",
		Parameters: map[string]any{
			"type": "object",
			"properties": writeFileProps{
				Path:    paramSchema{Type: "string", Description: "Relative path to the file"},
				Content: paramSchema{Type: "string", Description: "Content to write"},
			},
			"required": []string{"path", "content"},
		},
	},
}

var toolEditFile = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolEditFile,
		DisplayName: "Edit",
		Description: "Make precise text replacements in a file. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Each oldText must match exactly (whitespace, indentation)",
		Parameters: map[string]any{
			"type": "object",
			"properties": editFileProps{
				Path: paramSchema{Type: "string", Description: "Relative path to the file"},
				Edits: paramSchema{
					Type: "array", Description: "Array of edits to apply",
					Items: &editItemSchema{
						Type: "object",
						Properties: editPropertyParams{
							OldText: paramSchema{Type: "string", Description: "Exact text to replace. Must be unique in the original file and must not overlap with any other edit's oldText."},
							NewText: paramSchema{Type: "string", Description: "Replacement text for this targeted edit."},
						},
						Required: []string{"oldText", "newText"},
					},
				},
			},
			"required": []string{"path", "edits"},
		},
	},
}

var toolBash = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolBash,
		DisplayName: "Execute",
		Description: "Execute a shell command. Always set timeout: 2s for fast commands (ls, cat, grep, git status), 10s for package installs (npm install, pip install), 30s for builds and tests (npm run build, npm test, make), 60s for long-running operations (migrations, large downloads). Max 300s.",
		Parameters: map[string]any{
			"type": "object",
			"properties": bashProps{
				Command: paramSchema{Type: "string", Description: "Shell command to execute"},
				Timeout: paramSchema{Type: "integer", Description: "Timeout in seconds (max 300)"},
			},
			"required": []string{"command"},
		},
	},
}

// toolBashReadOnly is the restricted bash tool for plan mode.
var toolBashReadOnly = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolBash,
		DisplayName: "Execute",
		Description: "Execute a read-only shell command. Only safe, non-destructive commands are allowed: ls, cat, grep, find, head, tail, wc, diff, git log, git status, git diff, git show, git branch, git remote, env, echo, pwd, tree, file, stat, du, which, man, help. Do NOT use write commands (rm, mv, cp, chmod, chown, touch, install, build, test, etc.). Always set timeout: 2s for fast commands.",
		Parameters: map[string]any{
			"type": "object",
			"properties": bashProps{
				Command: paramSchema{Type: "string", Description: "Read-only shell command to execute"},
				Timeout: paramSchema{Type: "integer", Description: "Timeout in seconds (max 300)"},
			},
			"required": []string{"command"},
		},
	},
}

var toolAskQuestion = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolAskQuestion,
		DisplayName: "Question",
		Description: "Ask the user a question. This must be used when clarification or approval is needed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": askQuestionProps{
				Question: paramSchema{Type: "string", Description: "The question to ask the user"},
			},
			"required": []string{"question"},
		},
	},
}

var toolRequestReview = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolRequestReview,
		DisplayName: "Done",
		Description: "Request review of completed work. This must be used if the work has been completed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": requestReviewProps{
				Summary: paramSchema{Type: "string", Description: "Brief summary of the changes made or issues encountered. Explain key decisions ONLY IF changes include complex logic."},
			},
			"required": []string{"summary"},
		},
	},
}

// toolRequestReviewPlan is the plan-mode variant with a shorter description.
var toolRequestReviewPlan = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolRequestReview,
		DisplayName: "Done",
		Description: "Request review of completed work. This must be used if the work has been completed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": requestReviewProps{
				Summary: paramSchema{Type: "string", Description: "Optional: Concise summary of accomplishments, any issues encountered"},
			},
			"required": []string{"summary"},
		},
	},
}

var toolCreateTask = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolCreateTask,
		DisplayName: "Create Task",
		Description: "Create a new task on the Kanban board. Use this to decompose work into subtasks. Returns the created task_id, which can be used as parent_task_id for nested subtasks. Do not create duplicate tasks.",
		Parameters: map[string]any{
			"type": "object",
			"properties": createTaskProps{
				Title:        paramSchema{Type: "string", Description: "Short, descriptive title for the task (max 80 characters)"},
				Prompt:       paramSchema{Type: "string", Description: "Detailed description of what the task should accomplish"},
				ParentTaskID: paramSchema{Type: "string", Description: "Optional: ID of a parent task to nest this task under. If omitted, creates a primary task. Use a previously created task_id to create nested subtasks."},
			},
			"required": []string{"title", "prompt"},
		},
	},
}

var toolViewTasks = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolViewTasks,
		DisplayName: "View Tasks",
		Description: "List all tasks with status 'backlog' in the current project. Returns an array of tasks with id, title, prompt, and parent_task_id fields.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		},
	},
}

// ─── LSP tool definitions ───

type lspSymbolProps struct {
	Path   paramSchema `json:"path"`
	Symbol paramSchema `json:"symbol"`
}

type lspRenameProps struct {
	Path    paramSchema `json:"path"`
	Symbol  paramSchema `json:"symbol"`
	NewName paramSchema `json:"new_name"`
}

var toolLSPReferences = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolLSPReferences,
		DisplayName: "LSP References",
		Description: "Find all references to a symbol in a file. Returns an array of locations with file path, line, and character. Use this to find all usages of a variable, function, or type before refactoring.",
		Parameters: map[string]any{
			"type": "object",
			"properties": lspSymbolProps{
				Path:   paramSchema{Type: "string", Description: "Relative path to the file containing the symbol"},
				Symbol: paramSchema{Type: "string", Description: "Name of the symbol to find references for"},
			},
			"required": []string{"path", "symbol"},
		},
	},
}

var toolLSPDefinition = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolLSPDefinition,
		DisplayName: "LSP Definition",
		Description: "Find the definition location of a symbol in a file. Returns the file path, line, and character where the symbol is defined.",
		Parameters: map[string]any{
			"type": "object",
			"properties": lspSymbolProps{
				Path:   paramSchema{Type: "string", Description: "Relative path to the file containing the symbol"},
				Symbol: paramSchema{Type: "string", Description: "Name of the symbol to find the definition of"},
			},
			"required": []string{"path", "symbol"},
		},
	},
}

var toolLSPHover = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolLSPHover,
		DisplayName: "LSP Hover",
		Description: "Get type signature, documentation for a symbol in a file. Returns the symbol's type, signature, and any attached documentation. Use this as a quick reference before reading full source code. If more clarification or validation is needed, read the source file.",
		Parameters: map[string]any{
			"type": "object",
			"properties": lspSymbolProps{
				Path:   paramSchema{Type: "string", Description: "Relative path to the file containing the symbol"},
				Symbol: paramSchema{Type: "string", Description: "Name of the symbol to hover over (e.g. 'fmt', 'Println', 'main')"},
			},
			"required": []string{"path", "symbol"},
		},
	},
}

var toolLSPRename = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolLSPRename,
		DisplayName: "LSP Rename",
		Description: "Rename a symbol across all files using the language server's rename capability. Applies the language server's multi-file edit (e.g. a Go or TypeScript function, variable, or type rename everywhere it is used). Validates renamability first; refuses edits outside the workspace. Prefer this over manual multi-file edit_file calls for symbol renames.",
		Parameters: map[string]any{
			"type": "object",
			"properties": lspRenameProps{
				Path:    paramSchema{Type: "string", Description: "Relative path to a file containing the symbol"},
				Symbol:  paramSchema{Type: "string", Description: "Name of the symbol to rename"},
				NewName: paramSchema{Type: "string", Description: "New name for the symbol"},
			},
			"required": []string{"path", "symbol", "new_name"},
		},
	},
}

// GetLSPToolDefinitions returns the LSP-aware tool definitions.
// These tools are only available when an LSP extension is installed.
func GetLSPToolDefinitions() []types.ChatTool {
	return []types.ChatTool{
		toolLSPReferences,
		toolLSPDefinition,
		toolLSPHover,
		toolLSPRename,
	}
}

var toolEditTask = types.ChatTool{
	Type: "function",
	Function: &types.ToolDef{
		Name:        ToolEditTask,
		DisplayName: "Edit Task",
		Description: "Edit a task's title, prompt, or parent_task_id. At least one field (title, prompt, parent_task_id) must be provided.",
		Parameters: map[string]any{
			"type": "object",
			"properties": editTaskProps{
				TaskID:       paramSchema{Type: "string", Description: "ID of the task to edit"},
				Title:        paramSchema{Type: "string", Description: "New title for the task"},
				Prompt:       paramSchema{Type: "string", Description: "New prompt/description for the task"},
				ParentTaskID: paramSchema{Type: "string", Description: "New parent task ID to nest this task under. Omit to keep existing parent."},
			},
			"required": []string{"task_id"},
		},
	},
}

// ─── Mode-specific tool sets ───

// GetPlanModeToolDefinitions returns the restricted tool set for plan mode.
// In plan mode, the LLM can read files, run safe read-only commands, create tasks, and signal completion.
// Write, edit tools are removed to prevent unauthorized changes.
func GetPlanModeToolDefinitions() []types.ChatTool {
	return []types.ChatTool{
		toolReadFile,
		toolBashReadOnly,
		toolCreateTask,
		toolAskQuestion,
		toolViewTasks,
		toolEditTask,
		toolRequestReviewPlan,
	}
}

// GetExploreModeToolDefinitions returns the tool set for exploration mode.
// Same as standard (act) mode plus task management tools (create_task, view_tasks, edit_task).
// This allows the LLM to brainstorm, prototype code, and organize work into tasks.
func GetExploreModeToolDefinitions() []types.ChatTool {
	return []types.ChatTool{
		toolReadFile,
		toolWriteFile,
		toolEditFile,
		toolBash,
		toolAskQuestion,
		toolRequestReview,
		toolCreateTask,
		toolViewTasks,
		toolEditTask,
	}
}

// GetToolDefinitions returns the standard tool definitions.
func GetToolDefinitions() []types.ChatTool {
	return []types.ChatTool{
		toolReadFile,
		toolWriteFile,
		toolEditFile,
		toolBash,
		toolAskQuestion,
		toolRequestReview,
	}
}
