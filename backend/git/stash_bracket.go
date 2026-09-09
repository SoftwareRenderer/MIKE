package git

import (
	"errors"
	"fmt"
)

// ErrKeepStash is returned by a WithStash function to mean "the work ran, and the entry stays on
// the stack for whoever restores it next". WithStash passes it back unchanged, so the caller reads
// it with errors.Is.
var ErrKeepStash = errors.New("keep the stash entry")

// WithStash runs fn with dir's uncommitted work parked in a stash entry labeled label, then puts
// that work back. It is the only place the push/run/restore bracket is written.
//
// The rule it exists to hold: a bracketed section starts by forcing a checkout, and
// `git checkout -f` on the branch you are already on silently reverts tracked modifications to
// HEAD — exit 0, no warning, empty status. When the push fails there is nothing left protecting
// that work, so WithStash returns the push error and does not call fn. ErrNothingToStash is the
// one tolerated failure: git found nothing to take, so there is nothing to lose, and fn runs with
// no restore pending.
//
// The work goes back after fn whatever fn returned, a failed run included: the entry is the only
// copy of it, and PopStashByHash leaves an entry it could not apply, so an apply that conflicts
// still leaves the work on the stack rather than going with the conflict markers. An entry that
// has vanished by restore time is an error naming its hash — silently continuing is how lost work
// stays lost. Nothing here ever drops an entry.
//
// fn returns ErrKeepStash to leave the entry for code that runs later and finds it by label: the
// sync-merge bracket leaves it when the merge conflicts, so the turn that resolves the conflict
// restores it, while the auto-merge bracket always restores.
//
// The project stash lock is taken around the push and around the restore, and held across neither,
// so fn may take the merge lock — merge before stash, never after.
//
// Two stash uses deliberately stay outside it: server/handler_thread.go's stashWorktreeState hands
// the hash back for a later thread switch to restore, which a bracket scoped to one call cannot
// express, and exec/loop_agent.go's restore-by-label pushes nothing at all.
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
