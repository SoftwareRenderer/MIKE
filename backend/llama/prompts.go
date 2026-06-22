package llama

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"kanban/backend/prompts"
	"kanban/backend/types"
)

// BuildSystemPrompt creates the system prompt for task execution.
func BuildSystemPrompt(worktreePath string) string {
	return buildSystemPrompt(worktreePath, false)
}

// BuildPlanModeSystemPrompt creates the system prompt for plan-mode task execution.
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

	// Read AGENTS.md from worktree root if it exists
	var agentsMd string
	if data, err := os.ReadFile(filepath.Join(worktreePath, "AGENTS.md")); err == nil {
		// Truncate to 8000 lines max to avoid blowing out context
		lines := strings.Split(string(data), "\n")
		if len(lines) > 8000 {
			lines = lines[:8000]
			agentsMd = strings.Join(lines, "\n") + "\n\n... (truncated)"
		} else {
			agentsMd = string(data)
		}
	}

	data := prompts.PromptData{
		WorktreePath: worktreePath,
		PlanMode:     planMode,
		Tools:        toolInfos,
		AgentsMd:     agentsMd,
	}

	result, err := prompts.RenderSystemPrompt(data)
	if err != nil {
		log.Printf("warning: rendering system prompt failed: %v", err)
		return ""
	}
	return result
}

// Ensure types.ChatMessage is usable from this package.
var _ = types.ChatMessage{}
