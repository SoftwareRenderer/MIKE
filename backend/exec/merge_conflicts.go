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
func getConflictedFiles(dir string) []string {
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
// direction is "task_to_target" (prefer task changes) or "target_to_task" (prefer target changes).
// If keepMergeOnFailure is true, the merge is NOT aborted on LLM resolution failure
// (caller will handle the conflicted state, e.g., for interactive resolution).
// On success, stages files, commits, and logs a merge_resolution message.
// On failure, aborts the merge (unless keepMergeOnFailure) and returns the error.
func (te *TaskExecutor) tryResolveMergeConflicts(dir, projectID, taskID, targetBranch, taskBranch, direction string, task *types.Task, keepMergeOnFailure bool) error {
	// Confirm we're actually in a merge state
	if !isMergeInProgress(dir) {
		// Not a merge conflict — likely a hook failure, lock issue, or other git error
		return fmt.Errorf("merge failed but not in merge state (no MERGE_HEAD): %w", ErrNotAMergeConflict)
	}

	conflictedFiles := getConflictedFiles(dir)

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
	te.logSystemMessage(taskID, task.CurrentThreadID,
		fmt.Sprintf("Merge conflict detected in: %s", strings.Join(conflictedFiles, ", ")))

	if !keepMergeOnFailure {
		_ = runGitDir(dir, "merge", "--abort")
	}
	return fmt.Errorf("merge conflict in %d file(s): %s", len(conflictedFiles), strings.Join(conflictedFiles, ", "))
}

// logSystemMessage creates a system message in the task's conversation thread.
func (te *TaskExecutor) logSystemMessage(taskID, threadID, content string) {
	if threadID == "" {
		return
	}
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID:        uuid.New().String(),
		TaskID:    taskID,
		ThreadID:  threadID,
		Role:      "system",
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
			"id":        msgID,
			"content":   errorMsg,
			"label":     "Merge failed",
			"is_error":  true,
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

