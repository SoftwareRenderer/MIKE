package llama

import "kanban/backend/types"

// toolDisplayNames maps tool names to their UI display names.
var toolDisplayNames = map[string]string{
	"read_file":        "Read",
	"write_file":       "Write",
	"edit_file":        "Edit",
	"bash":             "Execute",
	"task_complete":    "Done",
}

// GetToolDisplayName returns the display name for a tool, or the raw name if unknown.
func GetToolDisplayName(toolName string) string {
	if name, ok := toolDisplayNames[toolName]; ok {
		return name
	}
	return toolName
}

// GetToolDefinitions returns the standard tool definitions.
func GetToolDefinitions() []types.ChatTool {
	return []types.ChatTool{
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "read_file",
				DisplayName: "Read",
				Description: "Read the contents of a file from the worktree. Supports offset (1-indexed start line) and limit (max lines to return) for large files. Omit both for full file.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path":   map[string]interface{}{"type": "string", "description": "Path to the file relative to worktree root"},
						"offset": map[string]interface{}{"type": "integer", "description": "Line number to start reading from (1-indexed)"},
						"limit":  map[string]interface{}{"type": "integer", "description": "Maximum number of lines to read"},
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
			Function: &types.ToolDef{
				Name:        "edit_file",
				DisplayName: "Edit",
				Description: "Make precise text replacements in a file. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions just to connect distant changes.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{"type": "string", "description": "Path to the file to edit (relative to worktree root)"},
						"edits": map[string]interface{}{
							"type": "array",
							"items": map[string]interface{}{
								"type":       "object",
								"properties": map[string]interface{}{
									"oldText": map[string]interface{}{"type": "string", "description": "Exact text to replace. Must be unique in the original file and must not overlap with any other edit's oldText."},
									"newText": map[string]interface{}{"type": "string", "description": "Replacement text for this targeted edit."},
								},
								"required": []string{"oldText", "newText"},
							},
						},
					},
					"required": []string{"path", "edits"},
				},
			},
		},
		{
			Type: "function",
			Function: &types.ToolDef{
				Name:        "bash",
				DisplayName: "Execute",
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
			Function: &types.ToolDef{
				Name:        "task_complete",
				DisplayName: "Done",
				Description: "Signal that the task is complete",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"summary": map[string]interface{}{"type": "string", "description": "Brief summary of the changes made (files created/modified, key decisions, issues encountered)"},
					},
					"required": []string{"summary"},
				},
			},
		},
	}
}
