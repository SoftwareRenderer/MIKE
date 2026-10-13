package llama

import (
	"io"
	"path/filepath"
	"time"

	"mike/backend/fsutil"
	"mike/backend/prompts"
	"mike/backend/types"
	"mike/pkg/logging"
)

// agentsFileLimit caps the repository's AGENTS.md that goes into the system prompt: it rides every
// call, so an oversized one costs the operator instead of fitting the model.
const agentsFileLimit = 64 << 10

// readAgentsFile returns the worktree's AGENTS.md, or "" when there is nothing to read. A name
// that resolves outside the worktree reads nothing at all.
func readAgentsFile(worktreePath string) string {
	if worktreePath == "" {
		return ""
	}
	path, err := fsutil.Contained(filepath.Join(worktreePath, "AGENTS.md"), []string{worktreePath})
	if err != nil {
		return ""
	}
	f, _, err := fsutil.OpenChecked(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, agentsFileLimit+1))
	if err != nil {
		return ""
	}
	if len(data) > agentsFileLimit {
		logging.For(logging.LLM).Warnf("AGENTS.md in %s is over %d bytes; the prompt carries the first %d",
			worktreePath, agentsFileLimit, agentsFileLimit)
		data = data[:agentsFileLimit]
	}
	return string(data)
}

// BuildSystemPrompt creates the system prompt for task execution.
func BuildSystemPrompt(worktreePath, cwd, summary string, skillRoots []prompts.SkillRoot, operatingMode types.OperatingMode) string {
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

	agentsMd := readAgentsFile(worktreePath)

	data := prompts.PromptData{
		WorktreePath: cwd,
		CurrentDate:  time.Now().UTC().Format("2006-01-02"),
		PlanMode:     operatingMode == types.ModePlan,
		ExploreMode:  operatingMode == types.ModeExplore,
		Tools:        toolInfos,
		Skills:       prompts.DiscoverSkills(skillRoots...),
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
