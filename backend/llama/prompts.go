package llama

import (
	"os"
	"path/filepath"
	"time"

	"mike/backend/prompts"
	"mike/backend/types"
	"mike/pkg/logging"
)

// BuildSystemPrompt creates the system prompt for task execution.
func BuildSystemPrompt(worktreePath, cwd, summary, skillsMount string, operatingMode types.OperatingMode) string {
	var tools []types.ChatTool
	switch operatingMode {
	case types.ModePlan:
		tools = GetPlanModeToolDefinitions()
	case types.ModeExplore:
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
		PlanMode:     operatingMode == types.ModePlan,
		ExploreMode:  operatingMode == types.ModeExplore,
		Tools:        toolInfos,
		Skills:       prompts.DiscoverSkills(skillsMount),
		AgentsMd:     agentsMd,
		Summary:      summary,
	}

	result, err := prompts.RenderSystemPrompt(data)
	if err != nil {
		logging.For(logging.LLM).Warnf("warning: rendering system prompt failed: %v", err)
		return ""
	}
	return result
}

var _ = types.ChatMessage{}
