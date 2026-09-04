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
	ModePlan OperatingMode = "plan"
	// ModeExplore offers read-only tools and a read-only mount, for a turn that reads code.
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

// IsReadOnly reports whether a mode mounts the worktree read-only. The two read-only modes
// differ in the prompt and the tool set they are offered, not in what they may write, so every
// caller that only needs to know "may this turn write" asks this instead of naming a mode.
func (m OperatingMode) IsReadOnly() bool {
	return m == ModePlan || m == ModeExplore
}
