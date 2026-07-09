package llama

import (
	"log"
	"os"
	"path/filepath"
	"time"

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
	var tools []types.ChatTool
	if planMode {
		tools = GetPlanModeToolDefinitions()
	} else {
		tools = GetToolDefinitions()
	}
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
		agentsMd = string(data)
	}

	data := prompts.PromptData{
		WorktreePath: worktreePath,
		CurrentDate:  time.Now().UTC().Format("2006-01-02"),
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
