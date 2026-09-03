package exec

import (
	"context"
	"encoding/json"

	"kanban/backend/lsp"
)

// lspManager is the subset of *lsp.ExtensionLSPManager that TaskExecutor uses.
// The concrete *lsp.ExtensionLSPManager satisfies it; tests inject fakes.
type lspManager interface {
	GetRegistry() *lsp.ExtensionRegistry
	GetOrCreate(ctx context.Context, workspacePath, language string, cfg *lsp.ServerConfig) (*lsp.LSPProcess, error)
	Release(workspacePath, language string)
	SendToServer(ctx context.Context, proc *lsp.LSPProcess, msg json.RawMessage) error
	LSPNotify(ctx context.Context, proc *lsp.LSPProcess, msg json.RawMessage) error
	EnsureInitialized(ctx context.Context, workspacePath string, cfg *lsp.ServerConfig) error
	SyncDocument(ctx context.Context, workspacePath, filePath string, cfg *lsp.ServerConfig) (string, bool, error)
	Subscribe() chan json.RawMessage
	Unsubscribe(ch chan json.RawMessage)
	IsToolCallsEnabledForFile(filePath string) bool
	LSPRequest(ctx context.Context, workspacePath, filePath, method string, params any) (json.RawMessage, error)
	// FilesChanged ends the language-server connections of this workspace whose
	// extension declared one of the changed files as a reason to re-read the
	// project (`reload_files`). Returns true if any connection was ended.
	FilesChanged(workspacePath string, changed []string) bool
}
