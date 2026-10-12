package exec

import (
	"mike/backend/sandbox"
	"mike/backend/types"

	"mike/pkg/bridge"
	"mike/pkg/logging"
)

// bridgeSandboxManager is a subset of SandboxManager for bridge recovery,
// enabling tests without gVisor.
type bridgeSandboxManager interface {
	// GetClient returns the agent client for a task, reconnecting if needed.
	GetClient(taskID string) (*bridge.Client, error)
	// ContainerIsRunning reports if the task's sandbox container is running.
	ContainerIsRunning(taskID string) bool
	// Stop tears down the task's sandbox container, agent, and client.
	Stop(taskID string) error
	// Create provisions a new sandbox container and agent for the task.
	Create(taskID, worktreePath string, project *types.Project, opts sandbox.SandboxOpts) ([]types.AssignedPort, error)
}

// maxBridgeRecoveryAttempts is the maximum consecutive recovery attempts per task.
const maxBridgeRecoveryAttempts = 3

// recoverBridgeForTask attempts to restore the sandbox bridge for a task after a failure.
func (te *TaskExecutor) recoverBridgeForTask(mgr bridgeSandboxManager, setup *taskExecSetup, state *executionState, imageToUse string, onRecovered func()) bool {
	maxAttempts := maxBridgeRecoveryAttempts

	te.taskMu.Lock()
	if state.bridgeRecoveryAttempts >= maxAttempts {
		te.taskMu.Unlock()
		logging.For(logging.Bridge).With(logging.TaskID, setup.task.ID).Infof("max attempts (%d) reached, giving up", maxAttempts)
		return false
	}
	state.bridgeRecoveryAttempts++
	attempt := state.bridgeRecoveryAttempts
	te.taskMu.Unlock()

	logging.For(logging.Bridge).With(logging.TaskID, setup.task.ID).Infof("attempt %d: trying client reconnect", attempt)
	newClient, err := mgr.GetClient(setup.task.ID)
	if err == nil {
		probeErr := newClient.Ping(setup.ctx)
		if probeErr == nil {
			logging.For(logging.Bridge).With(logging.TaskID, setup.task.ID).Infof("client reconnect successful")
			state.setSandbox(func(sb *sandboxState) {
				sb.exec, sb.execStream, sb.read, sb.write, sb.edit = makeSandboxCallbacks(setup.task.ID, newClient)
			})
			te.resetBridgeRecoveryAttempts(state)
			return true
		}
		logging.For(logging.Bridge).With(logging.TaskID, setup.task.ID).Warnf("client probe failed: %v, escalating to sandbox recreation", probeErr)
		err = probeErr
	}

	// Recreate sandbox if agent is dead.
	if mgr.ContainerIsRunning(setup.task.ID) {
		logging.For(logging.Bridge).With(logging.TaskID, setup.task.ID).Warnf("client reconnect failed (%v), recreating sandbox", err)
		if stopErr := mgr.Stop(setup.task.ID); stopErr != nil {
			logging.For(logging.Bridge).With(logging.TaskID, setup.task.ID).Warnf("failed to stop old sandbox: %v", stopErr)
		}
	} else {
		logging.For(logging.Bridge).With(logging.TaskID, setup.task.ID).Infof("container not running, creating sandbox from scratch")
	}
	if _, createErr := mgr.Create(setup.task.ID, setup.worktreePath, setup.project, te.sandboxCreateOpts(setup, imageToUse, setup.task.IsPlanMode())); createErr != nil {
		logging.For(logging.Bridge).With(logging.TaskID, setup.task.ID).Warnf("failed to recreate sandbox: %v", createErr)
		return false
	}
	newClient, clientErr := mgr.GetClient(setup.task.ID)
	if clientErr != nil {
		logging.For(logging.Bridge).With(logging.TaskID, setup.task.ID).Warnf("failed to connect after recreation: %v", clientErr)
		return false
	}
	if probeErr := newClient.Ping(setup.ctx); probeErr != nil {
		logging.For(logging.Bridge).With(logging.TaskID, setup.task.ID).Warnf("probe failed after recreation: %v", probeErr)
		return false
	}

	logging.For(logging.Bridge).With(logging.TaskID, setup.task.ID).Infof("sandbox recreated successfully")
	state.setSandbox(func(sb *sandboxState) {
		sb.exec, sb.execStream, sb.read, sb.write, sb.edit = makeSandboxCallbacks(setup.task.ID, newClient)
	})
	te.resetBridgeRecoveryAttempts(state)
	if onRecovered != nil {
		onRecovered()
	}
	return true
}
