package types

// OperatingMode is the permission set a task's turn runs under. It decides which tool set the model
// is offered and whether the worktree is mounted read-only; the prompt text is only advisory, so
// these values are what actually contain a planning turn.
//
// The values cross SQLite, JSON, WebSocket frames and the prompt builder: they are the wire format
// and must not change.
type OperatingMode string

const (
	// ModeAct is the default: the agent may write, edit and run.
	ModeAct OperatingMode = "act"
	// ModePlan offers read-only tools and a read-only mount, for a turn that produces a plan.
	// It is the only mode whose worktree is mounted read-only.
	ModePlan OperatingMode = "plan"
	// ModeExplore is a turn that reads code: the act tool set plus the task-board tools, and a
	// writable mount, because exploring includes running the thing. It differs from act in never
	// getting a loop hint, and in staying out of the autostart auto-answer path.
	ModeExplore OperatingMode = "explore"
)

// ParseOperatingMode maps a client-supplied string onto an OperatingMode, reporting whether it is
// one of the three.
//
// This is the only place an unvalidated mode enters the program. Four places used to re-implement
// the valid set with their own policy for a bad value; getting it wrong is worse than rejecting the
// request, because the tool-set switch falls through to act and a task the user believes is
// read-only gets write tools.
func ParseOperatingMode(s string) (OperatingMode, bool) {
	switch OperatingMode(s) {
	case ModeAct, ModePlan, ModeExplore:
		return OperatingMode(s), true
	default:
		return "", false
	}
}
