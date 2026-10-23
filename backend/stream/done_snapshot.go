package stream

import (
	"encoding/json"
	"errors"

	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/types"
	"mike/pkg/logging"
)

// DoneTaskDiff is a done task's materialized diff. Its canonical diff (BaseRef..HeadRef)
// is immutable until the task is re-run. It backs the task_diff_stats frame and
// GET /tasks/{id}/diff to avoid running git diff per connection.
type DoneTaskDiff struct {
	StatsJSON  string // JSON object of task_diff_stats frame fields (no type/task_id/already_merged)
	DiffJSON   string // exact JSON body of the REST task-diff response
	HasChanges bool   // false when the canonical diff is empty (nothing to broadcast/serve)
}

// doneDiffResponse matches the JSON shape of the REST GET /tasks/{id}/diff
// response for a commit-to-commit diff: TaskDiff fields with json tags,
// and untracked file lines stripped to line counts.
type doneDiffResponse struct {
	Diff string `json:"diff"`
	// Never omitempty: key presence marks the response as carrying the parsed view.
	ParsedFiles       []git.TrackedDiffFile          `json:"parsed_files"`
	Untracked         []git.UntrackedFile            `json:"untracked"`
	ShortStat         string                         `json:"short_stat"`
	TruncatedFiles    []string                       `json:"truncated_files,omitempty"`
	OriginalFileStats map[string]git.FileChangeStats `json:"original_file_stats,omitempty"`
}

// MaterializeDoneTaskDiff returns the cached diff snapshot for a done task,
// computing and persisting it if missing or outdated. Returns (nil, nil) if
// the task has no canonical diff (no branch or empty diff). Errors are returned
// if computation fails; callers should fall back to the legacy path.
func MaterializeDoneTaskDiff(st *store.Store, wm *git.WorktreeManager, projectID string, task *types.Task) (*DoneTaskDiff, error) {
	if task.Branch == "" {
		return nil, nil
	}
	headRef := task.BranchTip
	if headRef == "" {
		headRef = task.Branch
	}
	baseRef := resolveDoneTaskBaseRef(wm, task)
	// Only a pinned BranchTip SHA is immutable. Tasks without one diff against
	// a branch name that can advance; those compute live and never persist.
	cacheable := task.BranchTip != ""

	// Cache hit: pinned refs match current refs.
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
	if !diffResult.HasChanges() {
		// Nothing to cache; mirrors legacy "no frame for empty diff" behavior.
		return &DoneTaskDiff{HasChanges: false}, nil
	}

	// A stored snapshot holds the shared keys only. It belongs to no connection, and already_merged is
	// recomputed when the snapshot is replayed.
	statsJSON, err := json.Marshal(diffStatsKeys(diffStatsOf(diffResult)))
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
// diff: the pinned BaseCommit if captured (prevents diff shifts after fast-forwards),
// otherwise the same resolved baseline the live paths use.
func resolveDoneTaskBaseRef(wm *git.WorktreeManager, task *types.Task) string {
	if task.BaseCommit != "" {
		return task.BaseCommit
	}
	return wm.TaskDiffBaseline(task)
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
	return wm.TaskDiffFromRepo(project.Path, baseRef, headRef, false)
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
