package sandbox

import "os"

// DangerZoneEnv is the operator's permission to turn sandboxing off: creating a project with
// sandboxing disabled, and switching an existing project to it. Both refusals live in
// backend/managers/project.go. It says nothing about where a running task executes — a project's
// sandbox_enabled column is that answer, and a project with sandboxing off is the operator's own
// decision, not re-checked where the shell or the tool call is handed out.
const DangerZoneEnv = "DANGER_ZONE"

// DangerZoneAllows reports whether the operator granted that permission. Only the exact value "1"
// counts: a truthy-looking typo is not the opt-in it was meant to be.
func DangerZoneAllows() bool { return os.Getenv(DangerZoneEnv) == "1" }
