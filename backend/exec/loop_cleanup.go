package exec

import (
	"fmt"
	"mike/pkg/logging"
)

// The cleanup that runs when a task loop exits.
// The order is a contract:
//
//  1. close(setup.done)          — stop taking user messages
//  2. persistSessionCost         — save accumulated cost
//  3. releaseTaskSlot            — release per-provider running-task slot
//  4. recoverTaskFromPanic       — handle panics and transition row state
//  5. forgetTaskSetup            — drop tasks-map entry if not an agent
//  6. releaseAssignedPorts       — clear host port forwards
//  7. close(setup.finished)      — LAST, signal that all writes above have landed
//
// recover() stays in executeTask's deferred closure to report the correct unwound function.
//
// The stream and sandbox container are not cleaned up here: the broadcaster lives until
// clients disconnect, and the container lives until task deletion or memory supervisor reclaim.

// persistSessionCost saves the run's accumulated cost. It uses the loop context
// because cost lives only in memory while the loop runs.
func (te *TaskExecutor) persistSessionCost(setup *taskExecSetup, lc *loopContext) {
	if lc == nil {
		return
	}
	setup.task.SessionCost = lc.sessionCost
	_ = te.store.UpdateTaskSessionCost(setup.task.ID, lc.sessionCost)
}

// recoverTaskFromPanic reports a panic and takes the task out of its in-flight state.
// `r` is the value returned by recover().
func (te *TaskExecutor) recoverTaskFromPanic(setup *taskExecSetup, r any) {
	logging.For(logging.Recovery).With(logging.TaskID, setup.task.ID).Errorf("recovered from panic: %v", r)
	te.sendAndPersistError(setup.task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
	te.handleCrashedTask(setup.task.ID, setup.projectID)
}

// forgetTaskSetup removes the in-flight registry entry for this run if it still owns it.
// Agent executors keep the entry for on-demand compaction.
func (te *TaskExecutor) forgetTaskSetup(setup *taskExecSetup) {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	if state, exists := te.tasks[setup.task.ID]; exists && state.ctx == setup.ctx {
		if state.executorType != "agent" {
			delete(te.tasks, setup.task.ID)
		}
	}
}

// releaseAssignedPorts clears host port forwards. It checks the store directly to avoid
// using a stale local copy of the task.
func (te *TaskExecutor) releaseAssignedPorts(setup *taskExecSetup) {
	row, err := te.store.GetTaskByID(setup.task.ID)
	if !((err == nil && len(row.AssignedPorts) > 0) || len(setup.task.AssignedPorts) > 0) {
		return
	}
	setup.task.AssignedPorts = nil
	_ = te.store.SetTaskAssignedPorts(setup.task.ID, setup.projectID, nil)
	te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
}
