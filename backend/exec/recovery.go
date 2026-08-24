package exec

import (
	"log"

	kanban_sandbox "kanban/backend/sandbox"
	"kanban/backend/types"

	sandbox "kanban/pkg/bridge"
)

// bridgeSandboxManager is the subset of SandboxManager operations used by
// bridge recovery. It is extracted as an interface so the recovery flow can be
// unit-tested without a live gVisor container.
type bridgeSandboxManager interface {
	// GetClient returns the agent client for a task, creating a fresh client on
	// the task's agent pipes if the cached one is dead.
	GetClient(taskID string) (*sandbox.Client, error)
	// ContainerIsRunning reports whether the task's sandbox container is
	// tracked and running.
	ContainerIsRunning(taskID string) bool
	// Stop tears down the task's sandbox container, agent process, and client.
	Stop(taskID string) error
	// Create provisions a fresh sandbox container and agent for the task.
	Create(taskID, worktreePath string, project *types.Project, opts kanban_sandbox.SandboxOpts) ([]types.AssignedPort, error)
}

// maxBridgeRecoveryAttempts bounds consecutive recovery attempts per task.
const maxBridgeRecoveryAttempts = 3

// recoverBridgeForTask attempts to restore the sandbox bridge for a task after
// a bridge failure
func (te *TaskExecutor) recoverBridgeForTask(mgr bridgeSandboxManager, setup *taskExecSetup, state *executionState, imageToUse string, agentSandbox *types.AgentSandbox, skillsDir string, onRecovered func()) bool {
	maxAttempts := maxBridgeRecoveryAttempts

	te.taskMu.Lock()
	if state.bridgeRecoveryAttempts >= maxAttempts {
		te.taskMu.Unlock()
		log.Printf("[bridge-recovery] task=%s max attempts (%d) reached, giving up", setup.task.ID, maxAttempts)
		return false
	}
	state.bridgeRecoveryAttempts++
	attempt := state.bridgeRecoveryAttempts
	te.taskMu.Unlock()

	log.Printf("[bridge-recovery] task=%s attempt %d: trying client reconnect", setup.task.ID, attempt)
	// try to get a fresh client
	newClient, err := mgr.GetClient(setup.task.ID)
	if err == nil {
		probeErr := newClient.Ping(setup.ctx)
		if probeErr == nil {
			log.Printf("[bridge-recovery] task=%s client reconnect successful", setup.task.ID)
			te.taskMu.Lock()
			state.sandboxExec, state.sandboxExecStream, state.sandboxRead, state.sandboxWrite, state.sandboxEdit = makeSandboxCallbacks(newClient)
			state.bridgeRecoveryAttempts = 0 // reset on success
			te.taskMu.Unlock()
			return true
		}
		log.Printf("[bridge-recovery] task=%s client probe failed: %v, escalating to sandbox recreation", setup.task.ID, probeErr)
		err = probeErr
	}

	// agent is dead — recreate the sandbox
	if mgr.ContainerIsRunning(setup.task.ID) {
		log.Printf("[bridge-recovery] task=%s client reconnect failed (%v), recreating sandbox", setup.task.ID, err)
		if stopErr := mgr.Stop(setup.task.ID); stopErr != nil {
			log.Printf("[bridge-recovery] task=%s failed to stop old sandbox: %v", setup.task.ID, stopErr)
		}
	} else {
		log.Printf("[bridge-recovery] task=%s container not running, creating sandbox from scratch", setup.task.ID)
	}
	if _, createErr := mgr.Create(setup.task.ID, setup.worktreePath, setup.project, kanban_sandbox.SandboxOpts{
		BaseImage:        imageToUse,
		NetworkIsolation: setup.sandboxOpts.NetworkIsolation,
		MemoryLimit:      setup.sandboxOpts.MemoryLimit,
		ForwardPorts:     setup.sandboxOpts.ForwardPorts,
		AgentSandbox:     agentSandbox,
		SkillsDir:        skillsDir,
		SandboxMounts:    setup.sandboxOpts.SandboxMounts,
		SandboxEnvs:      setup.sandboxOpts.SandboxEnvs,
		Capabilities:     setup.sandboxOpts.Capabilities,
		PlanMode:         setup.task.IsPlanMode(),
	}); createErr != nil {
		log.Printf("[bridge-recovery] task=%s failed to recreate sandbox: %v", setup.task.ID, createErr)
		return false
	}
	newClient, clientErr := mgr.GetClient(setup.task.ID)
	if clientErr != nil {
		log.Printf("[bridge-recovery] task=%s failed to connect after recreation: %v", setup.task.ID, clientErr)
		return false
	}
	if probeErr := newClient.Ping(setup.ctx); probeErr != nil {
		log.Printf("[bridge-recovery] task=%s probe failed after recreation: %v", setup.task.ID, probeErr)
		return false
	}

	log.Printf("[bridge-recovery] task=%s sandbox recreated successfully", setup.task.ID)
	te.taskMu.Lock()
	state.sandboxExec, state.sandboxExecStream, state.sandboxRead, state.sandboxWrite, state.sandboxEdit = makeSandboxCallbacks(newClient)
	state.bridgeRecoveryAttempts = 0 // reset on success
	te.taskMu.Unlock()
	if onRecovered != nil {
		onRecovered()
	}
	return true
}
