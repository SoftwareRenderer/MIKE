package stream

import (
	"strings"

	"mike/backend/git"
	"mike/backend/types"
)

// diffStats counts what a task's reader sees, internal files excluded.
type diffStats struct {
	VisibleTracked   int
	VisibleUntracked int
	UntrackedLines   int
	TrackedAdd       int
	TrackedDel       int
}

func computeDiffStats(diffResult *git.TaskDiff) *diffStats {
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

	return &diffStats{
		VisibleTracked:   visibleTracked,
		VisibleUntracked: visibleUntracked,
		UntrackedLines:   untrackedLines,
		TrackedAdd:       trackedAdd,
		TrackedDel:       trackedDel,
	}
}

// BuildDiffStatsEvent assembles the shared task_diff_stats keys from one computed diff. A producer
// adds only what it alone holds: a watcher's removed_files delta, and already_merged, which needs the
// target branch.
func BuildDiffStatsEvent(projectID, taskID string, diffResult *git.TaskDiff) DiffStatsEvent {
	stats := computeDiffStats(diffResult)
	return DiffStatsEvent{
		ProjectID:           projectID,
		TaskID:              taskID,
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

// diffStatsKeys is the frame map of the keys every producer shares.
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
