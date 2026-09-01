package git

import (
	"errors"
	"os/exec"
)

// ErrWorktreeNotFound is returned by every operation that needs a task's worktree and
// cannot find it - including the row lookup that finds no task at all, because for these
// callers both ask for the same recovery. Classify it with errors.Is, never by text.
var ErrWorktreeNotFound = errors.New("worktree not found")

// GitExitCode reports the exit code of the git subprocess that produced err, or -1.
// The code is the signal: 128 means git refused to run against this repository (bad
// revision, worktree removed under us) - a not-found shape - where an OS failure is not.
// Matching the rendered text instead only works if every layer happens to use %w.
func GitExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}
