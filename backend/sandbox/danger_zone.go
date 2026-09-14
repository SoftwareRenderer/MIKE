package sandbox

// DangerZoneEnv is the operator's permission to turn sandboxing off: creating a project with
// sandboxing disabled, and switching an existing project to it. Both refusals live in
// backend/api/project.go. It says nothing about where a running task executes — a project's
// sandbox_enabled column is that answer, and a project with sandboxing off is the operator's own
// decision, not re-checked where the shell or the tool call is handed out.
const DangerZoneEnv = "DANGER_ZONE"

// DangerZone is the resolved answer: did the operator grant that permission. It is a value handed to
// the two places that decide, not a variable re-read where they decide, so a project create and a
// host terminal cannot disagree about a process that only started once. The resolution rule — only
// the exact value "1" counts, a truthy-looking typo is not the opt-in it was meant to be — is
// startup's.
type DangerZone bool

// Allows reports whether the grant was made.
func (d DangerZone) Allows() bool { return bool(d) }
