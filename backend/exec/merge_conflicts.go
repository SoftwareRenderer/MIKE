package exec

import (
	"errors"
	"fmt"
	"log"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"kanban/backend/git"
	"kanban/backend/types"
)

// ErrNotAMergeConflict is returned when git merge failed but no merge is in
// progress (no MERGE_HEAD). This indicates a non-conflict failure such as a
// hook error, missing branch, or lock issue. Callers should handle this
// separately from conflict resolution failures.
// The sentinel message is empty — it's for errors.Is() detection only.
// Callers should surface the underlying git error to the user.
var ErrNotAMergeConflict = errors.New("")

// isMergeInProgress checks whether a git merge is currently in progress in the given directory.
// Uses `git rev-parse --git-dir` to correctly resolve the git directory in both
// regular repos and worktrees (where .git is a file, not a directory).
func isMergeInProgress(dir string) bool {
	cmd := osexec.Command("git", "rev-parse", "--git-dir")
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

// getConflictedFiles returns a deduplicated list of files with merge conflicts.
// Uses git status --porcelain (catches UU, AD, DU, AA, DD, UD, UA) and falls back
// to git diff --diff-filter=U (catches binary conflicts and renames).
// GetConflictedFiles returns a list of files with merge conflicts in the given directory.
func GetConflictedFiles(dir string) []string {
	seen := make(map[string]struct{})
	var files []string

	// Primary: git status --porcelain
	cmd := osexec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		for line := range strings.SplitSeq(string(out), "\n") {
			line = strings.TrimSpace(line)
			if len(line) >= 4 && strings.ContainsRune(line[:2], 'U') {
				f := strings.TrimSpace(line[3:])
				if f != "" && addIfNew(f, seen, &files) {
				}
			}
		}
	}

	// Fallback: git diff --diff-filter=U (binary conflicts, renames)
	if len(files) == 0 {
		cmd = osexec.Command("git", "diff", "--name-only", "--diff-filter=U")
		cmd.Dir = dir
		if out, err := cmd.Output(); err == nil {
			for line := range strings.SplitSeq(string(out), "\n") {
				f := strings.TrimSpace(line)
				if f != "" && addIfNew(f, seen, &files) {
				}
			}
		}
	}

	return files
}

// addIfNew adds f to files if not already present, tracked in seen.
func addIfNew(f string, seen map[string]struct{}, files *[]string) bool {
	if _, ok := seen[f]; ok {
		return false
	}
	seen[f] = struct{}{}
	*files = append(*files, f)
	return true
}

// getAllConflictsResolved checks whether ALL conflicted files have been
// resolved by running `git diff --check MERGE_HEAD -- <files>` and looking
// for conflict marker warnings. This delegates detection to Git instead of
// scanning file contents for marker strings.
//
// Returns true when every conflicted file is clean — git requires all
// conflicts to be staged before a merge commit can be made, so partial
// resolution is not sufficient.
func GetAllConflictsResolved(dir string) bool {
	conflictedFiles := GetConflictedFiles(dir)
	if len(conflictedFiles) == 0 {
		return true
	}

	// Check MERGE_HEAD exists (we should be in a merge state).
	// If not, fall back to file-content scan.
	mergeHeadCmd := osexec.Command("git", "rev-parse", "--verify", "--quiet", "MERGE_HEAD")
	mergeHeadCmd.Dir = dir
	if mergeHeadCmd.Run() == nil {
		// Use git diff --check to detect conflict markers.
		// Compares working tree against MERGE_HEAD; any markers in the
		// diff are reported as warnings and cause a non-zero exit code.
		args := append([]string{"diff", "--check", "MERGE_HEAD", "--"}, conflictedFiles...)
		cmd := osexec.Command("git", args...)
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
	cmd := osexec.Command("git", "status", "--porcelain")
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
func (te *TaskExecutor) tryResolveMergeConflicts(dir, projectID, taskID, targetBranch, taskBranch string, task *types.Task) error {
	// Confirm we're actually in a merge state
	if !isMergeInProgress(dir) {
		// Not a merge conflict — likely a hook failure, lock issue, or other git error
		return fmt.Errorf("merge failed but not in merge state (no MERGE_HEAD): %w", ErrNotAMergeConflict)
	}

	conflictedFiles := GetConflictedFiles(dir)

	// No conflicted files found — try git merge --continue (git may have auto-resolved)
	if len(conflictedFiles) == 0 {
		log.Printf("[merge] task=%s no conflicted files found, trying merge --continue", taskID)
		continueErr := runGitDir(dir, append(git.GitAuthorFlags(), "merge", "--continue")...)
		if continueErr == nil {
			log.Printf("[merge] task=%s merge auto-resolved via --continue", taskID)
			te.logMergeComplete(taskID, task.CurrentThreadID, taskBranch, targetBranch)
			return nil
		}
		// --continue also failed — include diagnostics in error
		statusOutput := getMergeStatusOutput(dir)
		log.Printf("[merge] task=%s merge --continue failed: %v. Status: %s", taskID, continueErr, statusOutput)
		_ = runGitDir(dir, "merge", "--abort")
		return fmt.Errorf("merge conflict but no conflicted files found (status: %s)", statusOutput)
	}

	log.Printf("[merge] task=%s conflict in %d file(s): %s",
		taskID, len(conflictedFiles), strings.Join(conflictedFiles, ", "))

	// Log conflict detection as system message
	te.logSystemMessage(taskID, task.CurrentThreadID, "error",
		fmt.Sprintf("Merge conflict detected in: %s", strings.Join(conflictedFiles, ", ")))

	_ = runGitDir(dir, "merge", "--abort")
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
		log.Printf("[system-msg] task=%s failed to log message: %v", taskID, err)
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
		log.Printf("[merge-msg] task=%s failed to log message: %v", taskID, err)
		return
	}
	task, _ := te.store.GetTaskByTaskID(taskID)
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
		log.Printf("[merge-msg] task=%s failed to log message: %v", taskID, err)
		return
	}
	// Broadcast to task stream clients for real-time display
	task, _ := te.store.GetTaskByTaskID(taskID)
	if task != nil && te.appStreamMgr != nil {
		te.appStreamMgr.SendTaskEvent(task.ProjectID, taskID, types.StreamEvent{Type: types.WSTypeMergeResolution, Data: map[string]any{
			"id":         msgID,
			"content":    content,
			"label":      label,
			"created_at": time.Now().UTC().Format(time.RFC3339),
		}})
	}
}

// runGitDir runs a git command in the given directory with author flags.
func runGitDir(dir string, args ...string) error {
	cmd := osexec.Command("git", args...)
	cmd.Dir = dir
	_, err := cmd.CombinedOutput()
	return err
}

// commitConflictResolution stages resolved conflicted files, commits the
// resolution, drops the stale stash, clears the merge conflict flag, and
// attempts the final merge. Returns (commitOK, refreshedTask).
//
// This encapsulates the git operations shared by all merge-conflict resolution
// paths in the execution loop. Callers handle the divergent post-resolution
// control flow (inject prompt vs. mark done vs. mark failed).
func (te *TaskExecutor) commitConflictResolution(projectID, taskID, worktreePath string, task *types.Task) (commitOK bool, updatedTask *types.Task) {
	// Don't acquire worktree mutex — MergeBranch→syncTargetIntoTask does. Would deadlock.
	// Safe: LLM is done, loop is single-threaded, syncTargetIntoTask locks below.
	commitOK = true

	if isMergeInProgress(worktreePath) {
		// Sync-merge conflict: worktree has MERGE_HEAD with conflict markers.
		conflictedFiles := GetResolvedConflictedFiles(worktreePath)
		if len(conflictedFiles) > 0 {
			addArgs := append([]string{"add"}, conflictedFiles...)
			if err := te.gitRun(worktreePath, addArgs...); err != nil {
				log.Printf("[merge] task=%s git add failed: %v", taskID, err)
				commitOK = false
			}
		}
	} else {
		// Final-merge conflict: worktree has no MERGE_HEAD.
		checkCmd := osexec.Command("git", "diff", "--quiet")
		checkCmd.Dir = worktreePath
		if checkCmd.Run() == nil {
			log.Printf("[merge] task=%s no changes to commit for conflict resolution", taskID)
			commitOK = false
		}
		if commitOK {
			if err := te.gitRun(worktreePath, "add", "-A"); err != nil {
				log.Printf("[merge] task=%s git add -A failed: %v", taskID, err)
				commitOK = false
			}
		}
	}

	if commitOK {
		if err := te.gitRun(worktreePath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...); err != nil {
			log.Printf("[merge] task=%s git commit failed: %v", taskID, err)
			commitOK = false
		}
	}

	if commitOK {
		log.Printf("[merge] task=%s conflict resolution committed", taskID)
		// Drop stale stash (contains LLM's pre-sync edits, already committed).
		// Do NOT pop — that would re-apply old edits as unstaged changes.
		te.worktreeMgr.GetProjectStashMu(projectID).Lock()
		_ = te.worktreeMgr.GitRunInDir(worktreePath, "stash", "drop")
		te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
	}

	_ = te.store.SetTaskMergeConflict(taskID, projectID, false)

	if commitOK && task.AutoMerge {
		if mergeErr := te.MergeBranch(projectID, taskID, task); mergeErr != nil {
			log.Printf("[merge] task=%s final merge failed: %v", taskID, mergeErr)
		}
	} else if commitOK && !task.AutoMerge {
		log.Printf("[merge] task=%s merge skipped (auto_merge=false)", taskID)
	}

	updatedTask, _ = te.store.GetTaskByTaskID(taskID)
	return commitOK, updatedTask
}
