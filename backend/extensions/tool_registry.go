package extensions

import (
	"mike/backend/types"
	"mike/pkg/logging"
)

// ToolRegistry asks the store on every question instead of caching: enabling an extension or changing its
// address changes the answer.
//
// A tool is offered only when a call could succeed, so the model is never handed a tool that can only
// fail.
type ToolRegistry struct {
	store *ExtensionStore
}

// ContributedTool is one offered tool with the row that runs it: the egress call needs its configuration.
type ContributedTool struct {
	Spec      types.ToolSpec
	Extension *Extension
	Tool      ToolConfig
}

// ToolProblem is a tool an enabled extension does not offer, and why.
type ToolProblem struct {
	Extension string
	Tool      string
	Reason    string
}

func NewToolRegistry(store *ExtensionStore) *ToolRegistry {
	return &ToolRegistry{store: store}
}

// Contributed lists the offered tools, in store order.
func (r *ToolRegistry) Contributed() []ContributedTool {
	offered, _ := r.survey()
	return offered
}

// Lookup answers only for the tools Contributed returns, so a withheld tool cannot be run through here.
func (r *ToolRegistry) Lookup(name string) (*ContributedTool, bool) {
	offered, _ := r.survey()
	for i := range offered {
		if offered[i].Spec.Name == name {
			return &offered[i], true
		}
	}
	return nil, false
}

// Spec is the definition behind the display name on a tool card.
func (r *ToolRegistry) Spec(name string) (types.ToolSpec, bool) {
	if found, ok := r.Lookup(name); ok {
		return found.Spec, true
	}
	return types.ToolSpec{}, false
}

// NetworkedExtensions run in this process, so a project that closes its network does not close these
// calls. The Sandbox Configuration states this with a list.
func (r *ToolRegistry) NetworkedExtensions() []types.NetworkedExtension {
	offered := r.Contributed()
	if len(offered) == 0 {
		return nil
	}
	var out []types.NetworkedExtension
	at := map[string]int{}
	for _, tool := range offered {
		name := tool.Spec.Extension
		if i, seen := at[name]; seen {
			out[i].Tools = append(out[i].Tools, tool.Spec.Name)
			continue
		}
		display := name
		if shown := tool.Extension.Manifest.DisplayName; shown != "" {
			display = shown
		}
		at[name] = len(out)
		out = append(out, types.NetworkedExtension{
			Name:        name,
			DisplayName: display,
			Tools:       []string{tool.Spec.Name},
		})
	}
	return out
}

// A name already taken is skipped, not offered twice: dispatch resolves tools by name alone.
func (r *ToolRegistry) survey() ([]ContributedTool, []ToolProblem) {
	rows, err := r.toolExtensions()
	if err != nil {
		logging.For(logging.Extensions).Warnf("list tool extensions: %v", err)
		return nil, nil
	}
	var (
		offered  []ContributedTool
		problems []ToolProblem
		taken    = map[string]bool{}
	)
	report := func(ext *Extension, toolName, reason string) {
		problems = append(problems, ToolProblem{Extension: ext.Name, Tool: toolName, Reason: reason})
		logging.For(logging.Extensions).Debugf("extension=%s tool=%s not offered: %s", ext.Name, toolName, reason)
	}

	for i := range rows {
		ext := &rows[i]
		if !ext.Enabled {
			continue
		}
		usable, why := ext.UsableContributedTools()
		if why != nil {
			report(ext, "", why.Error())
		}
		for j := range usable {
			tool := &usable[j]
			switch {
			case types.IsCoreToolName(tool.Name):
				report(ext, tool.Name, "the name belongs to a core tool")
				continue
			case taken[tool.Name]:
				report(ext, tool.Name, "another enabled extension already contributes this name")
				continue
			}
			if reason := unconfiguredReason(ext, tool); reason != "" {
				report(ext, tool.Name, reason)
				continue
			}
			taken[tool.Name] = true
			offered = append(offered, ContributedTool{
				Spec:      toolSpecFor(ext, tool),
				Extension: ext,
				Tool:      *tool,
			})
		}
	}
	return offered, problems
}

func (r *ToolRegistry) toolExtensions() ([]Extension, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	return r.store.GetByType(ExtensionTypeTool)
}

// The origin is the only configuration a tool must have; every other placeholder carries a default or may
// be empty. The wording comes from the call path, so both answers tell a person the same thing.
func unconfiguredReason(ext *Extension, tool *ToolConfig) string {
	_, problem := tool.configuredOrigin(ext.Config)
	return problem
}

// The manifest's description is passed through unchanged: it is the tool's only route to the model.
func toolSpecFor(ext *Extension, tool *ToolConfig) types.ToolSpec {
	display := tool.DisplayName
	if display == "" {
		display = ext.Manifest.DisplayName
	}
	return types.ToolSpec{
		Name:        tool.Name,
		DisplayName: display,
		Description: tool.Description,
		Parameters:  tool.Parameters,
		Extension:   ext.Name,
	}
}
