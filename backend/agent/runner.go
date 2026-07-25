package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"kanban/backend/extensions"
	sandboxmgr "kanban/backend/sandbox"
)

// AgentRunner manages an agent adapter (WASM) and its subprocess.
// The adapter owns all protocol logic; the runner is a pure I/O bridge.
//
// Data flow:
//
//	Backend ──Prompt()──► Runner ──EncodeCommand()──► Adapter ──bytes──► Agent stdin
//	Agent stdout ──bytes──► Adapter ──ParseOutput()──► Events ──► Runner ──► Backend
type AgentRunner struct {
	adapter      AgentAdapter
	stdin        io.WriteCloser
	stdout       io.Reader
	cmd          *exec.Cmd
	sandboxMgr   *sandboxmgr.SandboxManager
	taskID       string
	sandboxEnabled bool

	mu          sync.Mutex
	closed      bool
	eventChan   chan Event
	statsChan   chan SessionStatsData
	compactChan chan *CompactResult
	done        chan struct{}

	// model name discovered via get_state
	modelName string
}

// RunnerConfig holds runtime configuration passed to the adapter.
type RunnerConfig struct {
	ExtensionName string                 `json:"extension_name"`
	SessionName   string                 `json:"session_name"`
	WorktreePath  string                 `json:"worktree_path"`
	Sandbox       bool                   `json:"sandbox"`
	Config        map[string]any         `json:"config,omitempty"`
	AgentManifest *extensions.AgentConfig `json:"agent_manifest,omitempty"`
}

// NewAgentRunner creates a new agent runner.
// It loads the WASM adapter, initializes it, spawns the agent subprocess,
// and starts the stdout reader goroutine.
func NewAgentRunner(ext *extensions.Extension, sandboxMgr *sandboxmgr.SandboxManager, taskID, worktreePath, sessionName string, sandboxEnabled bool) (*AgentRunner, error) {
	cfg := RunnerConfig{
		ExtensionName: ext.Name,
		SessionName:   sessionName,
		WorktreePath:  worktreePath,
		Sandbox:       sandboxEnabled,
		Config:        ext.Config,
		AgentManifest: ext.Manifest.Agent,
	}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal runner config: %w", err)
	}

	// Load and initialize the WASM adapter
	adapter, err := loadAdapter(ext)
	if err != nil {
		return nil, fmt.Errorf("load adapter: %w", err)
	}
	if err := adapter.Init(cfgBytes); err != nil {
		adapter.Close()
		return nil, fmt.Errorf("init adapter: %w", err)
	}

	r := &AgentRunner{
		adapter:      adapter,
		sandboxMgr:   sandboxMgr,
		taskID:       taskID,
		sandboxEnabled: sandboxEnabled,
		eventChan:    make(chan Event, 64),
		statsChan:    make(chan SessionStatsData, 16),
		compactChan:  make(chan *CompactResult, 4),
		done:         make(chan struct{}),
	}

	// Get subprocess config from adapter
	spCfg := adapter.SubprocessConfig()
	if spCfg.Binary == "" {
		adapter.Close()
		return nil, fmt.Errorf("adapter returned empty binary name")
	}

	// Spawn the agent subprocess
	if sandboxEnabled {
		ph, err := sandboxMgr.StartProcess(taskID, spCfg.Binary, spCfg.Args)
		if err != nil {
			adapter.Close()
			return nil, fmt.Errorf("start agent in container: %w", err)
		}
		r.cmd = ph.Cmd
		r.stdin = ph.Stdin
		r.stdout = ph.Stdout
	} else {
		cmd := exec.Command(spCfg.Binary, spCfg.Args...)
		cmd.Dir = worktreePath
		cmd.Env = append(os.Environ(), spCfg.Env...)

		stdin, err := cmd.StdinPipe()
		if err != nil {
			adapter.Close()
			return nil, fmt.Errorf("create stdin pipe: %w", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			stdin.Close()
			adapter.Close()
			return nil, fmt.Errorf("create stdout pipe: %w", err)
		}
		cmd.Stderr = nil // stderr goes to parent

		if err := cmd.Start(); err != nil {
			stdin.Close()
			stdout.Close()
			adapter.Close()
			return nil, fmt.Errorf("start agent process: %w", err)
		}

		r.cmd = cmd
		r.stdin = stdin
		r.stdout = stdout
	}

	// Start stdout reader goroutine
	go r.readOutput()

	log.Printf("[agent] runner started (binary=%s, pid=%d, session=%s)", spCfg.Binary, r.PID(), sessionName)
	return r, nil
}

// Prompt sends a user message to the agent.
func (r *AgentRunner) Prompt(ctx context.Context, message string, behavior ...string) error {
	data := map[string]any{"message": message}
	if len(behavior) > 0 && behavior[0] != "" {
		data["behavior"] = behavior[0]
	}
	dataBytes, _ := json.Marshal(data)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("agent runner closed")
	}

	bytes := r.adapter.EncodeCommand(CommandPrompt, dataBytes)
	_, err := r.stdin.Write(bytes)
	return err
}

// EventChan returns the channel for receiving agent events.
func (r *AgentRunner) EventChan() <-chan Event {
	return r.eventChan
}

// Compact sends a compact command and waits for the result.
func (r *AgentRunner) Compact(ctx context.Context) (*CompactResult, error) {
	r.mu.Lock()
	if !r.closed {
		bytes := r.adapter.EncodeCommand(CommandCompact, nil)
		r.stdin.Write(bytes) // best-effort
	}
	r.mu.Unlock()

	select {
	case result := <-r.compactChan:
		return result, result.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(120 * time.Second):
		return nil, fmt.Errorf("compaction timed out")
	}
}

// QueryStats sends a session stats query and waits for the response.
func (r *AgentRunner) QueryStats(ctx context.Context) SessionStatsData {
	r.mu.Lock()
	if !r.closed {
		bytes := r.adapter.EncodeCommand(CommandStats, nil)
		r.stdin.Write(bytes) // best-effort
	}
	r.mu.Unlock()

	select {
	case stats := <-r.statsChan:
		return stats
	case <-ctx.Done():
		return SessionStatsData{}
	case <-time.After(3 * time.Second):
		return SessionStatsData{}
	}
}

// QueryModel sends a get_state command and waits for model info.
func (r *AgentRunner) QueryModel(ctx context.Context) string {
	r.mu.Lock()
	if !r.closed {
		bytes := r.adapter.EncodeCommand(CommandGetState, nil)
		r.stdin.Write(bytes) // best-effort
	}
	r.mu.Unlock()

	// Wait for a status event with model info in RawData
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case event := <-r.eventChan:
			if event.Type == EventTypeStatus && event.RawData != nil {
				if modelName, ok := event.RawData["model_name"].(string); ok && modelName != "" {
					r.mu.Lock()
					r.modelName = modelName
					r.mu.Unlock()
					return modelName
				}
			}
		case <-timeout.C:
			r.mu.Lock()
			name := r.modelName
			r.mu.Unlock()
			if name != "" {
				return name
			}
			return "Agent"
		}
	}
}

// ModelName returns the agent model name discovered via get_state.
func (r *AgentRunner) ModelName() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.modelName == "" {
		return "Agent"
	}
	return r.modelName
}

// PID returns the agent subprocess PID.
func (r *AgentRunner) PID() int {
	if r.cmd == nil || r.cmd.Process == nil {
		return -1
	}
	return r.cmd.Process.Pid
}

// Close gracefully shuts down the agent runner.
func (r *AgentRunner) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true

	// Send abort command (best-effort)
	if r.stdin != nil {
		bytes := r.adapter.EncodeCommand(CommandAbort, nil)
		r.stdin.Write(bytes)
		r.stdin.Close()
	}

	// Wait for process exit with timeout
	if r.cmd != nil && r.cmd.Process != nil {
		done := make(chan error, 1)
		go func() { done <- r.cmd.Wait() }()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			r.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				r.cmd.Process.Kill()
				<-done
			}
		}
	}

	r.adapter.Close()

	// Close channels (guarded — readOutput may have already closed done)
	select {
	case <-r.done:
		// Already closed by readOutput (process exited)
	default:
		close(r.eventChan)
		close(r.done)
	}
}

// readOutput reads raw bytes from agent stdout and feeds them to the adapter.
func (r *AgentRunner) readOutput() {
	// Close event channel when stdout is exhausted (process exited)
	defer func() {
		close(r.eventChan)
		// Signal done — Close() checks this to avoid double-close
		select {
		case <-r.done:
		default:
			close(r.done)
		}
	}()

	buf := make([]byte, 32*1024)
	for {
		n, err := r.stdout.Read(buf)
		if n > 0 {
			events := r.adapter.ParseOutput(buf[:n])
			for _, event := range events {
				r.dispatchEvent(event)
			}
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("[agent] stdout read error: %v", err)
			}
			break
		}
	}
}

// dispatchEvent routes events to the appropriate channel.
func (r *AgentRunner) dispatchEvent(event Event) {
	// Route stats to dedicated channel
	if event.Type == EventTypeStats {
		select {
		case r.statsChan <- event.Stats:
		default:
			log.Printf("[agent] stats channel full, dropping")
		}
		return
	}

	// Route compact responses to dedicated channel
	if event.Type == EventTypeStatus && event.RawData != nil {
		if cmd, ok := event.RawData["command"].(string); ok && cmd == "compact" {
			result := &CompactResult{}
			if event.RawData["summary"] != nil {
				result.Summary = fmt.Sprintf("%v", event.RawData["summary"])
			}
			if event.RawData["error"] != nil {
				result.Err = fmt.Errorf("%v", event.RawData["error"])
			}
			select {
			case r.compactChan <- result:
			default:
				log.Printf("[agent] compact channel full, dropping")
			}
			return
		}
	}

	// Route UI requests through adapter
	if event.Type == EventTypeStatus && event.RawData != nil {
		if method, ok := event.RawData["ui_request_method"].(string); ok && method != "" {
			if uiID, ok := event.RawData["ui_request_id"].(string); ok {
				respBytes := r.adapter.HandleUIRequest(uiID, method)
				if respBytes != nil && r.stdin != nil {
					r.mu.Lock()
					if !r.closed {
						r.stdin.Write(respBytes)
					}
					r.mu.Unlock()
				}
			}
		}
	}

	// Send to main event channel (non-blocking)
	select {
	case r.eventChan <- event:
	default:
		log.Printf("[agent] event channel full, dropping type=%s", event.Type)
	}
}
