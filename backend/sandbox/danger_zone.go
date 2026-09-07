package sandbox

import (
	"fmt"
	"os"
)

// DangerZoneEnv is the operator's opt-in to running anything on the host instead of in a
// gVisor container: an unsandboxed project, its terminal, and the bash tool call a task
// makes inside it.
const DangerZoneEnv = "DANGER_ZONE"

// HostExecutionAllowed reports whether the operator opted in. Only the exact value "1"
// counts: a truthy-looking typo is not consent.
func HostExecutionAllowed() bool { return os.Getenv(DangerZoneEnv) == "1" }

// ErrHostExecution is the refusal both host paths answer with, naming the variable because
// that is the only actionable part of it. Checked where the shell is handed out and not only
// at create time: a row with sandboxing off can predate that rule, and a row is not consent.
func ErrHostExecution(what string) error {
	return fmt.Errorf("%s is not allowed: this project runs with sandboxing disabled and %s=1 is not set in the server environment",
		what, DangerZoneEnv)
}
