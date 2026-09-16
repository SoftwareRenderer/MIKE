package stream

import (
	"encoding/json"
	"errors"

	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/types"
	"mike/pkg/logging"
)

// DoneTaskDiff is a done task's materialized diff. Its canonical diff is BaseRef..HeadRef — two pinned
// refs — so once computed it never changes until the task is re-run. It backs both the task_diff_stats
// frame and GET /tasks/{id}/diff instead of a `git diff` per connection.
type DoneTaskDiff struct {
	StatsJSON  string // JSON object of task_diff_stats frame fields (no type/task_id/already_merged)
	DiffJSON   string // exact JSON body of the REST task-diff response
	HasChanges bool   // false when the canonical diff is empty (nothing to broadcast/serve)
}

// doneDiffResponse mirrors the JSON shape of the REST GET /tasks/{id}/diff
// response for a commit-to-commit (headRef) diff: TaskDiff fields with
// json tags, untracked file lines stripped to line counts.
type doneDiffResponse struct {
	Diff string `json:"diff"`
	// Never omitempty: in a stored body the key's presence is what marks it as carrying the parsed
	// view (empty diffs are never persisted).
	ParsedFiles       []git.TrackedDiffFile          `json:"parsed_files"`
	Untracked         []git.UntrackedFile            `json:"untracked"`
	ShortStat         string                         `json:"short_stat"`
	TruncatedFiles    []string                       `json:"truncated_files,omitempty"`
	OriginalFileStats map[string]git.FileChangeStats `json:"original_file_stats,omitempty"`
}

// MaterializeDoneTaskDiff returns the cached diff snapshot for a done task,
// computing and persisting it on first access (or when the pinned refs have
// changed). It returns (nil, nil) when the task has no canonical diff (no
// branch or empty diff) — the same condition under which the legacy code
// sends no stats frame. Errors are returned when the diff cannot be computed
// at all (e.g. repo missing); callers should fall back to the legacy path.
func MaterializeDoneTaskDiff(st *store.Store, wm *git.WorktreeManager, projectID string, task *types.Task) (*DoneTaskDiff, error) {
	if task.Branch == "" {
		return nil, nil
	}
	headRef := task.BranchTip
	if headRef == "" {
		headRef = task.Branch
	}
	baseRef, err := resolveDoneTaskBaseRef(st, wm, projectID, task)
	if err != nil {
		return nil, err
	}
	// Only a pinned BranchTip SHA is immutable. Legacy done tasks without one
	// diff against a branch name that can still advance — those compute live
	// and never persist (same behavior as before snapshots existed).
	cacheable := task.BranchTip != ""

	// Cache hit: the snapshot's pinned refs match the task's current refs.
	// (Empty diffs are never persisted, so a stored snapshot always has changes.)
	if cacheable {
		if snap, err := st.GetTaskDiffSnapshot(task.ID); err == nil && snap != nil &&
			snap.BaseCommit == baseRef && snap.BranchTip == headRef {
			return &DoneTaskDiff{
				StatsJSON:  snap.StatsJSON,
				DiffJSON:   snap.DiffJSON,
				HasChanges: true,
			}, nil
		}
	}

	diffResult, err := computeDoneTaskDiff(wm, st, projectID, task, baseRef, headRef)
	if err != nil {
		return nil, err
	}
	if diffResult.Diff == "" && len(diffResult.Untracked) == 0 && diffResult.CommittedDiff == "" {
		// Nothing to cache — mirrors the legacy "no frame for empty diff" behavior.
		return &DoneTaskDiff{HasChanges: false}, nil
	}

	hash := ComputeDiffHash(diffResult.Diff, diffResult.CommittedDiff, diffResult.Untracked, diffResult.StagedHash)
	if diffResult.CommittedDiff == "" && diffResult.Diff != "" {
		// Repo-fallback path has no worktree HEAD overlay; the whole canonical
		// diff is committed work, so use it for both diff and committedDiff.
		hash = ComputeDiffHash(diffResult.Diff, diffResult.Diff, nil, "")
	}
	stats := ComputeDiffStats(diffResult)
	filesN := stats.VisibleTracked + stats.VisibleUntracked

	statsObj := map[string]any{
		"hash":                  hash,
		"short_stat":            diffResult.ShortStat,
		"files_n":               filesN,
		"untracked_n":           stats.VisibleUntracked,
		"untracked_lines":       stats.UntrackedLines,
		"tracked_add":           stats.TrackedAdd,
		"tracked_del":           stats.TrackedDel,
		"file_hashes":           filterInternalPathsFromHashes(diffResult.GetPerFileHashes()),
		"staged_paths":          filterInternalPathsFromList(diffResult.StagedPaths),
		"has_committed_changes": diffResult.CommittedDiff != "" || diffResult.Diff != "",
	}
	statsJSON, err := json.Marshal(statsObj)
	if err != nil {
		return nil, err
	}
	diffJSON, err := json.Marshal(doneDiffResponseFromTaskDiff(diffResult))
	if err != nil {
		return nil, err
	}

	// A snapshot write failure is not fatal — serve the freshly computed data
	// and let the next read retry persistence.
	if cacheable {
		persistSnapshot(st, task, baseRef, headRef, statsJSON, diffJSON)
	}

	return &DoneTaskDiff{
		StatsJSON:  string(statsJSON),
		DiffJSON:   string(diffJSON),
		HasChanges: true,
	}, nil
}

// persistSnapshot stores a freshly computed done-task snapshot.
func persistSnapshot(st *store.Store, task *types.Task, baseRef, headRef string, statsJSON, diffJSON []byte) {
	if err := st.UpsertTaskDiffSnapshot(&store.TaskDiffSnapshot{
		TaskID:     task.ID,
		ProjectID:  task.ProjectID,
		BaseCommit: baseRef,
		BranchTip:  headRef,
		StatsJSON:  string(statsJSON),
		DiffJSON:   string(diffJSON),
	}); err != nil {
		logging.For(logging.Worktree).With(logging.TaskID, task.ID).Warnf("persist failed: %v", err)
	}
}

// resolveDoneTaskBaseRef returns the base ref for a done task's canonical
// diff: the pinned BaseCommit when captured (authoritative — required for a
// correct diff after fast-forward merges, deliberately NOT re-resolved for
// usability), otherwise the same resolved baseline the live paths use.
func resolveDoneTaskBaseRef(st *store.Store, wm *git.WorktreeManager, projectID string, task *types.Task) (string, error) {
	if task.BaseCommit != "" {
		return task.BaseCommit, nil
	}
	return resolveTaskDiffBaseline(st, wm, projectID, task)
}

// computeDoneTaskDiff computes the canonical commit-to-commit diff, using the
// task worktree when it exists and falling back to the project repo after
// worktree deletion.
func computeDoneTaskDiff(wm *git.WorktreeManager, st *store.Store, projectID string, task *types.Task, baseRef, headRef string) (*git.TaskDiff, error) {
	diffResult, err := wm.GetTaskDiff(task.ID, baseRef, headRef)
	if err == nil {
		return diffResult, nil
	}
	if !errors.Is(err, git.ErrWorktreeNotFound) {
		return nil, err
	}
	project, perr := st.GetProject(projectID)
	if perr != nil || project == nil || project.Path == "" {
		return nil, err
	}
	return wm.TaskDiffFromRepo(project.Path, baseRef, headRef)
}

// doneDiffResponseFromTaskDiff builds the REST response body for a
// commit-to-commit diff: untracked file lines are stripped to line counts
// (same as stripUntrackedLines in the api package).
func doneDiffResponseFromTaskDiff(d *git.TaskDiff) doneDiffResponse {
	untracked := make([]git.UntrackedFile, len(d.Untracked))
	for i, u := range d.Untracked {
		entry := u
		if len(u.Lines) > 0 {
			entry.LineCount = len(u.Lines)
			entry.Lines = nil
		}
		untracked[i] = entry
	}
	return doneDiffResponse{
		Diff:              d.Diff,
		ParsedFiles:       git.SortedParsedFiles(d.Tracked),
		Untracked:         untracked,
		ShortStat:         d.ShortStat,
		TruncatedFiles:    d.TruncatedFiles,
		OriginalFileStats: d.OriginalFileStats,
	}
}
