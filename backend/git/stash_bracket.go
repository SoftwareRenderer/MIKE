package git

import (
	"errors"
	"fmt"
)

// ErrKeepStash tells WithStash to leave the entry on the stack for whoever restores it next.
// WithStash passes it back unchanged, so the caller reads it with errors.Is.
var ErrKeepStash = errors.New("keep the stash entry")

// WithStash runs fn with dir's uncommitted work parked in a stash entry, then puts it back. The
// bracket opens with `git checkout -f`, which silently reverts tracked modifications to HEAD, so when
// the push did not take the work WithStash refuses to run fn at all - running it would destroy that
// work. It is the only place the push/run/restore sequence is written.
func (wm *WorktreeManager) WithStash(projectID, dir, label string, fn func() error) error {
	release := wm.LockProjectStash(projectID)
	hash, pushErr := wm.PushStash(dir, label)
	release()

	if errors.Is(pushErr, ErrNothingToStash) {
		return fn()
	}
	if pushErr != nil {
		return pushErr
	}

	runErr := fn()
	if errors.Is(runErr, ErrKeepStash) {
		return runErr
	}

	release = wm.LockProjectStash(projectID)
	found, restoreErr := wm.PopStashByHash(dir, hash)
	release()
	if restoreErr != nil {
		restoreErr = fmt.Errorf("restore the stashed work: %w", restoreErr)
	} else if !found {
		restoreErr = fmt.Errorf("the stash entry %s is no longer on the stack", shortHash(hash))
	}

	if runErr != nil && restoreErr != nil {
		return errors.Join(runErr, restoreErr)
	}
	if runErr != nil {
		return runErr
	}
	return restoreErr
}
