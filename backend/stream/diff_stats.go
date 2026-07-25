package stream

import (
	"strings"

	"kanban/backend/git"
	"kanban/backend/types"
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

// ComputeDiffStats computes diff statistics from a task diff result, excluding
// internal files. This encapsulates the common logic shared between
// checkAndBroadcastDiff (exec package) and sendInitialProjectData (stream package).
//
// The AlreadyMerged field is left as false by default — the caller should set it
// after verifying via worktreeMgr.IsMergedInto(projectPath, task.Branch, targetBranch).
func ComputeDiffStats(diffResult *git.TaskDiff, task types.Task) *DiffStats {
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

// CheckAlreadyMerged returns the target branch to check against, or empty string
// if the merge check should be skipped. The caller should then invoke
// worktreeMgr.IsMergedInto(projectPath, task.Branch, targetBranch) when both
// values are non-empty.
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
