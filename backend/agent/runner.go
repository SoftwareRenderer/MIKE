// Package agent runs agent-type extensions (Pi and the like) as subprocesses.
// A WASM adapter loaded through wazero translates the agent's protocol; this package owns
// the subprocess, its stdin/stdout bridge and the model listing. Subprocesses get
// SafeAgentEnv, never os.Environ(), so an extension binary cannot read the server's secrets.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"mike/backend/extensions"
	"mike/backend/sandbox"
	pkgagent "mike/pkg/agent"
	"mike/pkg/logging"
)

// AgentRunner manages an agent adapter (WASM) and its subprocess.
// The adapter owns all protocol logic; the runner is a pure I/O bridge.
//
// Data flow:
//
//	Backend ──Prompt()──► Runner ──EncodeCommand()──► Adapter ──bytes──► Agent stdin
//	Agent stdout ──bytes──► Adapter ──ParseOutput()──► pkgagent.Events ──► Runner ──► Backend
type AgentRunner struct {
	adapter pkgagent.AgentAdapter
	stdin   io.WriteCloser
	stdout  io.Reader
	cmd     *exec.Cmd
	mu      sync.Mutex
	closed  bool
	// sendMu guards eventChan against close-while-sending: Close() may tear the
	// channels down while the reader goroutine is mid-dispatch. channelsClosed is set
	// under the write lock, so a send that observed false is still in flight under the
	// read lock and cannot race the close.
	sendMu         sync.RWMutex
	channelsClosed bool
	// closeOnce makes channel teardown idempotent — see closeChannels.
	closeOnce   sync.Once
	eventChan   chan pkgagent.Event
	statsChan   chan pkgagent.SessionStatsData
	compactChan chan *pkgagent.CompactResult
	// modelUpdated is closed (not sent to) so multiple waiters can observe it.
	modelUpdated chan struct{}
	done         chan struct{}

	// model name discovered via get_state
	modelName string
	// provider name discovered via persist entries; used for stats accumulation
	providerName string
	// last known session stats from get_session_stats; used for persistence after agent exits
	lastStats pkgagent.SessionStatsData
	// accumulated cost from persist entries; fallback when get_session_stats doesn't report cost
	sessionCost float64
	// session file path discovered via get_state; used for switch_session on restart
	sessionFile string
}

// RunnerConfig holds runtime configuration passed to the adapter.
type RunnerConfig struct {
	ExtensionName string                  `json:"extension_name"`
	SessionName   string                  `json:"session_name"`
	WorktreePath  string                  `json:"worktree_path"`
	Sandbox       bool                    `json:"sandbox"`
	Config        map[string]any          `json:"config,omitempty"`
	AgentManifest *extensions.AgentConfig `json:"agent_manifest,omitempty"`
}

// RunnerDeps is one agent run. Its three string fields are interchangeable to the compiler, so a call
// site names each one.
type RunnerDeps struct {
	Ext            *extensions.Extension
	SandboxMgr     *sandbox.SandboxManager
	TaskID         string
	WorktreePath   string
	SessionName    string
	SandboxEnabled bool
}

// NewAgentRunner prepares the adapter, then starts its process. A caller that must know the
// binary before anything is spawned takes PrepareAgent and Start instead: the name lives only in
// the adapter.
func NewAgentRunner(ctx context.Context, deps RunnerDeps) (*AgentRunner, error) {
	prepared, err := PrepareAgent(ctx, deps)
	if err != nil {
		return nil, err
	}
	return prepared.Start(deps.SandboxMgr, deps.TaskID)
}

// Prepared is a loaded, initialized adapter that has said which process it runs, with that
// process not started. Splitting construction here is what lets a sandboxed task's pre-flight ask
// the container about the binary the adapter named — the backend knows nothing about agent
// protocols and must not guess a name.
type Prepared struct {
	adapter      pkgagent.AgentAdapter
	spCfg        pkgagent.SubprocessConfig
	worktreePath string
	sandbox      bool
}

// PrepareAgent loads, initializes and resolves the adapter's subprocess config. An adapter that
// cannot name a binary is refused here, before any process exists.
func PrepareAgent(ctx context.Context, deps RunnerDeps) (*Prepared, error) {
	ext, worktreePath, sessionName, sandboxEnabled := deps.Ext, deps.WorktreePath, deps.SessionName, deps.SandboxEnabled
	cfg := RunnerConfig{
		ExtensionName: ext.Name,
		SessionName:   sessionName,
		WorktreePath:  worktreePath,
		Sandbox:       sandboxEnabled,
		Config:        ext.Config,
		AgentManifest: ext.Manifest.Capabilities.GetAgent(),
	}
	// {{property}} references become config_schema defaults, so the adapter gets values and not
	// template strings.
	if cfg.AgentManifest != nil {
		resolved := *cfg.AgentManifest
		resolved.Sandbox = cfg.AgentManifest.ResolveSandboxTemplates(ext.Manifest.ConfigSchema)
		cfg.AgentManifest = &resolved
	}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal runner config: %w", err)
	}

	adapter, err := LoadAdapter(ext)
	if err != nil {
		return nil, fmt.Errorf("load adapter: %w", err)
	}
	if err := adapter.Init(ctx, cfgBytes); err != nil {
		adapter.Close()
		return nil, fmt.Errorf("init adapter: %w", err)
	}

	spCfg := adapter.SubprocessConfig(ctx)
	if spCfg.Binary == "" {
		adapter.Close()
		return nil, fmt.Errorf("adapter returned empty binary name")
	}

	return &Prepared{
		adapter:      adapter,
		spCfg:        spCfg,
		worktreePath: worktreePath,
		sandbox:      sandboxEnabled,
	}, nil
}

// Binary is the executable the adapter named, verbatim.
func (p *Prepared) Binary() string { return p.spCfg.Binary }

// Close releases the adapter of a preparation whose process was never started.
func (p *Prepared) Close() { p.adapter.Close() }

// Start spawns the agent process the adapter described and begins streaming its events. The
// adapter is closed on failure and owned by the runner otherwise.
func (p *Prepared) Start(sandboxMgr *sandbox.SandboxManager, taskID string) (*AgentRunner, error) {
	if p.sandbox && sandboxMgr == nil {
		return nil, fmt.Errorf("agent %q is configured to run in a sandbox container, which needs a sandbox manager", p.spCfg.Binary)
	}

	r := &AgentRunner{
		adapter:      p.adapter,
		eventChan:    make(chan pkgagent.Event, 64),
		statsChan:    make(chan pkgagent.SessionStatsData, 16),
		compactChan:  make(chan *pkgagent.CompactResult, 4),
		modelUpdated: make(chan struct{}, 1),
		done:         make(chan struct{}),
	}

	// Spawn the agent subprocess
	if p.sandbox {
		ph, err := sandboxMgr.StartProcess(taskID, p.spCfg.Binary, p.spCfg.Args)
		if err != nil {
			p.adapter.Close()
			return nil, fmt.Errorf("start agent in container: %w", err)
		}
		r.cmd = ph.Cmd
		r.stdin = ph.Stdin
		r.stdout = ph.Stdout
	} else {
		// Host mode needs nothing agent-specific: the adapter's SubprocessConfig already carries the
		// config-dir env vars it derived from the extension's sandbox_mount declaration.
		cmd := exec.Command(p.spCfg.Binary, p.spCfg.Args...)
		cmd.Dir = p.worktreePath
		// Never hand the agent the server's own environment: it would expose
		// MIKE_LOGIN_PASSWORD / MIKE_ENCRYPTION_KEY to an extension-supplied binary.
		cmd.Env = SafeAgentEnv(p.spCfg.Env...)

		stdin, err := cmd.StdinPipe()
		if err != nil {
			p.adapter.Close()
			return nil, fmt.Errorf("create stdin pipe: %w", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			stdin.Close()
			p.adapter.Close()
			return nil, fmt.Errorf("create stdout pipe: %w", err)
		}
		cmd.Stderr = nil // stderr goes to parent

		if err := cmd.Start(); err != nil {
			stdin.Close()
			stdout.Close()
			p.adapter.Close()
			return nil, fmt.Errorf("start agent process: %w", err)
		}

		r.cmd = cmd
		r.stdin = stdin
		r.stdout = stdout
	}

	// Start stdout reader goroutine
	// readCtx is cancelled when the runner closes (r.done closed), providing
	// cancellation for ParseOutput and HandleUIRequest calls in the reader.
	readCtx, readCancel := context.WithCancel(context.Background())
	go func() {
		<-r.done
		readCancel()
	}()
	go r.readOutput(readCtx)

	logging.For(logging.Agent).With(logging.TaskID, taskID).Infof("runner started (binary=%s, pid=%d)", p.spCfg.Binary, r.PID())
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

	bytes := r.adapter.EncodeCommand(ctx, pkgagent.CommandPrompt, dataBytes)
	if bytes == nil {
		return fmt.Errorf("adapter EncodeCommand returned nil (adapter may be broken)")
	}
	_, err := r.stdin.Write(bytes)
	return err
}

// EventChan returns the channel for receiving agent events.
func (r *AgentRunner) EventChan() <-chan pkgagent.Event {
	return r.eventChan
}

// Compact sends a compact command and waits for the result.
func (r *AgentRunner) Compact(ctx context.Context) (*pkgagent.CompactResult, error) {
	r.mu.Lock()
	if !r.closed {
		bytes := r.adapter.EncodeCommand(ctx, pkgagent.CommandCompact, nil)
		if bytes != nil {
			r.stdin.Write(bytes) // best-effort
		}
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
func (r *AgentRunner) QueryStats(ctx context.Context) pkgagent.SessionStatsData {
	r.mu.Lock()
	if !r.closed {
		bytes := r.adapter.EncodeCommand(ctx, pkgagent.CommandStats, nil)
		if bytes != nil {
			r.stdin.Write(bytes) // best-effort
		}
	}
	r.mu.Unlock()

	select {
	case stats := <-r.statsChan:
		return stats
	case <-ctx.Done():
		return r.LastStats()
	case <-time.After(3 * time.Second):
		return r.LastStats()
	}
}

func (r *AgentRunner) QueryModel(ctx context.Context) string {
	r.mu.Lock()
	if !r.closed {
		bytes := r.adapter.EncodeCommand(ctx, pkgagent.CommandGetState, nil)
		if bytes != nil {
			r.stdin.Write(bytes) // best-effort
		}
	}
	r.mu.Unlock()

	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()

	select {
	case <-r.modelUpdated:
	case <-timeout.C:
	case <-ctx.Done():
		return r.ModelName()
	}

	return r.ModelName()
}

// SetModel sends a set_model command to the agent and waits for the model to update.
// Returns immediately if the runner is closed or context is cancelled.
func (r *AgentRunner) SetModel(ctx context.Context, modelName string) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	data, _ := json.Marshal(map[string]any{"model_name": modelName})
	bytes := r.adapter.EncodeCommand(ctx, pkgagent.CommandSetModel, data)
	if bytes != nil {
		r.stdin.Write(bytes) // best-effort
	}
	r.mu.Unlock()

	// Wait for the model to be updated (via response parsing → modelUpdated channel)
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()

	select {
	case <-r.modelUpdated:
		// Model updated successfully
	case <-timeout.C:
		logging.For(logging.Agent).Warnf("SetModel timed out waiting for response")
	case <-ctx.Done():
		// Context cancelled
	}
}

// SwitchSession sends a switch_session command to load a Pi session file.
// Returns true if the command was sent successfully.
func (r *AgentRunner) SwitchSession(ctx context.Context, sessionPath string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	data, _ := json.Marshal(map[string]any{"session_path": sessionPath})
	bytes := r.adapter.EncodeCommand(ctx, pkgagent.CommandSwitchSession, data)
	if bytes == nil {
		return false
	}
	_, err := r.stdin.Write(bytes)
	return err == nil
}

// SessionFile returns the session file path discovered via get_state.
func (r *AgentRunner) SessionFile() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionFile
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

// ProviderName returns the agent provider name discovered from persist entries.
func (r *AgentRunner) ProviderName() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.providerName
}

// LastStats returns the last known session stats. Prefer sessionCost
// (accumulated from message_end + persist entries) over lastStats.Cost
// because get_session_stats may return NaN/null cost (Pi agent bug).
func (r *AgentRunner) LastStats() pkgagent.SessionStatsData {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.lastStats
	if r.sessionCost > 0 {
		s.Cost = r.sessionCost
	}
	return s
}

// SessionCost returns the accumulated cost from persist entries.
// This is a reliable fallback when get_session_stats returns NaN/null cost.
func (r *AgentRunner) SessionCost() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionCost
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

	// Send abort command (best-effort) with a short timeout context
	if r.stdin != nil {
		abortCtx, abortCancel := context.WithTimeout(context.Background(), 2*time.Second)
		bytes := r.adapter.EncodeCommand(abortCtx, pkgagent.CommandAbort, nil)
		abortCancel()
		if bytes != nil {
			r.stdin.Write(bytes) // best-effort
		}
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

	r.closeChannels()
}

// closeChannels tears down the outbound channels exactly once, whichever path gets there
// first: Close() or readOutput's defer. Testing a flag before closing is not atomic - two
// teardowns can both pass and close the same channel twice, which panics the process.
func (r *AgentRunner) closeChannels() {
	r.closeOnce.Do(func() {
		r.sendMu.Lock()
		defer r.sendMu.Unlock()
		r.channelsClosed = true
		close(r.eventChan)
		close(r.done)
	})
}

// readOutput reads raw bytes from agent stdout and feeds them to the adapter.
// ctx is cancelled when the runner closes, providing cancellation for ParseOutput
// and HandleUIRequest calls.
func (r *AgentRunner) readOutput(ctx context.Context) {
	// The process exited (or stdout was closed): no more events will be produced, so
	// release the outbound channels. Close() may have done it already — closeChannels
	// is a no-op in that case.
	defer r.closeChannels()

	buf := make([]byte, 32*1024)
	for {
		n, err := r.stdout.Read(buf)
		if n > 0 {
			events := r.adapter.ParseOutput(ctx, buf[:n])
			for _, event := range events {
				r.dispatchEvent(ctx, event)
			}
		}
		if err != nil {
			if err != io.EOF {
				logging.For(logging.Agent).Warnf("stdout read error: %v", err)
			}
			break
		}
	}
}

// dispatchEvent routes events to the appropriate channel.
// ctx is used for cancellation of HandleUIRequest calls.
func (r *AgentRunner) dispatchEvent(ctx context.Context, event pkgagent.Event) {
	// Route stats to dedicated channel
	if event.Type == pkgagent.EventTypeStats {
		r.mu.Lock()
		// Merge stats: use full snapshot from get_session_stats, or accumulate
		// per-message cost from message_end events.
		if event.Stats.ContextWindow > 0 {
			r.lastStats = event.Stats
			// Also accumulate the cost from get_session_stats
			if event.Stats.Cost > 0 {
				r.sessionCost = event.Stats.Cost
			}
		} else if event.Stats.Cost > 0 {
			// Per-message cost from message_end — accumulate additively
			r.sessionCost += event.Stats.Cost
		}
		r.mu.Unlock()
		select {
		case r.statsChan <- event.Stats:
		default:
			logging.For(logging.Agent).Infof("stats channel full, dropping")
		}
		return
	}

	// Route compact responses to dedicated channel
	if event.RawData != nil {
		if cmd, ok := event.RawData["command"].(string); ok && cmd == "compact" {
			result := &pkgagent.CompactResult{}
			if event.Type == pkgagent.EventTypeStatus {
				if summary, ok := event.RawData["summary"].(string); ok {
					result.Summary = summary
				} else if event.RawData["summary"] != nil {
					result.Summary = fmt.Sprintf("%v", event.RawData["summary"])
				}
				if errStr, ok := event.RawData["error"].(string); ok {
					result.Err = fmt.Errorf("%s", errStr)
				} else if event.RawData["error"] != nil {
					result.Err = fmt.Errorf("%v", event.RawData["error"])
				}
				if success, ok := event.RawData["success"].(bool); ok && !success {
					if result.Err == nil {
						result.Err = fmt.Errorf("compaction failed")
					}
				}
			} else if event.Type == pkgagent.EventTypeError {
				if msg, ok := event.RawData["message"].(string); ok {
					result.Err = fmt.Errorf("%s", msg)
				} else {
					result.Err = fmt.Errorf("compaction failed")
				}
			}
			select {
			case r.compactChan <- result:
			default:
				logging.For(logging.Agent).Infof("compact channel full, dropping")
			}
			return
		}
	}

	// Route UI requests through adapter
	if event.Type == pkgagent.EventTypeStatus && event.RawData != nil {
		if method, ok := event.RawData["ui_request_method"].(string); ok && method != "" {
			if uiID, ok := event.RawData["ui_request_id"].(string); ok {
				respBytes := r.adapter.HandleUIRequest(ctx, uiID, method)
				if respBytes != nil && r.stdin != nil {
					r.mu.Lock()
					if !r.closed {
						r.stdin.Write(respBytes)
					}
					r.mu.Unlock()
				}
			}
		}

		// Update model name if provided
		if modelName, ok := event.RawData["model_name"].(string); ok && modelName != "" {
			r.mu.Lock()
			r.modelName = modelName
			r.mu.Unlock()
			select {
			case r.modelUpdated <- struct{}{}:
			default:
			}
		}
		// Update session file path if provided
		if sessionFile, ok := event.RawData["session_file"].(string); ok && sessionFile != "" {
			r.mu.Lock()
			r.sessionFile = sessionFile
			r.mu.Unlock()
		}
	}

	// Capture provider name and accumulate cost from persist entries
	if event.Type == pkgagent.EventTypePersist && len(event.PersistEntries) > 0 {
		var turnCost float64
		for _, entry := range event.PersistEntries {
			if entry.ProviderName != "" {
				r.providerName = entry.ProviderName
			}
			turnCost += entry.Cost
		}
		if turnCost > 0 {
			r.mu.Lock()
			r.sessionCost += turnCost
			r.mu.Unlock()
		}
	}

	// Send to main event channel (non-blocking). The read lock spans the send itself:
	// closeChannels needs the write lock, so an in-flight send can never hit a closed
	// channel. Non-blocking is what makes holding the lock here safe.
	r.sendMu.RLock()
	if !r.channelsClosed {
		select {
		case r.eventChan <- event:
		default:
			logging.For(logging.Agent).Infof("event channel full, dropping type=%s", event.Type)
		}
	}
	r.sendMu.RUnlock()
}
