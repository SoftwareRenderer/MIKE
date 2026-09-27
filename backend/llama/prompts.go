package llama

import (
	"os"
	"path/filepath"
	"time"

	"mike/backend/prompts"
	"mike/backend/types"
	"mike/pkg/logging"
)

// BuildSystemPrompt creates the system prompt for task execution. worktreePath is where AGENTS.md
// is read from and cwd the working directory; summary is injected into the prompt; skillsMount is
// the container path for skills (e.g. "/mnt/skills") in sandbox mode, empty otherwise;
// operatingMode decides which tool set the prompt describes; an OperatingMode cannot hold
// anything else, so the switch below has no default case to fall through to by accident.
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

// Ensure types.ChatMessage is usable from this package.
var _ = types.ChatMessage{}
