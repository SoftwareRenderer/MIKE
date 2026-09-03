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
	// DependencyCaches reports the host directories that the enabled LSP extensions have been
	// granted for their resolved dependencies, with the container path and the variable each one is
	// read from. The task sandbox mounts them read-write so a turn that downloads dependencies
	// warms the cache the language server reads read-only; nothing in exec decides what any of it
	// means beyond "host directory, container path, variable".
	DependencyCaches() []lsp.DependencyCache
	// Environment reports whether the language server for this file can resolve
	// its workspace at all, and which diagnostic codes mean "could not load" when
	// it cannot. The codes come from the extension's manifest; nothing in core
	// knows what any of them mean.
	Environment(workspacePath, filePath string) lsp.EnvironmentReport
}
