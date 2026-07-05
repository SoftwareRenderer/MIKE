package exec

import (
	"context"
	"fmt"
	"log"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"kanban/backend/git"
	"kanban/backend/prompts"
	"kanban/backend/types"
)

// resolveMergeConflicts uses the LLM to resolve merge conflicts in the given files.
// basePath is the directory containing the conflicted files (worktree or project dir).
// targetBranch is the branch being merged into, taskBranch is the branch being merged.
// Returns the LLM-generated summary of the resolution.
func (te *TaskExecutor) resolveMergeConflicts(basePath, projectID, taskID, targetBranch, taskBranch string, task *types.Task, conflictedFiles []string) (string, error) {
	serverURL := task.LlamaServerURL
	modelName := task.ModelName
	if serverURL == "" || modelName == "" {
		return "", fmt.Errorf("no LLM config on task")
	}

	// Build conflict context for each file
	var conflictParts []string
	for _, f := range conflictedFiles {
		filePath := filepath.Join(basePath, f)
		content, err := os.ReadFile(filePath)
		if err != nil {
			log.Printf("[merge] task=%s failed to read conflicted file %s: %v", taskID, f, err)
			continue
		}
		conflictParts = append(conflictParts, fmt.Sprintf("FILE: %s\n---BEGIN---\n%s\n---END---", f, string(content)))
	}

	if len(conflictParts) == 0 {
		return "", fmt.Errorf("no conflict content to resolve")
	}

	conflictContext := strings.Join(conflictParts, "\n\n")
	prompt, err := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{
		TaskBranch:      taskBranch,
		TargetBranch:    targetBranch,
		TaskTitle:       task.Title,
		TaskDescription: task.Prompt,
		ConflictedFiles: conflictContext,
	})
	if err != nil {
		return "", fmt.Errorf("render merge conflict prompt: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	messages := []types.ChatMessage{
		{Role: "system", Content: "You are an expert developer resolving git merge conflicts. Output the fully resolved file contents and a summary."},
		{Role: "user", Content: prompt},
	}

	_, response, err := te.ChatCompletion(ctx, serverURL, modelName, messages)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}

	// Parse the response and write resolved files
	var summary string
	currentFile := ""
	var currentContent strings.Builder
	inResolve := false
	inSummary := false

	for line := range strings.SplitSeq(response, "\n") {
		if strings.HasPrefix(line, "RESOLVE ") {
			// Write previous file if any
			if currentFile != "" && inResolve {
				fullPath := filepath.Join(basePath, currentFile)
				if err := os.WriteFile(fullPath, []byte(currentContent.String()), 0644); err != nil {
					log.Printf("[merge] task=%s failed to write resolved file %s: %v", taskID, currentFile, err)
				}
			}
			currentFile = strings.TrimPrefix(line, "RESOLVE ")
			currentContent.Reset()
			inResolve = true
			inSummary = false
			continue
		}
		if line == "END_RESOLVE" {
			// Write current file
			if currentFile != "" && inResolve {
				fullPath := filepath.Join(basePath, currentFile)
				if err := os.WriteFile(fullPath, []byte(currentContent.String()), 0644); err != nil {
					log.Printf("[merge] task=%s failed to write resolved file %s: %v", taskID, currentFile, err)
				}
				inResolve = false
			}
			continue
		}
		if strings.HasPrefix(line, "SUMMARY") {
			inSummary = true
			inResolve = false
			continue
		}
		if line == "END_SUMMARY" {
			inSummary = false
			continue
		}
		if inResolve {
			currentContent.WriteString(line + "\n")
		} else if inSummary {
			if summary != "" {
				summary += "\n"
			}
			summary += line
		}
	}
	// Write last file if any
	if currentFile != "" && inResolve {
		fullPath := filepath.Join(basePath, currentFile)
		if err := os.WriteFile(fullPath, []byte(currentContent.String()), 0644); err != nil {
			log.Printf("[merge] task=%s failed to write resolved file %s: %v", taskID, currentFile, err)
		}
	}

	if summary == "" {
		summary = "Conflicts resolved (no summary provided by LLM)"
	}

	return summary, nil
}

// isMergeInProgress checks whether a git merge is currently in progress in the given directory.
// A merge is in progress if .git/MERGE_HEAD exists.
func isMergeInProgress(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD"))
	return err == nil
}

// getConflictedFilesInDir returns a list of files with merge conflicts in the given directory.
// Uses git status --porcelain which catches ALL conflict types (UU, AD, DU, AA, DD, UD, UA),
// unlike git diff --name-only --diff-filter=U which misses directory/file type conflicts.
func getConflictedFilesInDir(dir string) []string {
	cmd := osexec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 4 {
			continue
		}
		// Unmerged entries start with two status letters, one of which is 'U'
		status := line[:2]
		if strings.ContainsRune(status, 'U') {
			f := strings.TrimSpace(line[3:]) // skip "XY " prefix
			if f != "" {
				files = append(files, f)
			}
		}
	}
	return files
}

// getConflictedFilesFallback returns conflicted files using git diff --diff-filter=U.
// This catches cases that git status --porcelain might miss, such as binary conflicts
// and rename conflicts.
func getConflictedFilesFallback(dir string) []string {
	cmd := osexec.Command("git", "diff", "--name-only", "--diff-filter=U")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.TrimSpace(line)
		if f != "" {
			files = append(files, f)
		}
	}
	return files
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
// On failure, aborts the merge and returns the error (caller should handle gracefully).
func (te *TaskExecutor) tryResolveMergeConflicts(dir, projectID, taskID, targetBranch, taskBranch string, task *types.Task) error {
	// Confirm we're actually in a merge state
	if !isMergeInProgress(dir) {
		// Not a merge conflict — likely a hook failure, lock issue, or other git error
		return fmt.Errorf("merge failed but not in merge state (no MERGE_HEAD)")
	}

	// Primary scan: git status --porcelain (catches UU, AD, DU, AA, DD, UD, UA)
	conflictedFiles := getConflictedFilesInDir(dir)

	// Fallback scan: git diff --diff-filter=U (catches binary conflicts, renames)
	if len(conflictedFiles) == 0 {
		conflictedFiles = getConflictedFilesFallback(dir)
		if len(conflictedFiles) > 0 {
			log.Printf("[merge] task=%s fallback scan found %d conflicted file(s): %s",
				taskID, len(conflictedFiles), strings.Join(conflictedFiles, ", "))
		}
	}

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

	log.Printf("[merge] task=%s conflict, resolving %d file(s) with LLM: %s",
		taskID, len(conflictedFiles), strings.Join(conflictedFiles, ", "))

	// Log conflict detection as system message
	te.logSystemMessage(taskID, task.CurrentThreadID,
		fmt.Sprintf("Merge conflict detected in: %s. Resolving with LLM...", strings.Join(conflictedFiles, ", ")))

	// Resolve conflicts with LLM
	summary, err := te.resolveMergeConflicts(dir, projectID, taskID, targetBranch, taskBranch, task, conflictedFiles)
	if err != nil {
		log.Printf("[merge] task=%s LLM conflict resolution failed: %v", taskID, err)
		_ = runGitDir(dir, "merge", "--abort")
		return fmt.Errorf("LLM conflict resolution failed: %w", err)
	}

	// Stage resolved files and commit
	if err := commitMergeResolutionInDir(dir, task, summary); err != nil {
		_ = runGitDir(dir, "merge", "--abort")
		return err
	}

	log.Printf("[merge] task=%s conflicts resolved and committed", taskID)

	// Log resolution as a merge_resolution message (persisted + broadcast)
	te.logMergeResolution(taskID, task.CurrentThreadID, conflictedFiles, summary)

	return nil
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

// logMergeResolution persists a merge_resolution message and broadcasts it to task stream clients.
func (te *TaskExecutor) logMergeResolution(taskID, threadID string, conflictedFiles []string, summary string) {
	if threadID == "" {
		return
	}
	content := fmt.Sprintf("Merge conflicts resolved in %d file(s): %s\n\n%s",
		len(conflictedFiles), strings.Join(conflictedFiles, ", "), summary)
	te.persistAndBroadcastMergeMessage(taskID, threadID, content, "Merge conflicts resolved")
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

// commitMergeResolutionInDir stages resolved files and commits the merge in the given directory.
func commitMergeResolutionInDir(dir string, task *types.Task, summary string) error {
	if err := runGitDir(dir, append(git.GitAuthorFlags(), "add", "-A")...); err != nil {
		return fmt.Errorf("stage resolved files: %w", err)
	}

	modelName := task.ModelName
	if modelName == "" {
		modelName = "unknown model"
	}
	commitMsg := fmt.Sprintf("%s\n\n%s\n\nCo-authored-by: %s", task.Title, summary, modelName)
	if err := runGitDir(dir, append(git.GitAuthorFlags(), "commit", "-m", commitMsg)...); err != nil {
		return fmt.Errorf("commit resolved merge: %w", err)
	}
	return nil
}
