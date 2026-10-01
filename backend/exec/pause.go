package exec

import (
	"mike/backend/types"
	"mike/pkg/logging"
)

// A pause is written to the rows rather than derived from them: `paused` on a task stops it and every
// descendant that could still run. The scheduler reads those rows, so a stopped branch stays stopped
// whatever its runs report (isIncompleteForStart, AutoStartNextSibling), and a start clears the branch it
// names — resumeBranch here, memory_supervisor.go for a pause that supervisor wrote.

// branchRows lists a task and everything under it, deepest first with the named task last, so no parent
// reads stopped while one of its children still reads running. A descendant list that cannot be read is
// reported and the named task is still answered.
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

// moveBranch writes one execution state over another on every row it is handed, broadcasting each move,
// and returns the rows that had moved on in the meantime (store.TransitionTaskExecution).
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

// resumeBranch clears the pause on a task and everything under it, because starting a task by hand is that
// branch being answered: what runs next is the schedule's decision. Rows in any other state are left
// alone — a run owns its row, and a turn waiting in Review waits for its person.
func (te *TaskExecutor) resumeBranch(taskID string) {
	rows, err := te.branchRows(taskID)
	if err != nil {
		return
	}
	te.moveBranch(rows, types.ExecStatePaused, types.ExecStateIdle)
}

// PauseTaskAndDescendants stops a task and everything under it: it cancels every run in the branch and
// marks every row that was waiting to run, and every other row stays where it is. The mark comes before the
// cancelling because a cancelled run reports its exit to the scheduler (loop.go, loop_agent.go), which
// reads these rows and would otherwise continue the branch.
func (te *TaskExecutor) PauseTaskAndDescendants(taskID string) error {
	order, err := te.branchRows(taskID)
	if err != nil {
		return err
	}

	// Which rows hold a run decides which get cancelled: a run queued for a provider slot rests
	// in_progress+idle too, and that is a state the mark writes.
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

	// A row the write refused had moved on, so re-read it rather than guess whether a run now holds it.
	for _, row := range te.moveBranch(waiting, types.ExecStateIdle, types.ExecStatePaused) {
		if fresh, rerr := te.store.GetTaskByID(row.ID); rerr == nil && fresh.ExecutionState.InFlight() {
			held[row.ID] = true
		}
	}

	// A row holding a run goes through StopTask, the transition that writes the text the row reports. The
	// named task is excluded here and answered by its own call below.
	for _, row := range order[:len(order)-1] {
		if !held[row.ID] && !te.hasLiveExecutor(row.ID) {
			continue
		}
		if err := te.StopTask(row.ID); err != nil {
			logging.For(logging.Exec).With(logging.TaskID, row.ID).Warnf("pause: stopping descendant: %v", err)
		}
	}

	// A descendant that would not stop is a log line; the named task's error is the caller's to report.
	return te.StopTask(taskID)
}
