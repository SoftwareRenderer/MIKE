package git

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrWorktreeNotFound is returned when a task's worktree is missing.
// Use errors.Is to classify it.
var ErrWorktreeNotFound = errors.New("worktree not found")

// ErrNothingToStash is returned by PushStash when no changes were found to stash.
// Use errors.Is to classify it.
var ErrNothingToStash = errors.New("stash push created no entry")

// RestoreConflictError is a failed stash apply that left unmerged paths.
// The work remains on the stack and in the working tree with conflict markers.
// Use errors.As to classify it.
type RestoreConflictError struct {
	Hash  string
	Paths []string
	// GitMessage is the reason for the failed apply.
	GitMessage string
}

func (e *RestoreConflictError) Error() string {
	return fmt.Sprintf("stash apply %s left %d unmerged file(s) (%s); the entry is still on the stack: %s",
		shortHash(e.Hash), len(e.Paths), strings.Join(e.Paths, ", "), e.GitMessage)
}

// gitMessage returns git's failure message on one line, falling back to the exit status.
func gitMessage(out string, err error) string {
	msg := strings.Join(strings.Fields(strings.TrimSpace(out)), " ")
	if msg == "" {
		if err != nil {
			return err.Error()
		}
		return "unknown error"
	}
	return msg
}

// GitExitCode returns the exit code of the git subprocess. An exit code of 128
// often indicates a repository-level failure (e.g. bad revision).
func GitExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}
