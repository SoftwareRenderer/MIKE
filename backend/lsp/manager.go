package lsp

// LSPProcess is a language server process — in production a gVisor sandbox run by ExtensionLSPManager.
// Workspace and Language drive the LSPHandler's per-workspace routing and disconnect cleanup.
type LSPProcess struct {
	Workspace string
	Language  string
}

func processKey(workspacePath, language string) string {
	return workspacePath + "\t" + language
}
