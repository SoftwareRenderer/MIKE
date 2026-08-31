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
	SendToServer(proc *lsp.LSPProcess, msg json.RawMessage) error
	LSPNotify(proc *lsp.LSPProcess, msg json.RawMessage) error
	Subscribe() chan json.RawMessage
	Unsubscribe(ch chan json.RawMessage)
	IsToolCallsEnabledForFile(filePath string) bool
	LSPRequest(workspacePath, filePath, method string, params any) (json.RawMessage, error)
	// SessionInitialized reports whether this workspace's language-server process
	// already completed the initialize handshake. The handshake is per-process, and
	// a server asked twice answers the second request with an error.
	SessionInitialized(workspacePath, language string) bool
	// MarkSessionInitialized records a handshake a caller completed itself.
	MarkSessionInitialized(workspacePath, language string)
	// FilesChanged restarts the sessions whose extension declared one of the
	// changed files as a reason to re-read the project. Returns true if any ran.
	FilesChanged(workspacePath string, changed []string) bool
	// Environment reports whether the language server for this file can resolve
	// its workspace at all, and which diagnostic codes mean "could not load" when
	// it cannot. The codes come from the extension's manifest; nothing in core
	// knows what any of them mean.
	Environment(workspacePath, filePath string) lsp.EnvironmentReport
}
