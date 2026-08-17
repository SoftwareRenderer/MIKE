package exec

import (
	"encoding/json"

	"kanban/backend/lsp"
)

// lspManager is the subset of *lsp.ExtensionLSPManager that TaskExecutor uses.
// The concrete *lsp.ExtensionLSPManager satisfies it; tests inject fakes.
type lspManager interface {
	GetRegistry() *lsp.ExtensionRegistry
	GetOrCreate(workspacePath, language string, cfg *lsp.ServerConfig) (*lsp.LSPProcess, error)
	Release(workspacePath, language string)
	SendToServer(proc *lsp.LSPProcess, msg json.RawMessage) error
	LSPNotify(proc *lsp.LSPProcess, msg json.RawMessage) error
	Subscribe() chan json.RawMessage
	Unsubscribe(ch chan json.RawMessage)
	IsToolCallsEnabledForFile(filePath string) bool
	LSPRequest(workspacePath, filePath, method string, params any) (json.RawMessage, error)
}
