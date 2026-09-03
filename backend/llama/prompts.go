package llama

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"kanban/backend/prompts"
	"kanban/backend/types"
)

// BuildSystemPrompt creates the system prompt for task execution. worktreePath is where AGENTS.md
// is read from and cwd the working directory; summary is injected into the prompt; skillsMount is
// the container path for skills (e.g. "/mnt/skills") in sandbox mode, empty otherwise;
// operatingMode is "act" (default), "plan", or "explore".
func BuildSystemPrompt(worktreePath, cwd, summary, skillsMount, operatingMode string) string {
	var tools []types.ChatTool
	switch operatingMode {
	case "plan":
		tools = GetPlanModeToolDefinitions()
	case "explore":
		tools = GetExploreModeToolDefinitions()
	default:
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
		WorktreePath: cwd,
		CurrentDate:  time.Now().UTC().Format("2006-01-02"),
		PlanMode:     operatingMode == "plan",
		Tools:        toolInfos,
		Skills:       prompts.DiscoverSkills(skillsMount),
		AgentsMd:     agentsMd,
		Summary:      summary,
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
