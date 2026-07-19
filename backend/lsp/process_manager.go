package lsp

import (
	"encoding/json"
	"sync"
	"sync/atomic"
)

// LSPProcess represents a running language server process for a workspace.
type LSPProcess struct {
	ID        string       // unique process identifier
	Workspace string       // absolute path to the workspace/project root
	Config    *ServerConfig // language server configuration
	mu        sync.Mutex
	// clients tracks the number of connected WebSocket clients
	clients int
	// initialized tracks whether the server has been initialized
	initialized bool
	// initResponse caches the initialize response for subsequent clients
	initResponse json.RawMessage
}

// ClientCount returns the current number of connected clients.
func (p *LSPProcess) ClientCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.clients
}

// AddClient increments the client count.
func (p *LSPProcess) AddClient() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clients++
}

// RemoveClient decrements the client count. Returns the new count.
func (p *LSPProcess) RemoveClient() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clients--
	return p.clients
}

// MarkInitialized marks the process as initialized and caches the response.
func (p *LSPProcess) MarkInitialized(response json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initialized = true
	p.initResponse = response
}

// Initialized returns whether the process has been initialized.
func (p *LSPProcess) Initialized() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.initialized
}

// GetInitResponse returns the cached initialize response.
func (p *LSPProcess) GetInitResponse() json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.initResponse
}

// ProcessManager manages LSPProcess instances and message forwarding.
type ProcessManager struct {
	mu         sync.Mutex
	processes  map[string]*LSPProcess // workspace path -> process
	nextID     atomic.Int64
	// messageCh is a channel where server messages arrive for routing to clients
	messageCh chan json.RawMessage
}

// NewProcessManager creates a new ProcessManager.
func NewProcessManager() *ProcessManager {
	return &ProcessManager{
		processes: make(map[string]*LSPProcess),
		messageCh: make(chan json.RawMessage, 1024),
	}
}

// GetOrCreateProcess returns an existing process for the workspace, or creates a new one.
func (pm *ProcessManager) GetOrCreateProcess(workspace string, cfg *ServerConfig) *LSPProcess {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if p, ok := pm.processes[workspace]; ok {
		return p
	}

	id := pm.nextID.Add(1)
	p := &LSPProcess{
		ID:        "lsp-proc-" + string(rune(id)),
		Workspace: workspace,
		Config:    cfg,
	}
	pm.processes[workspace] = p
	return p
}

// RemoveProcess removes a process when it has no more clients.
func (pm *ProcessManager) RemoveProcess(workspace string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.processes, workspace)
}

// SendToServer sends a JSON-RPC message to the language server.
// In a full implementation, this would write to the server's stdin.
// For the handler, this is the interface point for client→server messages.
func (pm *ProcessManager) SendToServer(process *LSPProcess, msg json.RawMessage) error {
	// In a full implementation, this writes to the language server's stdin pipe.
	// The handler uses this to forward remapped client requests.
	_ = process
	_ = msg
	return nil
}

// ServerMessageCh returns the channel for receiving server messages.
func (pm *ProcessManager) ServerMessageCh() <-chan json.RawMessage {
	return pm.messageCh
}

// DeliverServerMessage simulates a message arriving from the language server.
// Used internally and in tests to inject server responses.
func (pm *ProcessManager) DeliverServerMessage(msg json.RawMessage) {
	select {
	case pm.messageCh <- msg:
	default:
		// Drop if channel is full to avoid blocking
	}
}
