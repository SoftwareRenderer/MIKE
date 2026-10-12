package stream

import (
	"strings"

	"mike/backend/git"
	"mike/backend/types"
)

// DiffStats holds computed diff statistics for a task.
type DiffStats struct {
	VisibleTracked   int
	VisibleUntracked int
	UntrackedLines   int
	TrackedAdd       int
	TrackedDel       int
	AlreadyMerged    bool
}

// ComputeDiffStats computes diff statistics from a task diff result, excluding internal files.
//
// AlreadyMerged defaults to false; the caller sets it after verifying with worktreeMgr.IsMergedInto().
func ComputeDiffStats(diffResult *git.TaskDiff) *DiffStats {
	var untrackedLines, trackedAdd, trackedDel, visibleTracked, visibleUntracked int
	for path, f := range diffResult.Tracked {
		if git.IsInternalFile(path) {
			continue
		}
		visibleTracked++
		trackedAdd += f.Added
		trackedDel += f.Removed
	}
	for _, u := range diffResult.Untracked {
		if git.IsInternalFile(u.Path) {
			continue
		}
		visibleUntracked++
		if u.OrigAdded > 0 {
			untrackedLines += u.OrigAdded
		} else {
			untrackedLines += len(u.Lines)
		}
	}

	return &DiffStats{
		VisibleTracked:   visibleTracked,
		VisibleUntracked: visibleUntracked,
		UntrackedLines:   untrackedLines,
		TrackedAdd:       trackedAdd,
		TrackedDel:       trackedDel,
	}
}

// CheckAlreadyMerged returns the target branch for merge verification. If empty, the
// check is skipped. The caller verifies using worktreeMgr.IsMergedInto().
func CheckAlreadyMerged(task types.Task) (targetBranch string, shouldCheck bool) {
	targetBranch = task.BaseRef
	if strings.HasPrefix(targetBranch, "fatal:") {
		return "", false
	}
	if targetBranch == "" || task.Branch == "" {
		return "", false
	}
	return targetBranch, true
}
