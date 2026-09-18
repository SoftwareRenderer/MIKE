package git

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrWorktreeNotFound is returned by every operation that needs a task's worktree and
// cannot find it - including the row lookup that finds no task at all, because for these
// callers both ask for the same recovery. Classify it with errors.Is, never by text.
var ErrWorktreeNotFound = errors.New("worktree not found")

// ErrNothingToStash is returned by PushStash when the push created no entry: `git stash push` exits
// successfully saying "No local changes to save" whenever the worktree changed between the caller's check
// and the push. Callers must not read it as a saved state — but there is nothing lost either, because git
// found nothing to take. Classify it with errors.Is.
var ErrNothingToStash = errors.New("stash push created no entry")

// RestoreConflictError is a failed stash apply that left unmerged paths: the parked work is in the
// working tree with conflict markers *and* still on the stack. It reports the state of the working copy,
// not of the work the bracket parked it for, and names the files git could not put back and the entry
// that still holds them, which a caller has to report because the entry is deliberately not dropped.
// Classify it with errors.As.
type RestoreConflictError struct {
	Hash  string
	Paths []string
	// GitMessage is git's own reason for refusing the apply.
	GitMessage string
}

func (e *RestoreConflictError) Error() string {
	return fmt.Sprintf("stash apply %s left %d unmerged file(s) (%s); the entry is still on the stack: %s",
		shortHash(e.Hash), len(e.Paths), strings.Join(e.Paths, ", "), e.GitMessage)
}

// gitMessage renders why git failed: git's own words on one line, the exit status only as a fallback. It
// reaches the user through the error it is wrapped into, so "exit status 1" alone is never good enough.
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
