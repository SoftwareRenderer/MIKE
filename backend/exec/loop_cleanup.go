package exec

import (
	"fmt"
	"kanban/pkg/logging"
)

// The cleanup that runs when a task loop exits, as an ordered list rather than 50 lines inside a defer.
// The order is the contract and it is not interchangeable:
//
//  1. close(setup.done)          — the executor has stopped taking user messages
//  2. persistSessionCost         — the run's accumulated cost, before anything reads the row again
//  3. releaseTaskSlot            — the per-provider running-task slot
//  4. recoverTaskFromPanic       — only on a panic; transitions the row out of its in-flight state
//  5. forgetTaskSetup            — drop the tasks-map entry, unless a replacement executor owns it
//  6. releaseAssignedPorts       — the host ports this run forwarded
//  7. close(setup.finished)      — LAST, deferred at the top, so a caller waiting on it (StopTask)
//                                  knows every write above has landed even if one of them panics
//
// `recover()` itself stays in executeTask's deferred closure: it only reports when called directly by the
// function that the panic unwound through, so a helper cannot own it.
//
// What is deliberately absent: the stream and the sandbox container. The broadcaster lives as long as its
// clients are connected, and the container lives until the task is deleted (RemoveStream in cleanupTask).

// persistSessionCost writes the cost accumulated during this run. It takes the loop context rather than
// the store's state because the cost lives only in memory while the loop runs.
func (te *TaskExecutor) persistSessionCost(setup *taskExecSetup, lc *loopContext) {
	if lc == nil {
		return
	}
	setup.task.SessionCost = lc.sessionCost
	_ = te.store.UpdateTaskSessionCost(setup.task.ID, lc.sessionCost)
}

// recoverTaskFromPanic reports a panic that unwound the loop and takes the task out of its in-flight state,
// so a crash cannot leave a row that reads "running" forever. `r` is what recover() returned.
func (te *TaskExecutor) recoverTaskFromPanic(setup *taskExecSetup, r any) {
	logging.For(logging.Recovery).With(logging.TaskID, setup.task.ID).Errorf("recovered from panic: %v", r)
	te.sendAndPersistError(setup.task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
	te.handleCancelledTask(setup.task.ID, setup.projectID)
}

// forgetTaskSetup removes the in-flight registry entry for this run — only if this run still owns it. A
// replacement executor (a type switch mid-run) owns the entry by then, and deleting its row would let a
// third start believe nothing is running. An agent executor keeps its setup alive for on-demand
// compaction, so its entry stays with its status cleared.
func (te *TaskExecutor) forgetTaskSetup(setup *taskExecSetup) {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	if state, exists := te.tasks[setup.task.ID]; exists && state.ctx == setup.ctx {
		if state.executorType != "agent" {
			delete(te.tasks, setup.task.ID)
		} else {
			// Clear status so HasRunningTasks doesn't count completed agent tasks
			state.taskStatus = ""
		}
	}
}

// releaseAssignedPorts clears the host ports this run forwarded. The decision is asked of the ROW, not of
// this loop's copy of the task: a terminal that forwarded a port mid-run writes that column directly, so
// the copy can be behind, and a stale copy would leave a live host port bound to a stopped task. The
// in-memory check is only the fallback for a failed read. The write is column-only because the task may
// already be stopped, and a full-row write would put "running" back.
func (te *TaskExecutor) releaseAssignedPorts(setup *taskExecSetup) {
	row, err := te.store.GetTaskByID(setup.task.ID)
	if !((err == nil && len(row.AssignedPorts) > 0) || len(setup.task.AssignedPorts) > 0) {
		return
	}
	setup.task.AssignedPorts = nil
	_ = te.store.SetTaskAssignedPorts(setup.task.ID, setup.projectID, nil)
	te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
}
