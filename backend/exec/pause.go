package exec

import (
	"mike/backend/types"
	"mike/pkg/logging"
)

// A pause is stored on rows: paused on a task stops it and all descendants. The scheduler reads these
// rows, so a stopped branch stays stopped regardless of run status. A start clears the branch
// (see resumeBranch or memory_supervisor.go).

// branchRows lists a task and its descendants, deepest first, so parents are processed after children.
// If the descendant list cannot be read, the error is logged but the named task is still processed.
func (te *TaskExecutor) branchRows(taskID string) ([]types.Task, error) {
	target, err := te.store.GetTaskByID(taskID)
	if err != nil {
		return nil, err
	}
	descendants, err := te.store.GetAllDescendants(taskID)
	if err != nil {
		logging.For(logging.Exec).With(logging.TaskID, taskID).Warnf("reading the branch: %v", err)
	}
	order := make([]types.Task, 0, len(descendants)+1)
	for i := len(descendants) - 1; i >= 0; i-- {
		order = append(order, descendants[i])
	}
	return append(order, *target), nil
}

// moveBranch transitions the execution state of rows and broadcasts updates. It returns rows that
// have changed since they were passed in.
func (te *TaskExecutor) moveBranch(rows []types.Task, from, to types.ExecutionState) (refused []types.Task) {
	for _, row := range rows {
		moved, err := te.store.TransitionTaskExecution(row.ID, row.ProjectID, from, to)
		if err != nil {
			logging.For(logging.Exec).With(logging.TaskID, row.ID).Warnf("branch %s→%s: %v", from, to, err)
			continue
		}
		if !moved {
			refused = append(refused, row)
			continue
		}
		row.ExecutionState = to
		te.appStreamMgr.BroadcastTaskUpdated(row.ProjectID, &row)
	}
	return refused
}

// resumeBranch clears the pause on a task and its descendants. Manual starts clear the pause;
// the scheduler then decides what runs next. Non-paused rows are ignored.
func (te *TaskExecutor) resumeBranch(taskID string) {
	rows, err := te.branchRows(taskID)
	if err != nil {
		return
	}
	te.moveBranch(rows, types.ExecStatePaused, types.ExecStateIdle)
}

// PauseTaskAndDescendants stops a task and its descendants: it cancels runs and marks waiting rows
// as paused. Rows are marked before cancellation so the scheduler doesn't auto-resume the branch.
func (te *TaskExecutor) PauseTaskAndDescendants(taskID string) error {
	order, err := te.branchRows(taskID)
	if err != nil {
		return err
	}

	// determine which rows hold a run to cancel; provider-queued runs are in_progress+idle.
	held := make(map[string]bool, len(order))
	var waiting []types.Task
	for _, row := range order {
		if row.ExecutionState.InFlight() {
			held[row.ID] = true
		}
		if row.Status == types.StatusInProgress && row.ExecutionState == types.ExecStateIdle {
			waiting = append(waiting, row)
		}
	}

	// Re-read rows that refused transition to check if they are now in-flight.
	for _, row := range te.moveBranch(waiting, types.ExecStateIdle, types.ExecStatePaused) {
		if fresh, rerr := te.store.GetTaskByID(row.ID); rerr == nil && fresh.ExecutionState.InFlight() {
			held[row.ID] = true
		}
	}

	// StopTask is called for rows holding runs. The named task is handled separately at the end.
	for _, row := range order[:len(order)-1] {
		if !held[row.ID] && !te.hasLiveExecutor(row.ID) {
			continue
		}
		if err := te.StopTask(row.ID); err != nil {
			logging.For(logging.Exec).With(logging.TaskID, row.ID).Warnf("pause: stopping descendant: %v", err)
		}
	}

	// Descendant stop failures are logged; the named task's error is returned.
	return te.StopTask(taskID)
}
