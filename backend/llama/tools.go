package llama

import "kanban/backend/types"

// GetToolDefinitions returns the standard tool definitions.
func GetToolDefinitions() []types.ChatTool {
	return []types.ChatTool{
		{
			Type: "function",
			Function: &types.ToolDef{
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
			Function: &types.ToolDef{
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
			Function: &types.ToolDef{
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
			Function: &types.ToolDef{
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
