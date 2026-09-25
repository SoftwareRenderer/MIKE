package llama

// The spellings live in backend/types, where the reservation check an extension manifest is tested
// against also reads them (types.CoreToolNames). These are the same values under the short names this
// package's definitions use.

import "mike/backend/types"

const (
	ToolReadFile  = types.ToolReadFile
	ToolWriteFile = types.ToolWriteFile
	ToolEditFile  = types.ToolEditFile
	ToolBash      = types.ToolBash

	ToolAskQuestion   = types.ToolAskQuestion
	ToolRequestReview = types.ToolRequestReview
	ToolCreateTask    = types.ToolCreateTask
	ToolViewTasks     = types.ToolViewTasks
	ToolEditTask      = types.ToolEditTask

	ToolLSPReferences = types.ToolLSPReferences
	ToolLSPDefinition = types.ToolLSPDefinition
	ToolLSPHover      = types.ToolLSPHover
	ToolLSPRename     = types.ToolLSPRename
)

// AllToolNames is every tool name the server can be asked to run, in one list so a completeness test has
// something to enumerate. It is deliberately not derived from the mode-specific sets: a tool that exists in
// only one mode must still be dispatched in all of them.
func AllToolNames() []string { return types.CoreToolNames() }
