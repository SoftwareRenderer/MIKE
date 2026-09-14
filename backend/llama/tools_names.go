package llama

// Tool names are the wire contract between three places that must agree: the definitions this
// package publishes to the provider, the dispatch table in exec.toolRunner, and the frontend that
// renders a tool call's card. They were spelled as string literals at all three sites (~90 of them).
// A typo in one is not a compile error - it is a tool the model can call that nothing answers, which
// renders as a call stuck on "Running" forever.
//
// Every name here must match a case in the dispatch table AND appear in a tool set below; both
// directions are tested.
const (
	// File and shell tools. bash has two definitions (read-write, and the read-only variant
	// plan mode offers) under one name, because the model must not see two near-identical tools.
	ToolReadFile  = "read_file"
	ToolWriteFile = "write_file"
	ToolEditFile  = "edit_file"
	ToolBash      = "bash"

	// Conversation and workflow tools: asking the user, signalling completion, and the task
	// board the agent can edit.
	ToolAskQuestion   = "ask_question"
	ToolRequestReview = "request_review"
	ToolCreateTask    = "create_task"
	ToolViewTasks     = "view_tasks"
	ToolEditTask      = "edit_task"

	// LSP tools, gated per extension by capabilities.lsp.tool_calls_enabled and only offered
	// when an LSP extension is installed at all.
	ToolLSPReferences = "lsp_references"
	ToolLSPDefinition = "lsp_definition"
	ToolLSPHover      = "lsp_hover"
	ToolLSPRename     = "lsp_rename"
)

// AllToolNames is every tool name the server can be asked to run, in one list so a completeness
// test has something to enumerate. It is deliberately not derived from the mode-specific sets:
// a tool that exists in only one mode must still be dispatched in all of them.
func AllToolNames() []string {
	return []string{
		ToolReadFile, ToolWriteFile, ToolEditFile, ToolBash,
		ToolAskQuestion, ToolRequestReview, ToolCreateTask, ToolViewTasks, ToolEditTask,
		ToolLSPReferences, ToolLSPDefinition, ToolLSPHover, ToolLSPRename,
	}
}
