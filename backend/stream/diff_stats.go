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

// diffStatsOf counts one computed diff into what a producer publishes. It leaves out already_merged,
// because only the caller holds the target branch to check the task against.
func diffStatsOf(diffResult *git.TaskDiff) DiffStatsEvent {
	stats := ComputeDiffStats(diffResult)
	return DiffStatsEvent{
		DiffHash:            DiffHash(diffResult),
		ShortStat:           diffResult.ShortStat,
		FilesN:              stats.VisibleTracked + stats.VisibleUntracked,
		UntrackedN:          stats.VisibleUntracked,
		UntrackedLines:      stats.UntrackedLines,
		TrackedAdd:          stats.TrackedAdd,
		TrackedDel:          stats.TrackedDel,
		ChangedHashes:       diffResult.GetPerFileHashes(),
		StagedPaths:         diffResult.StagedPaths,
		HasCommittedChanges: diffResult.HasCommittedWork(),
	}
}

// diffStatsKeys are the task_diff_stats keys every producer shares. Each producer adds what only it
// knows: a watcher's removed_files delta, a frame's task_id, and already_merged, which a snapshot omits.
func diffStatsKeys(ev DiffStatsEvent) map[string]any {
	return map[string]any{
		"hash":                  ev.DiffHash,
		"short_stat":            ev.ShortStat,
		"files_n":               ev.FilesN,
		"untracked_n":           ev.UntrackedN,
		"untracked_lines":       ev.UntrackedLines,
		"tracked_add":           ev.TrackedAdd,
		"tracked_del":           ev.TrackedDel,
		"file_hashes":           filterInternalPathsFromHashes(ev.ChangedHashes),
		"staged_paths":          filterInternalPathsFromList(ev.StagedPaths),
		"has_committed_changes": ev.HasCommittedChanges,
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
