package types

// CoreToolNames are the tools the server implements itself, and an extension may not take one of these
// names: the model would call it and never reach the core tool.
//
// backend/llama owns the spellings (AllToolNames); this is the copy a package below llama can read, and
// TestCoreToolNamesMatchAllToolNames holds the two together.
func CoreToolNames() []string {
	return []string{
		"read_file", "write_file", "edit_file", "bash",
		"ask_question", "request_review", "create_task", "view_tasks", "edit_task",
		"lsp_references", "lsp_definition", "lsp_hover", "lsp_rename",
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
