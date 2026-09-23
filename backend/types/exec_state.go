package types

// ExecutionState is where a task's run stands right now, independent of its board column:
// a card can sit in In Progress while its execution is idle, paused or pulling an image.
// The values cross SQLite, JSON, WebSocket frames and the UI's spinner logic, so they are
// the wire format and must not change.
//
// merge_conflict is deliberately absent: a conflict lives in Task.HasMergeConflict, so
// execution_state can keep saying "running" while the user resolves it.
type ExecutionState string

const (
	// ExecStateIdle: no executor is running this task.
	ExecStateIdle ExecutionState = "idle"
	// ExecStateRunning: an executor holds the task and is talking to the model.
	ExecStateRunning ExecutionState = "running"
	// ExecStatePullingImage: the sandbox container is being built or pulled - its own
	// state, not running, because the UI shows progress for it.
	ExecStatePullingImage ExecutionState = "pulling_image"
	// ExecStatePaused: the user stopped an in-flight run; the worktree and branch are
	// intact and the task resumes from here.
	ExecStatePaused ExecutionState = "paused"
	// ExecStateCompleted: the run finished and its work was committed/merged.
	ExecStateCompleted ExecutionState = "completed"
	// ExecStateFailed: the run ended on an error.
	ExecStateFailed ExecutionState = "failed"
)

// InFlight reports whether a run is live rather than waiting. store.inFlightStates is the same set in
// SQL; terminalTurnStates (backend/stream) and taskIsInFlight (frontend) also count "paused", and are a
// different question.
func (s ExecutionState) InFlight() bool {
	return s == ExecStateRunning || s == ExecStatePullingImage
}

// ParseExecutionState maps a client-supplied string onto an ExecutionState,
// reporting whether it is one of the six.
//
// The partial-update path in the task API accepts execution_state, so without this a
// client could park a task in a state no code ever reads — after which no spinner,
// badge or start/stop transition matches it, and the card looks stuck with nothing in
// the UI able to move it.
func ParseExecutionState(s string) (ExecutionState, bool) {
	switch ExecutionState(s) {
	case ExecStateIdle, ExecStateRunning, ExecStatePullingImage,
		ExecStatePaused, ExecStateCompleted, ExecStateFailed:
		return ExecutionState(s), true
	default:
		return "", false
	}
}
