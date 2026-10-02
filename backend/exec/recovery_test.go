package exec

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/sandbox"
	"mike/backend/types"

	"mike/pkg/bridge"
	"mike/pkg/bridge/protocol"
)

// ---------------------------------------------------------------------------
// Mock bridge agent + fake sandbox manager (no gVisor required)
// ---------------------------------------------------------------------------

// spawnMockBridge starts a fake sandbox-bridge agent speaking the real bridge
// protocol over pipes, and returns a Client connected to it. It answers read
// (used by Ping) and bash requests like the real agent.
func spawnMockBridge() (*bridge.Client, func()) {
	agentReadPr, agentReadPw := io.Pipe()   // client → agent (requests)
	clientReadPr, clientReadPw := io.Pipe() // agent → client (responses)

	client := bridge.NewClient(agentReadPw, clientReadPr)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			frame, err := protocol.ReadFrame(agentReadPr)
			if err != nil {
				return
			}
			req, err := protocol.ParseRequest(frame)
			if err != nil {
				continue
			}
			switch req.Method {
			case protocol.MethodRead:
				_ = protocol.WriteFrame(clientReadPw, protocol.NewResult(req.MsgID, protocol.NewUint64Result(0)))
			case protocol.MethodBash:
				_ = protocol.WriteFrame(clientReadPw, protocol.NewChunk(req.MsgID, protocol.NewBashChunk(0, []byte("hello\n"))))
				_ = protocol.WriteFrame(clientReadPw, protocol.NewResult(req.MsgID, protocol.NewBashResult(0, []byte(`{}`))))
			default:
				_ = protocol.WriteFrame(clientReadPw, protocol.NewError(req.MsgID, "unsupported method"))
			}
		}
	}()

	stop := func() {
		_ = client.Close()
		_ = agentReadPw.Close()
		_ = clientReadPw.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	return client, stop
}

// fakeSandboxMgr models SandboxManager's task-sandbox lifecycle for unit tests:
//   - GetClient returns the cached client while its agent is alive, otherwise
//     fails ("no agent process") — mirroring the real manager after Stop.
//   - Stop closes the client and marks the sandbox gone (container stopped).
//   - Create provisions a fresh agent+client (container recreated from scratch).
type fakeSandboxMgr struct {
	mu          sync.Mutex
	taskID      string
	running     bool // container tracked + running
	agentAlive  bool // agent process (and its pipes) present
	client      *bridge.Client
	stopAgent   func()
	allStops    []func()
	createErr   error
	createCalls int
	stopCalls   int
	getCalls    int
}

func newFakeSandboxMgr(taskID string) *fakeSandboxMgr {
	return &fakeSandboxMgr{taskID: taskID}
}

func (f *fakeSandboxMgr) spawnLocked() *bridge.Client {
	client, stop := spawnMockBridge()
	f.client = client
	f.stopAgent = stop
	f.allStops = append(f.allStops, stop)
	f.agentAlive = true
	f.running = true
	return client
}

func (f *fakeSandboxMgr) GetClient(taskID string) (*bridge.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if !f.agentAlive {
		return nil, fmt.Errorf("no agent process for task %s", taskID)
	}
	if f.client != nil && f.client.IsAlive() {
		return f.client, nil
	}
	// Fresh client on existing (alive) agent pipes — mirrors real GetClient.
	return f.spawnLocked(), nil
}

func (f *fakeSandboxMgr) ContainerIsRunning(taskID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *fakeSandboxMgr) Stop(taskID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	if f.stopAgent != nil {
		f.stopAgent()
		f.stopAgent = nil
	}
	f.client = nil
	f.agentAlive = false
	f.running = false
	return nil
}

func (f *fakeSandboxMgr) Create(taskID, worktreePath string, project *types.Project, opts sandbox.SandboxOpts) ([]types.AssignedPort, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.spawnLocked()
	return nil, nil
}

// cleanup tears down every mock agent goroutine spawned during the test.
func (f *fakeSandboxMgr) cleanup() {
	f.mu.Lock()
	stops := f.allStops
	f.allStops = nil
	f.mu.Unlock()
	for _, s := range stops {
		s()
	}
}

func recoveryTestSetup(taskID string) (*taskExecSetup, *executionState) {
	setup := &taskExecSetup{
		task:         &types.Task{ID: taskID, OperatingMode: "full_access"},
		project:      &types.Project{Name: "test-proj"},
		worktreePath: "/tmp/worktree",
		ctx:          context.Background(),
	}
	return setup, &executionState{}
}

// runBashThroughState executes a bash command through the state's streaming
// callbacks exactly like executeTool's runBash does.
func runBashThroughState(t *testing.T, state *executionState, cmd string) (string, error) {
	t.Helper()
	if state.sandboxExecStream == nil {
		t.Fatal("state.sandboxExecStream is nil")
	}
	return state.sandboxExecStream(context.Background(), cmd, 5, func(string) {})
}

// ---------------------------------------------------------------------------
// Tier 1: client reconnect on live agent pipes
// ---------------------------------------------------------------------------

func TestRecoverBridgeForTask_Tier1ClientReconnect(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxMgr("task-tier1")
	defer f.cleanup()
	setup, state := recoveryTestSetup("task-tier1")

	// Initial sandbox (like setupSandbox): Create then GetClient.
	if _, err := f.Create(setup.task.ID, setup.worktreePath, setup.project, sandbox.SandboxOpts{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	initialCreateCalls := f.createCalls
	client0, err := f.GetClient(setup.task.ID)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	state.sandboxExec, state.sandboxExecStream, state.sandboxRead, state.sandboxWrite, state.sandboxEdit = makeSandboxCallbacks(setup.task.ID, client0)

	// Bridge failure: the captured client is closed (agent process still alive).
	if err := client0.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	if _, err := runBashThroughState(t, state, "echo hi"); err == nil || !strings.Contains(err.Error(), "client closed") {
		t.Fatalf("expected 'client closed' error, got: %v", err)
	}

	// Recovery reconnects a fresh client on the live agent pipes.
	ok := newBareTestExecutor(t).recoverBridgeForTask(f, setup, state, "img", nil)
	if !ok {
		t.Fatal("expected Tier 1 recovery to succeed")
	}
	if state.bridgeRecoveryAttempts != 0 {
		t.Errorf("attempts = %d, want 0 after success", state.bridgeRecoveryAttempts)
	}
	if f.createCalls != initialCreateCalls {
		t.Errorf("createCalls = %d, want %d (Tier 1 should not recreate)", f.createCalls, initialCreateCalls)
	}
	if f.getCalls != 2 {
		t.Errorf("getCalls = %d, want 2", f.getCalls)
	}

	// Retried bash through the refreshed callbacks must now succeed.
	out, err := runBashThroughState(t, state, "echo hi")
	if err != nil {
		t.Fatalf("bash after Tier 1 recovery: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("bash output = %q, want %q", out, "hello\n")
	}
}

// ---------------------------------------------------------------------------
// Tier 2: agent died inside a still-running container → recreate sandbox
// ---------------------------------------------------------------------------

func TestRecoverBridgeForTask_Tier2RecreateRunningContainer(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxMgr("task-tier2")
	defer f.cleanup()
	setup, state := recoveryTestSetup("task-tier2")

	// Initial sandbox (like setupSandbox): Create then GetClient.
	if _, err := f.Create(setup.task.ID, setup.worktreePath, setup.project, sandbox.SandboxOpts{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	initialCreateCalls := f.createCalls
	client0, err := f.GetClient(setup.task.ID)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	state.sandboxExec, state.sandboxExecStream, state.sandboxRead, state.sandboxWrite, state.sandboxEdit = makeSandboxCallbacks(setup.task.ID, client0)

	// Agent dies inside a live container: client closed, agent gone, container
	// still tracked as running.
	if err := client0.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	f.mu.Lock()
	f.agentAlive = false
	f.mu.Unlock()

	ok := newBareTestExecutor(t).recoverBridgeForTask(f, setup, state, "img", nil)
	if !ok {
		t.Fatal("expected Tier 2 recovery to succeed")
	}
	if f.stopCalls != 1 {
		t.Errorf("stopCalls = %d, want 1", f.stopCalls)
	}
	if f.createCalls != initialCreateCalls+1 {
		t.Errorf("createCalls = %d, want %d", f.createCalls, initialCreateCalls+1)
	}

	out, err := runBashThroughState(t, state, "echo hi")
	if err != nil {
		t.Fatalf("bash after Tier 2 recovery: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("bash output = %q, want %q", out, "hello\n")
	}
}

// ---------------------------------------------------------------------------
// A container stopped or removed while its task still runs (an image rebuild calls
// StopProjectContainers) is recovered by creating a new sandbox, not given up on.
// ---------------------------------------------------------------------------

func TestRecoverBridgeForTask_CreatesSandboxFromScratchWhenStopped(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxMgr("task-stopped")
	defer f.cleanup()
	setup, state := recoveryTestSetup("task-stopped")

	// Initial sandbox (like setupSandbox): Create then GetClient.
	if _, err := f.Create(setup.task.ID, setup.worktreePath, setup.project, sandbox.SandboxOpts{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	initialCreateCalls := f.createCalls
	client0, err := f.GetClient(setup.task.ID)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	state.sandboxExec, state.sandboxExecStream, state.sandboxRead, state.sandboxWrite, state.sandboxEdit = makeSandboxCallbacks(setup.task.ID, client0)

	// Image rebuild (or stale-container cleanup / container crash): the whole
	// sandbox is stopped while the task keeps executing.
	if err := f.Stop(setup.task.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// The task loop's next bash call fails with "client closed".
	if _, err := runBashThroughState(t, state, "echo hi"); err == nil || !strings.Contains(err.Error(), "client closed") {
		t.Fatalf("expected 'client closed' error after stop, got: %v", err)
	}

	// Tier 1 cannot help (agent pipes are gone) and the container is not
	// running — recovery must CREATE the sandbox from scratch.
	ok := newBareTestExecutor(t).recoverBridgeForTask(f, setup, state, "img", nil)
	if !ok {
		t.Fatal("recovery should create the sandbox from scratch instead of giving up")
	}
	if f.createCalls != initialCreateCalls+1 {
		t.Errorf("createCalls = %d, want %d", f.createCalls, initialCreateCalls+1)
	}
	if state.bridgeRecoveryAttempts != 0 {
		t.Errorf("attempts = %d, want 0 after success", state.bridgeRecoveryAttempts)
	}

	// Retried bash through the refreshed callbacks succeeds — task un-stuck.
	out, err := runBashThroughState(t, state, "echo hi")
	if err != nil {
		t.Fatalf("bash after from-scratch recovery: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("bash output = %q, want %q", out, "hello\n")
	}
}

// ---------------------------------------------------------------------------
// Recovery failure limits
// ---------------------------------------------------------------------------

func TestRecoverBridgeForTask_MaxAttemptsLatch(t *testing.T) {
	t.Parallel()
	f := newFakeSandboxMgr("task-max")
	defer f.cleanup()
	setup, state := recoveryTestSetup("task-max")

	// Everything fails: no agent, no container, Create errors.
	f.mu.Lock()
	f.agentAlive = false
	f.running = false
	f.createErr = fmt.Errorf("podman unavailable")
	f.mu.Unlock()

	te := newBareTestExecutor(t)
	if te.recoverBridgeForTask(f, setup, state, "img", nil) {
		t.Fatal("expected recovery attempt 1 to fail")
	}
	if state.bridgeRecoveryAttempts != 1 {
		t.Errorf("attempts = %d, want 1", state.bridgeRecoveryAttempts)
	}
	if te.recoverBridgeForTask(f, setup, state, "img", nil) {
		t.Error("expected recovery attempt 2 to fail")
	}
	if te.recoverBridgeForTask(f, setup, state, "img", nil) {
		t.Error("expected recovery attempt 3 to fail")
	}
	if state.bridgeRecoveryAttempts != 3 {
		t.Errorf("attempts = %d, want 3", state.bridgeRecoveryAttempts)
	}
	if f.createCalls != 3 {
		t.Errorf("createCalls = %d, want 3", f.createCalls)
	}

	// Fourth call: latch engaged, no further work performed.
	if te.recoverBridgeForTask(f, setup, state, "img", nil) {
		t.Fatal("expected recovery to give up after max attempts")
	}
	if f.createCalls != 3 {
		t.Errorf("createCalls = %d, want 3 (no attempt after latch)", f.createCalls)
	}
}

// TestExecuteTool_SuccessResetsRecoveryLatch verifies that a successful tool
// call clears the bridge-recovery failure latch, so a later transient sandbox
// outage can still be recovered instead of being permanently stuck.
func TestExecuteTool_SuccessResetsRecoveryLatch(t *testing.T) {
	t.Parallel()
	te := newBareTestExecutor(t)
	state := &executionState{
		recoverBridge:          func() bool { return true },
		bridgeRecoveryAttempts: 3, // latch previously engaged by failed recoveries
	}

	result, ok := te.executeTool(context.Background(), "ask_question", map[string]any{"question": "hi"}, "/tmp/worktree", &types.Task{}, state, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if state.bridgeRecoveryAttempts != 0 {
		t.Errorf("attempts = %d, want 0 after successful tool call", state.bridgeRecoveryAttempts)
	}

	// A failed call must NOT reset the latch.
	state.bridgeRecoveryAttempts = 2
	failingState := &executionState{
		recoverBridge: func() bool { return false },
	}
	_, ok = te.executeTool(context.Background(), "bash", map[string]any{"command": "false"}, "/tmp/worktree", &types.Task{}, failingState, nil)
	if ok {
		t.Fatal("expected bash to fail")
	}
}
