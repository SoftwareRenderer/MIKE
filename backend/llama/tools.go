package llama

import (
	"mike/backend/types"
)

var toolDisplayNames = map[string]string{
	types.ToolReadFile:      "Read",
	types.ToolWriteFile:     "Write",
	types.ToolEditFile:      "Edit",
	types.ToolBash:          "Execute",
	types.ToolAskQuestion:   "Question",
	types.ToolRequestReview: "Done",
	types.ToolCreateTask:    "Create Task",
	types.ToolViewTasks:     "View Tasks",
	types.ToolEditTask:      "Edit Task",
	types.ToolLSPReferences: "LSP References",
	types.ToolLSPDefinition: "LSP Definition",
	types.ToolLSPHover:      "LSP Hover",
	types.ToolLSPRename:     "LSP Rename",
}

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

// newTool wraps a tool's parameter struct. Struct field order determines the order the LLM sees parameters.
func newTool[P any](name, displayName, description string, props P, required []string) types.ChatTool {
	return types.ChatTool{
		Type: "function",
		Function: &types.ToolDef{
			Name: name, DisplayName: displayName, Description: description,
			Parameters: map[string]any{"type": "object", "properties": props, "required": required},
		},
	}
}

var toolReadFile = newTool(types.ToolReadFile, "Read",
	"Read the contents of a file. Supports offset (1-indexed start line) and limit (max lines to return) for large files. Omit both for full file.",
	readFileProps{
		Path:   paramSchema{Type: "string", Description: "Path to the file"},
		Offset: paramSchema{Type: "integer", Description: "Line number to start reading from (1-indexed)"},
		Limit:  paramSchema{Type: "integer", Description: "Maximum number of lines to read"},
	},
	[]string{"path"})

var toolWriteFile = newTool(types.ToolWriteFile, "Write",
	"Write content to a file (creates directories as needed)",
	writeFileProps{
		Path:    paramSchema{Type: "string", Description: "Relative path to the file"},
		Content: paramSchema{Type: "string", Description: "Content to write"},
	},
	[]string{"path", "content"})

var toolEditFile = newTool(types.ToolEditFile, "Edit",
	"Make precise text replacements in a file. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Each oldText must match exactly (whitespace, indentation)",
	editFileProps{
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
	[]string{"path", "edits"})

var toolBash = newTool(types.ToolBash, "Execute",
	"Execute a shell command. Always set timeout: 2s for fast commands (ls, cat, grep, git status), 10s for package installs (npm install, pip install), 30s for builds and tests (npm run build, npm test, make), 60s for long-running operations (migrations, large downloads). Max 300s.",
	bashProps{
		Command: paramSchema{Type: "string", Description: "Shell command to execute"},
		Timeout: paramSchema{Type: "integer", Description: "Timeout in seconds (max 300)"},
	},
	[]string{"command"})

// toolBashReadOnly is the restricted bash tool for plan mode.
var toolBashReadOnly = newTool(types.ToolBash, "Execute",
	"Execute a read-only shell command. Only safe, non-destructive commands are allowed: ls, cat, grep, find, head, tail, wc, diff, git log, git status, git diff, git show, git branch, git remote, env, echo, pwd, tree, file, stat, du, which, man, help. Do NOT use write commands (rm, mv, cp, chmod, chown, touch, install, build, test, etc.). Always set timeout: 2s for fast commands.",
	bashProps{
		Command: paramSchema{Type: "string", Description: "Read-only shell command to execute"},
		Timeout: paramSchema{Type: "integer", Description: "Timeout in seconds (max 300)"},
	},
	[]string{"command"})

var toolAskQuestion = newTool(types.ToolAskQuestion, "Question",
	"Ask the user a question. This must be used when clarification or approval is needed.",
	askQuestionProps{
		Question: paramSchema{Type: "string", Description: "The question to ask the user"},
	},
	[]string{"question"})

var toolRequestReview = newTool(types.ToolRequestReview, "Done",
	"Request review of completed work. This must be used if the work has been completed.",
	requestReviewProps{
		Summary: paramSchema{Type: "string", Description: "Brief summary of the changes made or issues encountered. Explain key decisions ONLY IF changes include complex logic."},
	},
	[]string{"summary"})

// toolRequestReviewPlan is the plan-mode variant with a shorter description.
var toolRequestReviewPlan = newTool(types.ToolRequestReview, "Done",
	"Request review of completed work. This must be used if the work has been completed.",
	requestReviewProps{
		Summary: paramSchema{Type: "string", Description: "Optional: Concise summary of accomplishments, any issues encountered"},
	},
	[]string{"summary"})

var toolCreateTask = newTool(types.ToolCreateTask, "Create Task",
	"Create a new task on the Kanban board. Returns the created task_id, which can be used as parent_task_id for nested subtasks. Tasks are executed independently and the deepest descendent will be executed first.",
	createTaskProps{
		Title:        paramSchema{Type: "string", Description: "Short, descriptive title for the task (max 80 characters)"},
		Prompt:       paramSchema{Type: "string", Description: "Detailed description of what the task should accomplish"},
		ParentTaskID: paramSchema{Type: "string", Description: "Optional: ID of a parent task to nest this task under. If omitted, creates a primary task. Use a previously created task_id to create nested subtasks."},
	},
	[]string{"title", "prompt"})

var toolViewTasks = newTool(types.ToolViewTasks, "View Tasks",
	"List all tasks with status 'backlog' in the current project. Returns an array of tasks with id, title, prompt, and parent_task_id fields.", map[string]any{},
	[]string{})

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

var toolLSPReferences = newTool(types.ToolLSPReferences, "LSP References",
	"Find all references to a symbol in a file. Returns an array of locations with file path, line, and character. Use this to find all usages of a variable, function, or type before refactoring.",
	lspSymbolProps{
		Path:   paramSchema{Type: "string", Description: "Relative path to the file containing the symbol"},
		Symbol: paramSchema{Type: "string", Description: "Name of the symbol to find references for"},
	},
	[]string{"path", "symbol"})

var toolLSPDefinition = newTool(types.ToolLSPDefinition, "LSP Definition",
	"Find the definition location of a symbol in a file. Returns the file path, line, and character where the symbol is defined.",
	lspSymbolProps{
		Path:   paramSchema{Type: "string", Description: "Relative path to the file containing the symbol"},
		Symbol: paramSchema{Type: "string", Description: "Name of the symbol to find the definition of"},
	},
	[]string{"path", "symbol"})

var toolLSPHover = newTool(types.ToolLSPHover, "LSP Hover",
	"Get type signature, documentation for a symbol in a file. Returns the symbol's type, signature, and any attached documentation. Use this as a quick reference before reading full source code. If more clarification or validation is needed, read the source file.",
	lspSymbolProps{
		Path:   paramSchema{Type: "string", Description: "Relative path to the file containing the symbol"},
		Symbol: paramSchema{Type: "string", Description: "Name of the symbol to hover over (e.g. 'fmt', 'Println', 'main')"},
	},
	[]string{"path", "symbol"})

var toolLSPRename = newTool(types.ToolLSPRename, "LSP Rename",
	"Rename a symbol across all files using the language server's rename capability. Applies the language server's multi-file edit (e.g. a Go or TypeScript function, variable, or type rename everywhere it is used). Validates renamability first; refuses edits outside the workspace. Prefer this over manual multi-file edit_file calls for symbol renames.",
	lspRenameProps{
		Path:    paramSchema{Type: "string", Description: "Relative path to a file containing the symbol"},
		Symbol:  paramSchema{Type: "string", Description: "Name of the symbol to rename"},
		NewName: paramSchema{Type: "string", Description: "New name for the symbol"},
	},
	[]string{"path", "symbol", "new_name"})

// LSP tools require an installed LSP extension.
func GetLSPToolDefinitions() []types.ChatTool {
	return []types.ChatTool{
		toolLSPReferences,
		toolLSPDefinition,
		toolLSPHover,
		toolLSPRename,
	}
}

// Parameter schemas pass through unchanged as they were validated at storage.
func ContributedToolDefinitions(specs []types.ToolSpec) []types.ChatTool {
	if len(specs) == 0 {
		return nil
	}
	defs := make([]types.ChatTool, 0, len(specs))
	for _, spec := range specs {
		params := spec.Parameters
		if len(params) == 0 {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		defs = append(defs, types.ChatTool{
			Type: "function",
			Function: &types.ToolDef{
				Name:        spec.Name,
				DisplayName: spec.DisplayName,
				Description: spec.Description,
				Parameters:  params,
			},
		})
	}
	return defs
}

var toolEditTask = newTool(types.ToolEditTask, "Edit Task",
	"Edit a task's title, prompt, or parent_task_id. At least one field (title, prompt, parent_task_id) must be provided.",
	editTaskProps{
		TaskID:       paramSchema{Type: "string", Description: "ID of the task to edit"},
		Title:        paramSchema{Type: "string", Description: "New title for the task"},
		Prompt:       paramSchema{Type: "string", Description: "New prompt/description for the task"},
		ParentTaskID: paramSchema{Type: "string", Description: "New parent task ID to nest this task under. Omit to keep existing parent."},
	},
	[]string{"task_id"})

// ─── Mode-specific tool sets ───

// Plan mode restricts tools to prevent unauthorized changes.
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

// Explore mode adds task management tools for brainstorming and prototyping.
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
