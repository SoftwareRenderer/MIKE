package lsp

// LSPProcess represents a language server process.
// In production the process runs inside a gVisor sandbox managed by
// ExtensionLSPManager. The Workspace and Language fields are used by
// the LSPHandler for per-workspace routing and cleanup on disconnect.
type LSPProcess struct {
	Workspace string
	Language  string
}

func processKey(workspacePath, language string) string {
	return workspacePath + "\t" + language
}
