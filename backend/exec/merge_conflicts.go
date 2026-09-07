package exec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"kanban/backend/git"
	"kanban/backend/types"
	"kanban/pkg/logging"
)

// ErrNotAMergeConflict means git merge failed with no merge in progress (no MERGE_HEAD): a
// hook error, a missing branch, a lock — not a conflict. Callers detect it with errors.Is and show
// the underlying git error instead.
//
// It carries text because an error that prints as nothing is a bug waiting for the one path that
// surfaces it alone; being wrapped with %w is not the only way an error reaches a log.
var ErrNotAMergeConflict = errors.New("not a merge conflict")

// isMergeInProgress checks whether a git merge is currently in progress in the given directory.
// Uses `git rev-parse --git-dir` to correctly resolve the git directory in both
// regular repos and worktrees (where .git is a file, not a directory).
func isMergeInProgress(dir string) bool {
	cmd := git.Command("", "rev-parse", "--git-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	gitDir := strings.TrimSpace(string(out))
	// git-dir might be relative (e.g., "../.git/worktrees/mytree")
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	_, err = os.Stat(filepath.Join(gitDir, "MERGE_HEAD"))
	return err == nil
}

// GetConflictedFiles returns a deduplicated list of files with merge conflicts.
// Uses git status --porcelain (catches UU, AD, DU, AA, DD, UD, UA) and falls back
// to git diff --diff-filter=U (catches binary conflicts and renames).
func GetConflictedFiles(dir string) []string {
	seen := make(map[string]struct{})
	var files []string

	// Primary: git status --porcelain
	cmd := git.Command("", "status", "--porcelain")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		for line := range strings.SplitSeq(string(out), "\n") {
			line = strings.TrimSpace(line)
			if len(line) >= 4 && strings.ContainsRune(line[:2], 'U') {
				if f := strings.TrimSpace(line[3:]); f != "" {
					files = addUnique(files, seen, f)
				}
			}
		}
	}

	// Fallback: git diff --diff-filter=U (binary conflicts, renames)
	if len(files) == 0 {
		cmd = git.Command("", "diff", "--name-only", "--diff-filter=U")
		cmd.Dir = dir
		if out, err := cmd.Output(); err == nil {
			for line := range strings.SplitSeq(string(out), "\n") {
				if f := strings.TrimSpace(line); f != "" {
					files = addUnique(files, seen, f)
				}
			}
		}
	}

	return files
}

// addUnique appends f to files unless seen already holds it, recording it in
// seen. Dedup only — callers must not care whether f was new.
func addUnique(files []string, seen map[string]struct{}, f string) []string {
	if _, ok := seen[f]; ok {
		return files
	}
	seen[f] = struct{}{}
	return append(files, f)
}

// GetAllConflictsResolved reports whether every conflicted file is resolved, by asking
// `git diff --check MERGE_HEAD -- <files>` for conflict markers rather than scanning file
// contents. It needs all of them clean: git refuses the merge commit until every conflict is
// staged, so partial resolution does not count.
func GetAllConflictsResolved(dir string) bool {
	conflictedFiles := GetConflictedFiles(dir)
	if len(conflictedFiles) == 0 {
		return true
	}

	// Check MERGE_HEAD exists (we should be in a merge state).
	// If not, fall back to file-content scan.
	mergeHeadCmd := git.Command("", "rev-parse", "--verify", "--quiet", "MERGE_HEAD")
	mergeHeadCmd.Dir = dir
	if mergeHeadCmd.Run() == nil {
		// Use git diff --check to detect conflict markers.
		// Compares working tree against MERGE_HEAD; any markers in the
		// diff are reported as warnings and cause a non-zero exit code.
		args := append([]string{"diff", "--check", "MERGE_HEAD", "--"}, conflictedFiles...)
		cmd := git.Command("", args...)
		cmd.Dir = dir
		return cmd.Run() == nil
	}

	// Fallback: scan file contents for markers (should not be reached in
	// normal operation — MERGE_HEAD always exists during an active merge).
	for _, f := range conflictedFiles {
		if hasConflictMarkers(filepath.Join(dir, f)) {
			return false
		}
	}
	return true
}

// hasConflictMarkers checks if a file contains git merge conflict markers.
// Kept as a fallback when MERGE_HEAD is unavailable.
func hasConflictMarkers(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(data)
	return strings.Contains(content, "<<<<<<<") ||
		strings.Contains(content, "=======") ||
		strings.Contains(content, ">>>>>>>")
}

// GetResolvedConflictedFiles returns the conflicted files to stage for the
// resolution commit. If all conflicts are resolved, returns all conflicted
// files (git requires ALL to be staged for the merge commit). If any file
// still has conflict markers, returns nil — the caller should skip the commit
// and re-prompt the LLM.
func GetResolvedConflictedFiles(dir string) []string {
	if GetAllConflictsResolved(dir) {
		return GetConflictedFiles(dir)
	}
	return nil
}

// getMergeStatusOutput returns the raw git status --porcelain output for diagnostics.
func getMergeStatusOutput(dir string) string {
	cmd := git.Command("", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("(git status failed: %v)", err)
	}
	result := strings.TrimSpace(string(out))
	if result == "" {
		return "(working tree clean)"
	}
	return result
}

// tryResolveMergeConflicts attempts LLM-based conflict resolution after a failed merge.
// Uses a fallback chain: primary scan → fallback scan → auto-continue → diagnostic error.
// On success, stages files, commits, and logs a merge_resolution message.
// On failure, aborts the merge and returns the error.
func (te *TaskExecutor) tryResolveMergeConflicts(dir, taskID, targetBranch, taskBranch string, task *types.Task) error {
	// Confirm we're actually in a merge state
	if !isMergeInProgress(dir) {
		// Not a merge conflict — likely a hook failure, lock issue, or other git error
		return fmt.Errorf("merge failed but not in merge state (no MERGE_HEAD): %w", ErrNotAMergeConflict)
	}

	conflictedFiles := GetConflictedFiles(dir)

	// No conflicted files found — try git merge --continue (git may have auto-resolved)
	if len(conflictedFiles) == 0 {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("no conflicted files found, trying merge --continue")
		continueErr := te.gitRun(dir, append(git.GitAuthorFlags(), "merge", "--continue")...)
		if continueErr == nil {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("merge auto-resolved via --continue")
			te.logMergeComplete(taskID, task.CurrentThreadID, taskBranch, targetBranch)
			return nil
		}
		// --continue also failed — include diagnostics in error
		statusOutput := getMergeStatusOutput(dir)
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("merge --continue failed: %v. Status: %s", continueErr, statusOutput)
		_ = te.gitRun(dir, "merge", "--abort")
		return fmt.Errorf("merge conflict but no conflicted files found (status: %s)", statusOutput)
	}

	logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("conflict in %d file(s): %s", len(conflictedFiles), strings.Join(conflictedFiles, ", "))

	// Log conflict detection as system message
	te.logSystemMessage(taskID, task.CurrentThreadID, "error",
		fmt.Sprintf("Merge conflict detected in: %s", strings.Join(conflictedFiles, ", ")))

	_ = te.gitRun(dir, "merge", "--abort")
	return fmt.Errorf("merge conflict in %d file(s): %s", len(conflictedFiles), strings.Join(conflictedFiles, ", "))
}

// logSystemMessage creates a system message in the task's conversation thread.
// severity is "info" for informational notices or "error" for problems;
// empty severity renders as the default warning style.
func (te *TaskExecutor) logSystemMessage(taskID, threadID, severity, content string) {
	if threadID == "" {
		return
	}
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID:        uuid.New().String(),
		TaskID:    taskID,
		ThreadID:  threadID,
		Role:      "system",
		Severity:  severity,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		logging.For(logging.Exec).With(logging.TaskID, taskID).Warnf("failed to log message: %v", err)
	}
}

// logMergeComplete logs a merge message for a clean (no-conflict) merge.
func (te *TaskExecutor) logMergeComplete(taskID, threadID, taskBranch, targetBranch string) {
	if threadID == "" {
		return
	}
	content := fmt.Sprintf("Branch %s merged into %s", taskBranch, targetBranch)
	te.persistAndBroadcastMergeMessage(taskID, threadID, content, "Merged")
}

// logMergeFailed logs a merge failure as a merge_resolution error message.
func (te *TaskExecutor) logMergeFailed(taskID, threadID, errorMsg string) {
	if threadID == "" {
		return
	}
	msgID := uuid.New().String()
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID:        msgID,
		TaskID:    taskID,
		ThreadID:  threadID,
		Role:      "merge_resolution",
		Content:   errorMsg,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("failed to log message: %v", err)
		return
	}
	task, _ := te.store.GetTaskByID(taskID)
	if task != nil && te.appStreamMgr != nil {
		te.appStreamMgr.SendTaskEvent(task.ProjectID, taskID, types.StreamEvent{Type: types.WSTypeMergeResolution, Data: map[string]any{
			"id":         msgID,
			"content":    errorMsg,
			"label":      "Merge failed",
			"is_error":   true,
			"created_at": time.Now().UTC().Format(time.RFC3339),
		}})
	}
}

// persistAndBroadcastMergeMessage creates a merge_resolution message and broadcasts it.
func (te *TaskExecutor) persistAndBroadcastMergeMessage(taskID, threadID, content, label string) {
	msgID := uuid.New().String()
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID:        msgID,
		TaskID:    taskID,
		ThreadID:  threadID,
		Role:      "merge_resolution",
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("failed to log message: %v", err)
		return
	}
	// Broadcast to task stream clients for real-time display
	task, _ := te.store.GetTaskByID(taskID)
	if task != nil && te.appStreamMgr != nil {
		te.appStreamMgr.SendTaskEvent(task.ProjectID, taskID, types.StreamEvent{Type: types.WSTypeMergeResolution, Data: map[string]any{
			"id":         msgID,
			"content":    content,
			"label":      label,
			"created_at": time.Now().UTC().Format(time.RFC3339),
		}})
	}
}

// stageResolvedConflicts stages every file git reports as resolved (conflict
// markers gone) back into the index. Returns false, logging the reason, when
// staging fails. Nothing recorded as resolved stages nothing and still counts as
// success — it is the commit below that records the resolution.
func (te *TaskExecutor) stageResolvedConflicts(taskID, worktreePath string) bool {
	conflictedFiles := GetResolvedConflictedFiles(worktreePath)
	if len(conflictedFiles) == 0 {
		return true
	}
	addArgs := append([]string{"add"}, conflictedFiles...)
	if err := te.gitRun(worktreePath, addArgs...); err != nil {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("git add failed: %v", err)
		return false
	}
	return true
}

// commitMergeConflictResolution records a conflict resolution as a commit. Both
// resolution paths (LLM loop and agent loop) commit here so the resolution
// message and author stay identical.
func (te *TaskExecutor) commitMergeConflictResolution(taskID, worktreePath string) bool {
	if err := te.gitRun(worktreePath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...); err != nil {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("git commit failed: %v", err)
		return false
	}
	return true
}

// conflictResolution is the outcome of commitConflictResolution: everything a caller needs to decide
// what the task becomes once the conflict is gone.
type conflictResolution struct {
	// task is the task as the store holds it now. Never nil.
	task *types.Task
	// committed records that the resolution became a commit. Nothing merges without it.
	committed bool
	// mergeErr is the error from the merge into the target branch, nil when it ran cleanly or was
	// not wanted (see mergeWanted).
	mergeErr error
	// commitDone says a "Commit and move to done" click produced this conflict, so it merges even
	// when auto_merge says no.
	commitDone bool
}

// mergeWanted reports whether anything asked for this work to land in the target branch: the task's
// auto_merge setting, or the user's click.
func (r conflictResolution) mergeWanted() bool {
	return r.commitDone || (r.task != nil && r.task.AutoMerge)
}

// finishesTask reports whether the resolution delivered what mergeWanted asked for: committed,
// merged cleanly, nothing re-conflicted. Otherwise the task goes back to the user.
func (r conflictResolution) finishesTask() bool {
	return r.committed && r.mergeWanted() && r.mergeErr == nil && r.task != nil && !r.task.HasMergeConflict
}

// commitConflictResolution stages resolved conflicted files, commits the
// resolution, drops the stale stash, clears the merge conflict flag, and
// attempts the final merge.
//
// commitConflictResolution runs the git operations every merge-conflict resolution path
// shares. Callers handle the divergent part afterwards: inject a prompt, mark done, or fail.
func (te *TaskExecutor) commitConflictResolution(projectID, taskID, worktreePath string, task *types.Task) conflictResolution {
	// No worktree mutex here: MergeBranch→syncTargetIntoTask takes it and would deadlock.
	// Safe because the LLM is done and the loop is single-threaded.
	commitDone := te.CommitAndDonePending(taskID)
	commitOK := true

	if isMergeInProgress(worktreePath) {
		// Sync-merge conflict: worktree has MERGE_HEAD with conflict markers.
		commitOK = te.stageResolvedConflicts(taskID, worktreePath)
	} else {
		// Final-merge conflict: worktree has no MERGE_HEAD.
		checkCmd := git.Command("", "diff", "--quiet")
		checkCmd.Dir = worktreePath
		if checkCmd.Run() == nil {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("no changes to commit for conflict resolution")
			commitOK = false
		}
		if commitOK {
			// `add -u` stages only tracked changes — never sweeps untracked files.
			if err := te.gitRun(worktreePath, "add", "-u"); err != nil {
				logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("git add -u failed: %v", err)
				commitOK = false
			}
		}
	}

	if commitOK {
		commitOK = te.commitMergeConflictResolution(taskID, worktreePath)
	}

	if commitOK {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("conflict resolution committed")
		// Drop this task's stale stash (its pre-sync edits are committed by now). Popping would re-apply them
		// as unstaged changes, and a bare drop would take stash@{0} - see DropStashForTask.
		//
		// Released explicitly rather than deferred: MergeBranch runs later in this function and takes the merge
		// lock, which must come before the stash lock, never after.
		unlockStash := te.worktreeMgr.LockProjectStash(projectID)
		if _, err := te.worktreeMgr.DropStashForTask(worktreePath, git.StashMessageForSyncMerge(taskID)); err != nil {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("discarding the sync-merge stash: %v", err)
		}
		unlockStash()
	}

	_ = te.store.SetTaskMergeConflict(taskID, projectID, false)

	var mergeErr error
	// A click merges whatever auto_merge says; without one, auto_merge alone decides, as before.
	if commitOK && (task.AutoMerge || commitDone) {
		mergeErr = te.MergeBranch(projectID, taskID, task)
		if mergeErr != nil {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("final merge failed: %v", mergeErr)
		}
	} else if commitOK {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("merge skipped (auto_merge=false, no commit-and-done click)")
	}

	updatedTask, err := te.store.GetTaskByID(taskID)
	if err != nil || updatedTask == nil {
		// Callers overwrite their own task with this, so a store hiccup must not hand them a nil.
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("refresh after conflict resolution failed: %v", err)
		updatedTask = task
	}

	res := conflictResolution{task: updatedTask, committed: commitOK, mergeErr: mergeErr, commitDone: commitDone}
	if !updatedTask.HasMergeConflict {
		// The conflict is over, so the click is spent, merge failure included; a still conflicted
		// task keeps it for the turn that finishes it.
		res.commitDone = te.takeCommitAndDone(taskID, mergeErr == nil)
	}
	return res
}
