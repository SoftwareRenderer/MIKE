package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// ServerConfig describes how to launch a language server process.
type ServerConfig struct {
	Command string   // executable path
	Args    []string // command-line arguments ({workspace} is replaced)
}

// LSPProcess wraps a single language server subprocess with ref-counted
// sharing across multiple WebSocket clients.
type LSPProcess struct {
	Cmd         *exec.Cmd
	Stdin       io.WriteCloser
	Stdout      io.ReadCloser
	Stderr      io.ReadCloser
	Workspace   string
	Language    string
	RefCount    int32 // atomic
	mu          sync.Mutex
	subscribers map[uint64]chan []byte // clientID → notification channel
	quit        chan struct{}
	writeCh     chan string // messages to send to the server
	wg          sync.WaitGroup
}

// ProcessManager tracks and shares LSPProcess instances by
// (workspace path, language) pairs.
type ProcessManager struct {
	mu        sync.Mutex
	processes map[string]*LSPProcess // key: "workspacePath\tlanguage"
}

// NewProcessManager creates a ready-to-use ProcessManager.
func NewProcessManager() *ProcessManager {
	return &ProcessManager{
		processes: make(map[string]*LSPProcess),
	}
}

func processKey(workspacePath, language string) string {
	return workspacePath + "\t" + language
}

// GetOrCreate returns an existing LSPProcess for the given workspace+language
// pair, or spawns a new one. The returned process has its RefCount already
// incremented.
func (pm *ProcessManager) GetOrCreate(workspacePath, language string, cfg *ServerConfig) (*LSPProcess, error) {
	key := processKey(workspacePath, language)

	pm.mu.Lock()
	if proc, ok := pm.processes[key]; ok {
		atomic.AddInt32(&proc.RefCount, 1)
		pm.mu.Unlock()
		return proc, nil
	}

	proc, err := spawnProcess(workspacePath, language, cfg)
	if err != nil {
		pm.mu.Unlock()
		return nil, err
	}

	pm.processes[key] = proc
	pm.mu.Unlock()
	return proc, nil
}

// Release decrements the reference count for the given workspace+language pair.
// When the count reaches zero the process is fully torn down and removed from
// the manager.
func (pm *ProcessManager) Release(workspacePath, language string) {
	key := processKey(workspacePath, language)

	pm.mu.Lock()
	proc, ok := pm.processes[key]
	if !ok {
		pm.mu.Unlock()
		return
	}

	n := atomic.AddInt32(&proc.RefCount, -1)
	if n > 0 {
		pm.mu.Unlock()
		return
	}

	// Ref count hit 0 — full teardown.
	delete(pm.processes, key)
	pm.mu.Unlock()

	proc.teardown()
}

// SendToServer writes a JSON-RPC message to the language server's stdin.
func (pm *ProcessManager) SendToServer(process *LSPProcess, msg string) error {
	// Non-blocking check first so we prefer the shutdown path when both
	// channels are ready (quit closed + writeCh has buffer space).
	select {
	case <-process.quit:
		return fmt.Errorf("lsp process %s/%s is shutting down", process.Workspace, process.Language)
	default:
	}
	select {
	case process.writeCh <- msg:
		return nil
	case <-process.quit:
		return fmt.Errorf("lsp process %s/%s is shutting down", process.Workspace, process.Language)
	}
}

// Subscribe registers a client to receive notification messages from the
// language server. Returns a channel that yields raw JSON bytes for each
// notification.
func (pm *ProcessManager) Subscribe(process *LSPProcess, clientID uint64) chan []byte {
	process.mu.Lock()
	defer process.mu.Unlock()
	ch := make(chan []byte, 64)
	process.subscribers[clientID] = ch
	return ch
}

// Unsubscribe removes a client's notification channel.
func (pm *ProcessManager) Unsubscribe(process *LSPProcess, clientID uint64) {
	process.mu.Lock()
	defer process.mu.Unlock()
	if ch, ok := process.subscribers[clientID]; ok {
		close(ch)
		delete(process.subscribers, clientID)
	}
}

// --- internal helpers ---

func spawnProcess(workspacePath, language string, cfg *ServerConfig) (*LSPProcess, error) {
	args := make([]string, 0, len(cfg.Args))
	for _, a := range cfg.Args {
		args = append(args, strings.ReplaceAll(a, "{workspace}", workspacePath))
	}

	cmd := exec.Command(cfg.Command, args...)
	cmd.Dir = workspacePath

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	proc := &LSPProcess{
		Cmd:         cmd,
		Stdin:       stdin,
		Stdout:      stdout,
		Stderr:      stderr,
		Workspace:   workspacePath,
		Language:    language,
		RefCount:    1,
		subscribers: make(map[uint64]chan []byte),
		quit:        make(chan struct{}),
		writeCh:     make(chan string, 128),
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}

	proc.wg.Add(3)
	go proc.readLoop()
	go proc.writeLoop()
	go proc.stderrLoop()

	return proc, nil
}

func (p *LSPProcess) teardown() {
	close(p.quit)

	// Kill the subprocess so pipes close and goroutines unblock.
	_ = p.Cmd.Process.Kill()
	_ = p.Cmd.Wait()

	// Close stdin to help unblock the write goroutine if it was mid-write.
	_ = p.Stdin.Close()

	// Wait for all goroutines to finish.
	p.wg.Wait()

	// Close any remaining subscriber channels.
	p.mu.Lock()
	for _, ch := range p.subscribers {
		close(ch)
	}
	p.subscribers = nil
	p.mu.Unlock()
}

// readLoop decodes JSON-RPC messages from stdout and dispatches them.
func (p *LSPProcess) readLoop() {
	defer p.wg.Done()
	decoder := json.NewDecoder(p.Stdout)
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return // EOF or error (process killed / pipe closed)
		}
		p.dispatchMessage(raw)
	}
}

// writeLoop reads messages from writeCh and forwards them to the server's stdin.
func (p *LSPProcess) writeLoop() {
	defer p.wg.Done()
	for {
		select {
		case <-p.quit:
			return
		case msg, ok := <-p.writeCh:
			if !ok {
				return
			}
			_, _ = p.Stdin.Write([]byte(msg + "\n"))
		}
	}
}

// stderrLoop logs all stderr output from the language server.
func (p *LSPProcess) stderrLoop() {
	defer p.wg.Done()
	scanner := bufio.NewScanner(p.Stderr)
	for scanner.Scan() {
		log.Printf("[lsp][%s] %s", p.Language, scanner.Text())
	}
}

// dispatchMessage routes a decoded JSON-RPC message to the appropriate handler.
func (p *LSPProcess) dispatchMessage(raw json.RawMessage) {
	// Unmarshal just enough to determine the message type.
	var envelope struct {
		ID     *json.RawMessage `json:"id"`
		Method *string          `json:"method"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return // malformed message, skip
	}

	// Notification: no id field
	if envelope.ID == nil {
		p.broadcastNotification(raw)
		return
	}

	// Server request (rare, e.g. window/showMessage): id + method present.
	// Responses (id present, no method) are routed by the handler layer.
	// We ignore server requests here; the handler can handle them if needed.
}

// broadcastNotification sends a raw notification to all subscriber channels.
func (p *LSPProcess) broadcastNotification(raw json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	msg := []byte(raw)
	for _, ch := range p.subscribers {
		// Non-blocking send — drop if subscriber is slow.
		select {
		case ch <- msg:
		default:
		}
	}
}
