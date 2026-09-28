package types

// Tool names are the wire contract between three places that must agree: the definitions backend/llama
// publishes to the provider, the dispatch table in exec.toolRunner, and the frontend that renders a tool
// call's card. They live here because both halves of that contract read them — llama to offer a tool,
// backend/extensions to refuse an extension that takes one of these names — and `types` is the package
// underneath both. A spelling therefore has one home: a core tool cannot be declared in one list and left
// unreserved in the other.
const (
	// File and shell tools. bash has two definitions (read-write, and the read-only variant plan mode
	// offers) under one name, because the model must not see two near-identical tools.
	ToolReadFile  = "read_file"
	ToolWriteFile = "write_file"
	ToolEditFile  = "edit_file"
	ToolBash      = "bash"

	// Conversation and workflow tools: asking the user, signalling completion, and the task board the
	// agent can edit.
	ToolAskQuestion   = "ask_question"
	ToolRequestReview = "request_review"
	ToolCreateTask    = "create_task"
	ToolViewTasks     = "view_tasks"
	ToolEditTask      = "edit_task"

	// LSP tools, gated per extension by capabilities.lsp.tool_calls_enabled and only offered when an
	// LSP extension is installed at all.
	ToolLSPReferences = "lsp_references"
	ToolLSPDefinition = "lsp_definition"
	ToolLSPHover      = "lsp_hover"
	ToolLSPRename     = "lsp_rename"
)

// CoreToolNames are the tools the server implements itself, and an extension may not take one of these
// names: the model would call it and never reach the core tool. This list is also every tool the server
// can be asked to run, so a tool added here is reserved the moment it is declared.
func CoreToolNames() []string {
	return []string{
		ToolReadFile, ToolWriteFile, ToolEditFile, ToolBash,
		ToolAskQuestion, ToolRequestReview, ToolCreateTask, ToolViewTasks, ToolEditTask,
		ToolLSPReferences, ToolLSPDefinition, ToolLSPHover, ToolLSPRename,
	}
}

func IsCoreToolName(name string) bool {
	for _, n := range CoreToolNames() {
		if n == name {
			return true
		}
	}
	return false
}
