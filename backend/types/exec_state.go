package types

// ExecutionState is where a task's run stands right now, independent of its board column.
// The values cross SQLite, JSON and WebSocket frames, so they are the wire format and must not change.
//
// merge_conflict is deliberately absent: a conflict lives in Task.HasMergeConflict, so the state can keep
// saying "running" while the user resolves it.
type ExecutionState string

const (
	// ExecStateIdle: no executor is running this task.
	ExecStateIdle ExecutionState = "idle"
	// ExecStateRunning: an executor holds the task and is talking to the model.
	ExecStateRunning ExecutionState = "running"
	// ExecStatePullingImage: the sandbox container is being built or pulled. Not "running": the UI
	// shows progress for it.
	ExecStatePullingImage ExecutionState = "pulling_image"
	// ExecStatePaused: the user stopped an in-flight run, which resumes from here.
	ExecStatePaused ExecutionState = "paused"
	// ExecStateCompleted: the run finished and its work was committed and merged.
	ExecStateCompleted ExecutionState = "completed"
	// ExecStateFailed: the run ended on an error.
	ExecStateFailed ExecutionState = "failed"
)

// InFlight reports whether a run is live rather than waiting. store.inFlightStates is the same set in SQL
// and taskIsInFlight (frontend) mirrors it; terminalTurnStates (backend/stream) counts "paused" and answers
// a different question - which states leave nothing owed to a subscriber.
func (s ExecutionState) InFlight() bool {
	return s == ExecStateRunning || s == ExecStatePullingImage
}

// ParseExecutionState maps a client-supplied string onto an ExecutionState, reporting whether it is one of
// the six. The task API accepts execution_state in a partial update, so an unknown value must be refused here:
// a task parked in a state no code reads has no spinner, badge or start/stop transition that matches it.
func ParseExecutionState(s string) (ExecutionState, bool) {
	switch ExecutionState(s) {
	case ExecStateIdle, ExecStateRunning, ExecStatePullingImage,
		ExecStatePaused, ExecStateCompleted, ExecStateFailed:
		return ExecutionState(s), true
	default:
		return "", false
	}
}
