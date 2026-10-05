package exec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/git"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// TestMergeConflictResolution_StagesOnlyConflictedFiles verifies that when
// the LLM resolves a merge conflict by editing conflicted files, the backend
// stages ONLY the conflicted files (not all changes) before committing.
// This ensures non-conflicted work-in-progress is not accidentally committed.
func TestMergeConflictResolution_StagesOnlyConflictedFiles(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "conflict-resolve-task"
	task := &types.Task{
		ID:               taskID,
		ProjectID:        projectID,
		Title:            "Conflict Resolution Test",
		Prompt:           "Resolve merge conflicts",
		Status:           types.StatusInProgress,
		Branch:           "task/conflict-resolve",
		BaseRef:          "master",
		LlamaServerURL:   "http://localhost:8080",
		ModelName:        "test-model",
		HasMergeConflict: true,
		CurrentThreadID:  "thread-1",
	}
	te.store.CreateTask(task)

	// Create task branch with a change to shared.txt
	testgit.Git(t, repoDir, "checkout", "-b", "task/conflict-resolve")
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("task version of shared\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "task edits shared")
	testgit.Git(t, repoDir, "checkout", "master")

	// Create worktree for the task
	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", wtPath, "task/conflict-resolve")
	testgit.Git(t, wtPath, "checkout", "task/conflict-resolve")

	// Add conflicting change to master
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("master version of shared\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "master edits shared")

	// Manually simulate the sync-merge that creates the conflict state:
	// 1. Stash working tree changes
	unlockStash1 := te.worktreeMgr.LockProjectStash(projectID)
	_ = te.worktreeMgr.GitRunInDir(wtPath, "stash", "push", "-u", "-m", "kanban sync-merge stash")
	unlockStash1()

	// 2. Checkout task branch
	testgit.Git(t, wtPath, "checkout", "-f", "task/conflict-resolve")

	// 3. Merge master into task branch (will conflict)
	cmd := runGitCmd(wtPath, "merge", "master", "--no-edit")
	if cmd.exitCode == 0 {
		t.Fatal("merge should have conflicted")
	}

	// Verify conflict state
	if !isMergeInProgress(wtPath) {
		t.Fatal("merge should be in progress")
	}
	conflictedFiles := GetConflictedFiles(wtPath)
	if len(conflictedFiles) != 1 || conflictedFiles[0] != "shared.txt" {
		t.Fatalf("expected [shared.txt], got %v", conflictedFiles)
	}

	// --- Simulate LLM resolution ---
	// The LLM reads the conflicted file and writes a resolved version.
	// It also creates a new file (non-conflicted, should NOT be committed).
	resolvedContent := "resolved: combined task and master changes\n"
	os.WriteFile(filepath.Join(wtPath, "shared.txt"), []byte(resolvedContent), 0o644)
	// Non-conflicted change (LLM created a new file as part of the task)
	os.WriteFile(filepath.Join(wtPath, "new_feature.txt"), []byte("new feature by LLM\n"), 0o644)

	// --- Execute the conflict resolution commit logic (from loop.go request_review handler) ---
	// This is the exact logic from the request_review handler:
	// 1. Stage only RESOLVED conflicted files (no conflict markers)
	conflictedFiles = GetResolvedConflictedFiles(wtPath)
	if len(conflictedFiles) > 0 {
		addArgs := append([]string{"add"}, conflictedFiles...)
		if _, err := git.Run(wtPath, addArgs...); err != nil {
			t.Fatalf("git add conflicted files failed: %v", err)
		}
	}

	// 2. Commit the resolution (in modern git, this completes the merge)
	if _, err := git.Run(wtPath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// 3. Pop stash (restore user's unstaged changes)
	unlockStash2 := te.worktreeMgr.LockProjectStash(projectID)
	_ = te.worktreeMgr.GitRunInDir(wtPath, "stash", "pop")
	unlockStash2()

	// 4. Clear merge conflict flag
	_ = te.store.SetTaskMergeConflict(taskID, projectID, false)

	// --- Verify ---

	// Verify the merge is no longer in progress (git commit completed it)
	if isMergeInProgress(wtPath) {
		t.Error("merge should no longer be in progress after resolution")
	}

	// Verify shared.txt has the resolved content
	content, err := os.ReadFile(filepath.Join(wtPath, "shared.txt"))
	if err != nil {
		t.Fatalf("read shared.txt: %v", err)
	}
	if string(content) != resolvedContent {
		t.Errorf("shared.txt should have resolved content, got %q", string(content))
	}

	// Verify new_feature.txt exists but was NOT committed (it's an unstaged change)
	newFeaturePath := filepath.Join(wtPath, "new_feature.txt")
	if _, err := os.Stat(newFeaturePath); err != nil {
		t.Error("new_feature.txt should still exist in worktree")
	}

	// Check git log — only the conflict resolution commit should exist (not new_feature.txt)
	cmd = runGitCmd(wtPath, "log", "--oneline", "-2")
	logOutput := cmd.output
	if !strings.Contains(logOutput, "Resolve merge conflicts") {
		t.Errorf("git log should contain 'Resolve merge conflicts', got:\n%s", logOutput)
	}

	// Verify the task branch has the resolved content
	cmd = runGitCmd(repoDir, "show", "task/conflict-resolve:shared.txt")
	if cmd.exitCode != 0 {
		t.Fatalf("shared.txt should exist on task branch: %s", cmd.output)
	}
	if !strings.Contains(cmd.output, "combined task and master changes") {
		t.Errorf("task branch shared.txt should have resolved content, got: %q", cmd.output)
	}

	// Verify new_feature.txt is NOT on the branch (only in working tree)
	cmd = runGitCmd(repoDir, "show", "task/conflict-resolve:new_feature.txt")
	if cmd.exitCode == 0 {
		t.Error("new_feature.txt should NOT be committed — only conflicted files should be committed")
	}
}

// TestMergeConflictResolution_MultipleConflictedFiles verifies that when
// multiple files have conflicts, ALL conflicted files are staged and committed.
func TestMergeConflictResolution_MultipleConflictedFiles(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "multi-conflict-task"
	task := &types.Task{
		ID:               taskID,
		ProjectID:        projectID,
		Title:            "Multi Conflict Test",
		Prompt:           "Resolve multiple merge conflicts",
		Status:           types.StatusInProgress,
		Branch:           "task/multi-conflict",
		BaseRef:          "master",
		LlamaServerURL:   "http://localhost:8080",
		ModelName:        "test-model",
		HasMergeConflict: true,
		CurrentThreadID:  "thread-1",
	}
	te.store.CreateTask(task)

	// Create task branch with changes to multiple files
	testgit.Git(t, repoDir, "checkout", "-b", "task/multi-conflict")
	os.WriteFile(filepath.Join(repoDir, "fileA.txt"), []byte("task version A\n"), 0o644)
	os.WriteFile(filepath.Join(repoDir, "fileB.txt"), []byte("task version B\n"), 0o644)
	testgit.Git(t, repoDir, "add", "fileA.txt", "fileB.txt")
	testgit.Git(t, repoDir, "commit", "-m", "task edits A and B")
	testgit.Git(t, repoDir, "checkout", "master")

	// Create worktree
	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", wtPath, "task/multi-conflict")
	testgit.Git(t, wtPath, "checkout", "task/multi-conflict")

	// Conflicting changes on master for the same files
	os.WriteFile(filepath.Join(repoDir, "fileA.txt"), []byte("master version A\n"), 0o644)
	os.WriteFile(filepath.Join(repoDir, "fileB.txt"), []byte("master version B\n"), 0o644)
	testgit.Git(t, repoDir, "add", "fileA.txt", "fileB.txt")
	testgit.Git(t, repoDir, "commit", "-m", "master edits A and B")

	// Simulate sync-merge conflict
	unlockStash3 := te.worktreeMgr.LockProjectStash(projectID)
	_ = te.worktreeMgr.GitRunInDir(wtPath, "stash", "push", "-u", "-m", "kanban sync-merge stash")
	unlockStash3()
	testgit.Git(t, wtPath, "checkout", "-f", "task/multi-conflict")
	cmd := runGitCmd(wtPath, "merge", "master", "--no-edit")
	if cmd.exitCode == 0 {
		t.Fatal("merge should have conflicted")
	}

	// Verify both files are conflicted
	conflictedFiles := GetConflictedFiles(wtPath)
	if len(conflictedFiles) != 2 {
		t.Fatalf("expected 2 conflicted files, got %d: %v", len(conflictedFiles), conflictedFiles)
	}

	// --- Simulate LLM resolution ---
	os.WriteFile(filepath.Join(wtPath, "fileA.txt"), []byte("resolved A\n"), 0o644)
	os.WriteFile(filepath.Join(wtPath, "fileB.txt"), []byte("resolved B\n"), 0o644)

	// --- Execute conflict resolution commit ---
	conflictedFiles = GetResolvedConflictedFiles(wtPath)
	if len(conflictedFiles) > 0 {
		addArgs := append([]string{"add"}, conflictedFiles...)
		if _, err := git.Run(wtPath, addArgs...); err != nil {
			t.Fatalf("git add failed: %v", err)
		}
	}
	if _, err := git.Run(wtPath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}
	unlockStash4 := te.worktreeMgr.LockProjectStash(projectID)
	_ = te.worktreeMgr.GitRunInDir(wtPath, "stash", "pop")
	unlockStash4()

	// --- Verify ---
	if isMergeInProgress(wtPath) {
		t.Error("merge should no longer be in progress")
	}

	// Both resolved files should be on the task branch
	cmd = runGitCmd(repoDir, "show", "task/multi-conflict:fileA.txt")
	if cmd.exitCode != 0 || !strings.Contains(cmd.output, "resolved A") {
		t.Errorf("fileA.txt on branch should be resolved, got: %s", cmd.output)
	}
	cmd = runGitCmd(repoDir, "show", "task/multi-conflict:fileB.txt")
	if cmd.exitCode != 0 || !strings.Contains(cmd.output, "resolved B") {
		t.Errorf("fileB.txt on branch should be resolved, got: %s", cmd.output)
	}
}

// TestMergeConflictResolution_CommitMessage verifies that the conflict
// resolution commit uses the correct message "Resolve merge conflicts".
func TestMergeConflictResolution_CommitMessage(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "commit-msg-task"
	task := &types.Task{
		ID:               taskID,
		ProjectID:        projectID,
		Title:            "Commit Message Test",
		Prompt:           "Test commit message",
		Status:           types.StatusInProgress,
		Branch:           "task/commit-msg",
		BaseRef:          "master",
		LlamaServerURL:   "http://localhost:8080",
		ModelName:        "test-model",
		HasMergeConflict: true,
		CurrentThreadID:  "thread-1",
	}
	te.store.CreateTask(task)

	// Set up conflict
	testgit.Git(t, repoDir, "checkout", "-b", "task/commit-msg")
	os.WriteFile(filepath.Join(repoDir, "conflict.txt"), []byte("task content\n"), 0o644)
	testgit.Git(t, repoDir, "add", "conflict.txt")
	testgit.Git(t, repoDir, "commit", "-m", "task edit")
	testgit.Git(t, repoDir, "checkout", "master")

	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", wtPath, "task/commit-msg")
	testgit.Git(t, wtPath, "checkout", "task/commit-msg")

	os.WriteFile(filepath.Join(repoDir, "conflict.txt"), []byte("master content\n"), 0o644)
	testgit.Git(t, repoDir, "add", "conflict.txt")
	testgit.Git(t, repoDir, "commit", "-m", "master edit")

	// Simulate sync-merge conflict
	unlockStash5 := te.worktreeMgr.LockProjectStash(projectID)
	_ = te.worktreeMgr.GitRunInDir(wtPath, "stash", "push", "-u", "-m", "kanban sync-merge stash")
	unlockStash5()
	testgit.Git(t, wtPath, "checkout", "-f", "task/commit-msg")
	runGitCmd(wtPath, "merge", "master", "--no-edit")

	// Resolve conflict
	os.WriteFile(filepath.Join(wtPath, "conflict.txt"), []byte("resolved content\n"), 0o644)

	// Commit resolution
	conflictedFiles := GetResolvedConflictedFiles(wtPath)
	if len(conflictedFiles) > 0 {
		addArgs := append([]string{"add"}, conflictedFiles...)
		_, _ = git.Run(wtPath, addArgs...)
	}
	_, _ = git.Run(wtPath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...)

	// Verify commit message
	cmd := runGitCmd(wtPath, "log", "-1", "--format=%s")
	if strings.TrimSpace(cmd.output) != "Resolve merge conflicts" {
		t.Errorf("expected commit message 'Resolve merge conflicts', got %q", strings.TrimSpace(cmd.output))
	}
}

// TestMergeConflictResolution_WithFinalMerge verifies the full flow:
// sync merge conflict → LLM resolution → commit (completes merge) → final merge into target.
func TestMergeConflictResolution_WithFinalMerge(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "full-flow-task"
	task := &types.Task{
		ID:               taskID,
		ProjectID:        projectID,
		Title:            "Full Flow Test",
		Prompt:           "Full merge conflict flow",
		Status:           types.StatusInProgress,
		Branch:           "task/full-flow",
		BaseRef:          "master",
		LlamaServerURL:   "http://localhost:8080",
		ModelName:        "test-model",
		HasMergeConflict: true,
		AutoCommit:       true,
		AutoMerge:        true,
		CurrentThreadID:  "thread-1",
	}
	te.store.CreateTask(task)

	// Create task branch with a change
	testgit.Git(t, repoDir, "checkout", "-b", "task/full-flow")
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("line 1\ntask change\nline 3\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "task edit")
	testgit.Git(t, repoDir, "checkout", "master")

	// Create worktree
	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", wtPath, "task/full-flow")
	testgit.Git(t, wtPath, "checkout", "task/full-flow")

	// Conflicting change on master
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("line 1\nmaster change\nline 3\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "master edit")

	// Simulate sync-merge conflict
	unlockStash6 := te.worktreeMgr.LockProjectStash(projectID)
	_ = te.worktreeMgr.GitRunInDir(wtPath, "stash", "push", "-u", "-m", "kanban sync-merge stash")
	unlockStash6()
	testgit.Git(t, wtPath, "checkout", "-f", "task/full-flow")
	runGitCmd(wtPath, "merge", "master", "--no-edit")

	// Verify conflict state
	if !isMergeInProgress(wtPath) {
		t.Fatal("merge should be in progress")
	}

	// --- Simulate LLM resolution ---
	resolvedContent := "line 1\ncombined task and master\nline 3\n"
	os.WriteFile(filepath.Join(wtPath, "shared.txt"), []byte(resolvedContent), 0o644)

	// --- Execute conflict resolution commit ---
	conflictedFiles := GetResolvedConflictedFiles(wtPath)
	if len(conflictedFiles) > 0 {
		addArgs := append([]string{"add"}, conflictedFiles...)
		_, _ = git.Run(wtPath, addArgs...)
	}
	_, _ = git.Run(wtPath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...)
	unlockStash7 := te.worktreeMgr.LockProjectStash(projectID)
	_ = te.worktreeMgr.GitRunInDir(wtPath, "stash", "pop")
	unlockStash7()
	_ = te.store.SetTaskMergeConflict(taskID, projectID, false)

	// --- Attempt final merge (task branch into master) ---
	task, _ = te.store.GetTaskByID(taskID)
	if task.HasMergeConflict {
		t.Fatal("task should not have merge conflict after resolution")
	}

	// Merge task branch into master
	err := te.MergeBranch(projectID, taskID, task)
	if err != nil {
		t.Fatalf("final MergeBranch failed: %v", err)
	}

	// Verify the resolved content is on master
	testgit.Git(t, repoDir, "checkout", "master")
	content, err := os.ReadFile(filepath.Join(repoDir, "shared.txt"))
	if err != nil {
		t.Fatalf("read shared.txt on master: %v", err)
	}
	if string(content) != resolvedContent {
		t.Errorf("master shared.txt should have resolved content, got %q", string(content))
	}
}

// TestMergeConflictResolution_StashRestored verifies that user's unstaged
// changes are properly restored after conflict resolution via stash pop.
func TestMergeConflictResolution_StashRestored(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "stash-restore-task"
	task := &types.Task{
		ID:               taskID,
		ProjectID:        projectID,
		Title:            "Stash Restore Test",
		Prompt:           "Test stash restore",
		Status:           types.StatusInProgress,
		Branch:           "task/stash-restore",
		BaseRef:          "master",
		LlamaServerURL:   "http://localhost:8080",
		ModelName:        "test-model",
		HasMergeConflict: true,
		CurrentThreadID:  "thread-1",
	}
	te.store.CreateTask(task)

	// Create task branch
	testgit.Git(t, repoDir, "checkout", "-b", "task/stash-restore")
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("task version\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "task edit")
	testgit.Git(t, repoDir, "checkout", "master")

	// Create worktree
	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", wtPath, "task/stash-restore")
	testgit.Git(t, wtPath, "checkout", "task/stash-restore")

	// User has unstaged changes (e.g., work-in-progress notes)
	unstagedFile := filepath.Join(wtPath, "notes.txt")
	os.WriteFile(unstagedFile, []byte("user's work in progress notes\n"), 0o644)

	// Conflicting change on master
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("master version\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "master edit")

	// Simulate sync-merge conflict (stashes the notes.txt)
	unlockStash8 := te.worktreeMgr.LockProjectStash(projectID)
	stashErr := te.worktreeMgr.GitRunInDir(wtPath, "stash", "push", "-u", "-m", "kanban sync-merge stash")
	unlockStash8()
	if stashErr != nil {
		t.Fatalf("stash should succeed: %v", stashErr)
	}

	// notes.txt should be gone (stashed)
	if _, err := os.Stat(unstagedFile); err == nil {
		t.Error("notes.txt should be stashed (not in worktree)")
	}

	testgit.Git(t, wtPath, "checkout", "-f", "task/stash-restore")
	runGitCmd(wtPath, "merge", "master", "--no-edit")

	// Resolve conflict
	os.WriteFile(filepath.Join(wtPath, "shared.txt"), []byte("resolved\n"), 0o644)

	// Commit resolution
	conflictedFiles := GetResolvedConflictedFiles(wtPath)
	if len(conflictedFiles) > 0 {
		addArgs := append([]string{"add"}, conflictedFiles...)
		_, _ = git.Run(wtPath, addArgs...)
	}
	_, _ = git.Run(wtPath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...)

	// Pop stash — should restore notes.txt
	unlockStash9 := te.worktreeMgr.LockProjectStash(projectID)
	popErr := te.worktreeMgr.GitRunInDir(wtPath, "stash", "pop")
	unlockStash9()

	// Verify notes.txt is restored
	if popErr != nil {
		t.Logf("stash pop warning: %v (may be ok if clean working tree)", popErr)
	}
	content, err := os.ReadFile(unstagedFile)
	if err != nil {
		t.Fatalf("notes.txt should be restored after stash pop: %v", err)
	}
	if string(content) != "user's work in progress notes\n" {
		t.Errorf("notes.txt should have original content, got %q", string(content))
	}
}

// TestMergeConflictResolution_NoConflictedFilesAutoContinues verifies that
// when there are no conflicted files (git auto-resolved), the merge --continue
// succeeds without needing an explicit commit.
func TestMergeConflictResolution_NoConflictedFilesAutoContinues(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "auto-resolve-task"
	task := &types.Task{
		ID:               taskID,
		ProjectID:        projectID,
		Title:            "Auto Resolve Test",
		Prompt:           "Test auto resolve",
		Status:           types.StatusInProgress,
		Branch:           "task/auto-resolve",
		BaseRef:          "master",
		LlamaServerURL:   "http://localhost:8080",
		ModelName:        "test-model",
		HasMergeConflict: false,
		CurrentThreadID:  "thread-1",
	}
	te.store.CreateTask(task)

	// Create task branch with a file
	testgit.Git(t, repoDir, "checkout", "-b", "task/auto-resolve")
	os.WriteFile(filepath.Join(repoDir, "task_only.txt"), []byte("task only file\n"), 0o644)
	testgit.Git(t, repoDir, "add", "task_only.txt")
	testgit.Git(t, repoDir, "commit", "-m", "task adds file")
	testgit.Git(t, repoDir, "checkout", "master")

	// Create worktree
	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", wtPath, "task/auto-resolve")
	testgit.Git(t, wtPath, "checkout", "task/auto-resolve")

	// Non-conflicting change on master (different file)
	os.WriteFile(filepath.Join(repoDir, "master_only.txt"), []byte("master only file\n"), 0o644)
	testgit.Git(t, repoDir, "add", "master_only.txt")
	testgit.Git(t, repoDir, "commit", "-m", "master adds file")

	// Merge should succeed cleanly (no conflicts)
	err := te.merge.syncTargetIntoTask(projectID, taskID, "master", "task/auto-resolve", task)
	if err != nil {
		t.Fatalf("sync should succeed (no conflicts): %v", err)
	}

	// Verify no merge conflict flag
	task, _ = te.store.GetTaskByID(taskID)
	if task.HasMergeConflict {
		t.Error("should not have merge conflict flag for clean merge")
	}

	// Verify both files exist
	if _, err := os.Stat(filepath.Join(wtPath, "task_only.txt")); err != nil {
		t.Error("task_only.txt should exist")
	}
	if _, err := os.Stat(filepath.Join(wtPath, "master_only.txt")); err != nil {
		t.Error("master_only.txt should exist (merged from master)")
	}
}

// TestMergeConflictResolution_UnresolvedSkipped verifies that when the LLM
// fails to properly resolve conflicts (conflict markers remain),
// GetResolvedConflictedFiles returns nil and no commit is attempted.
// This prevents committing files with conflict markers.
func TestMergeConflictResolution_UnresolvedSkipped(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "unresolved-task"
	task := &types.Task{
		ID:               taskID,
		ProjectID:        projectID,
		Title:            "Unresolved Test",
		Prompt:           "Test unresolved",
		Status:           types.StatusInProgress,
		Branch:           "task/unresolved",
		BaseRef:          "master",
		LlamaServerURL:   "http://localhost:8080",
		ModelName:        "test-model",
		HasMergeConflict: true,
		CurrentThreadID:  "thread-1",
	}
	te.store.CreateTask(task)

	// Create task branch with a change
	testgit.Git(t, repoDir, "checkout", "-b", "task/unresolved")
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("task version\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "task edit")
	testgit.Git(t, repoDir, "checkout", "master")

	// Create worktree
	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", wtPath, "task/unresolved")
	testgit.Git(t, wtPath, "checkout", "task/unresolved")

	// Conflicting change on master
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("master version\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "master edit")

	// Simulate sync-merge conflict
	unlockStash10 := te.worktreeMgr.LockProjectStash(projectID)
	_ = te.worktreeMgr.GitRunInDir(wtPath, "stash", "push", "-u", "-m", "kanban sync-merge stash")
	unlockStash10()
	testgit.Git(t, wtPath, "checkout", "-f", "task/unresolved")
	runGitCmd(wtPath, "merge", "master", "--no-edit")

	// --- Bad LLM resolution: leaves conflict markers ---
	badContent := "<<<<<<< HEAD\ntask version\n=======\nmaster version\n>>>>>>> master\n"
	os.WriteFile(filepath.Join(wtPath, "shared.txt"), []byte(badContent), 0o644)

	// GetResolvedConflictedFiles should return nil (all files still have markers)
	resolvedFiles := GetResolvedConflictedFiles(wtPath)
	if len(resolvedFiles) != 0 {
		t.Fatalf("expected no resolved files (markers remain), got %v", resolvedFiles)
	}

	// GetAllConflictsResolved should return false
	if GetAllConflictsResolved(wtPath) {
		t.Fatal("GetAllConflictsResolved should return false when markers remain")
	}

	// Since no files to stage, the production code skips the commit.
	// Merge is still in progress — the LLM needs to try again.
	if !isMergeInProgress(wtPath) {
		t.Error("merge should still be in progress (not committed)")
	}
}

// TestMergeConflictResolution_SubtaskIntoParentBranch verifies the full
// conflict resolution flow for a subtask merging into a parent branch.
func TestMergeConflictResolution_SubtaskIntoParentBranch(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent task
	parentID := "parent-conflict-resolve"
	parent := &types.Task{
		ID:              parentID,
		ProjectID:       projectID,
		Title:           "Parent Task",
		Prompt:          "Parent prompt",
		Status:          types.StatusInProgress,
		Branch:          "task/" + parentID,
		LlamaServerURL:  "http://localhost:8080",
		ModelName:       "test-model",
		CurrentThreadID: "thread-parent",
	}
	te.store.CreateTask(parent)

	// Create parent branch
	testgit.Git(t, repoDir, "checkout", "-b", "task/"+parentID)
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("parent initial\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "parent initial")
	testgit.Git(t, repoDir, "checkout", "master")

	// Create parent worktree
	parentWtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, parentID)
	os.MkdirAll(parentWtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", parentWtPath, "task/"+parentID)
	testgit.Git(t, parentWtPath, "checkout", "task/"+parentID)

	// Create subtask
	subtaskID := "subtask-conflict-resolve"
	subtask := &types.Task{
		ID:              subtaskID,
		ProjectID:       projectID,
		Title:           "Subtask",
		Prompt:          "Subtask prompt",
		Status:          types.StatusInProgress,
		ParentTaskID:    parentID,
		Branch:          "task/" + subtaskID,
		LlamaServerURL:  "http://localhost:8080",
		ModelName:       "test-model",
		CurrentThreadID: "thread-subtask",
	}
	te.store.CreateTask(subtask)

	// Create subtask branch from parent branch
	testgit.Git(t, parentWtPath, "checkout", "-b", "task/"+subtaskID)
	os.WriteFile(filepath.Join(parentWtPath, "shared.txt"), []byte("parent initial\ntask change\n"), 0o644)
	testgit.Git(t, parentWtPath, "add", "shared.txt")
	testgit.Git(t, parentWtPath, "commit", "-m", "subtask edit shared")
	testgit.Git(t, parentWtPath, "checkout", "task/"+parentID)

	// Create subtask worktree
	subtaskWtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, subtaskID)
	os.MkdirAll(subtaskWtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", subtaskWtPath, "task/"+subtaskID)
	testgit.Git(t, subtaskWtPath, "checkout", "task/"+subtaskID)

	// Parent makes a conflicting change to shared.txt
	os.WriteFile(filepath.Join(parentWtPath, "shared.txt"), []byte("parent initial\nparent change\n"), 0o644)
	testgit.Git(t, parentWtPath, "add", "shared.txt")
	testgit.Git(t, parentWtPath, "commit", "-m", "parent edit shared")

	// --- Simulate sync-merge conflict in subtask worktree ---
	unlockStash11 := te.worktreeMgr.LockProjectStash(projectID)
	_ = te.worktreeMgr.GitRunInDir(subtaskWtPath, "stash", "push", "-u", "-m", "kanban sync-merge stash")
	unlockStash11()
	testgit.Git(t, subtaskWtPath, "checkout", "-f", "task/"+subtaskID)
	runGitCmd(subtaskWtPath, "merge", "task/"+parentID, "--no-edit")

	// Verify conflict
	if !isMergeInProgress(subtaskWtPath) {
		t.Fatal("merge should be in progress (conflict)")
	}

	// --- Simulate LLM resolution ---
	resolvedContent := "parent initial\ncombined parent and subtask changes\n"
	os.WriteFile(filepath.Join(subtaskWtPath, "shared.txt"), []byte(resolvedContent), 0o644)

	// --- Execute conflict resolution commit ---
	conflictedFiles := GetResolvedConflictedFiles(subtaskWtPath)
	if len(conflictedFiles) > 0 {
		addArgs := append([]string{"add"}, conflictedFiles...)
		_, _ = git.Run(subtaskWtPath, addArgs...)
	}
	_, _ = git.Run(subtaskWtPath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...)
	unlockStash12 := te.worktreeMgr.LockProjectStash(projectID)
	_ = te.worktreeMgr.GitRunInDir(subtaskWtPath, "stash", "pop")
	unlockStash12()
	_ = te.store.SetTaskMergeConflict(subtaskID, projectID, false)

	// --- Merge subtask into parent branch ---
	subtaskFromDB, _ := te.store.GetTaskByID(subtaskID)
	err := te.MergeBranch(projectID, subtaskID, subtaskFromDB)
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Verify resolved content is on parent branch
	cmd := runGitCmd(repoDir, "show", "task/"+parentID+":shared.txt")
	if cmd.exitCode != 0 {
		t.Fatalf("shared.txt should exist on parent branch: %s", cmd.output)
	}
	if !strings.Contains(cmd.output, "combined parent and subtask changes") {
		t.Errorf("parent branch should have resolved content, got: %q", cmd.output)
	}
}

// TestMergeConflictResolution_PartialResolution_SkipsCommit verifies that when
// the LLM resolves some files but not others (leaves conflict markers in some),
// GetResolvedConflictedFiles returns nil — git requires ALL conflicts to be
// resolved before a merge commit can be made. The commit is skipped and the
// LLM is re-prompted to fix the remaining conflicts.
func TestMergeConflictResolution_PartialResolution_SkipsCommit(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "partial-resolve-task"
	task := &types.Task{
		ID:               taskID,
		ProjectID:        projectID,
		Title:            "Partial Resolve Test",
		Prompt:           "Resolve some conflicts",
		Status:           types.StatusInProgress,
		Branch:           "task/partial-resolve",
		BaseRef:          "master",
		LlamaServerURL:   "http://localhost:8080",
		ModelName:        "test-model",
		HasMergeConflict: true,
		CurrentThreadID:  "thread-1",
	}
	te.store.CreateTask(task)

	// Create task branch with changes to two files
	testgit.Git(t, repoDir, "checkout", "-b", "task/partial-resolve")
	os.WriteFile(filepath.Join(repoDir, "fileA.txt"), []byte("task version A\n"), 0o644)
	os.WriteFile(filepath.Join(repoDir, "fileB.txt"), []byte("task version B\n"), 0o644)
	testgit.Git(t, repoDir, "add", "fileA.txt", "fileB.txt")
	testgit.Git(t, repoDir, "commit", "-m", "task edits A and B")
	testgit.Git(t, repoDir, "checkout", "master")

	// Create worktree
	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", wtPath, "task/partial-resolve")
	testgit.Git(t, wtPath, "checkout", "task/partial-resolve")

	// Conflicting changes on master
	os.WriteFile(filepath.Join(repoDir, "fileA.txt"), []byte("master version A\n"), 0o644)
	os.WriteFile(filepath.Join(repoDir, "fileB.txt"), []byte("master version B\n"), 0o644)
	testgit.Git(t, repoDir, "add", "fileA.txt", "fileB.txt")
	testgit.Git(t, repoDir, "commit", "-m", "master edits A and B")

	// Simulate sync-merge conflict
	unlockStash13 := te.worktreeMgr.LockProjectStash(projectID)
	_ = te.worktreeMgr.GitRunInDir(wtPath, "stash", "push", "-u", "-m", "kanban sync-merge stash")
	unlockStash13()
	testgit.Git(t, wtPath, "checkout", "-f", "task/partial-resolve")
	runGitCmd(wtPath, "merge", "master", "--no-edit")

	// Verify both files are conflicted
	conflictedFiles := GetConflictedFiles(wtPath)
	if len(conflictedFiles) != 2 {
		t.Fatalf("expected 2 conflicted files, got %d: %v", len(conflictedFiles), conflictedFiles)
	}

	// --- Simulate PARTIAL LLM resolution ---
	// LLM resolves fileA but leaves fileB with conflict markers
	os.WriteFile(filepath.Join(wtPath, "fileA.txt"), []byte("resolved A\n"), 0o644)
	badContent := "<<<<<<< HEAD\ntask version B\n=======\nmaster version B\n>>>>>>> master\n"
	os.WriteFile(filepath.Join(wtPath, "fileB.txt"), []byte(badContent), 0o644)

	// GetResolvedConflictedFiles returns nil because NOT ALL conflicts are resolved.
	// Git requires ALL conflicted files to be staged for a merge commit.
	resolvedFiles := GetResolvedConflictedFiles(wtPath)
	if len(resolvedFiles) != 0 {
		t.Fatalf("expected nil (partial resolution, cannot commit), got %v", resolvedFiles)
	}

	// GetAllConflictsResolved should return false
	if GetAllConflictsResolved(wtPath) {
		t.Fatal("GetAllConflictsResolved should return false when any file has markers")
	}

	// Merge is still in progress — the LLM needs to fix fileB
	if !isMergeInProgress(wtPath) {
		t.Error("merge should still be in progress (not committed)")
	}
}

// TestMergeConflictResolution_CommitIsMergeCommit verifies that the conflict
// resolution commit is a proper merge commit (has two parents).
func TestMergeConflictResolution_CommitIsMergeCommit(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "merge-commit-task"
	task := &types.Task{
		ID:               taskID,
		ProjectID:        projectID,
		Title:            "Merge Commit Test",
		Prompt:           "Test merge commit",
		Status:           types.StatusInProgress,
		Branch:           "task/merge-commit",
		BaseRef:          "master",
		LlamaServerURL:   "http://localhost:8080",
		ModelName:        "test-model",
		HasMergeConflict: true,
		CurrentThreadID:  "thread-1",
	}
	te.store.CreateTask(task)

	// Create task branch with a change
	testgit.Git(t, repoDir, "checkout", "-b", "task/merge-commit")
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("task version\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "task edit")
	testgit.Git(t, repoDir, "checkout", "master")

	// Create worktree
	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", wtPath, "task/merge-commit")
	testgit.Git(t, wtPath, "checkout", "task/merge-commit")

	// Conflicting change on master
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("master version\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "master edit")

	// Simulate sync-merge conflict
	unlockStash14 := te.worktreeMgr.LockProjectStash(projectID)
	_ = te.worktreeMgr.GitRunInDir(wtPath, "stash", "push", "-u", "-m", "kanban sync-merge stash")
	unlockStash14()
	testgit.Git(t, wtPath, "checkout", "-f", "task/merge-commit")
	runGitCmd(wtPath, "merge", "master", "--no-edit")

	// Resolve conflict
	os.WriteFile(filepath.Join(wtPath, "shared.txt"), []byte("resolved\n"), 0o644)

	// Commit resolution
	conflictedFiles := GetResolvedConflictedFiles(wtPath)
	if len(conflictedFiles) > 0 {
		addArgs := append([]string{"add"}, conflictedFiles...)
		_, _ = git.Run(wtPath, addArgs...)
	}
	_, _ = git.Run(wtPath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...)

	// Verify the resolution commit is a merge commit (has 2 parents)
	cmd := runGitCmd(wtPath, "cat-file", "-p", "HEAD")
	if !strings.Contains(cmd.output, "parent") {
		t.Fatalf("expected merge commit with parents, got:\n%s", cmd.output)
	}

	// Count parents
	parentCount := strings.Count(cmd.output, "parent ")
	if parentCount != 2 {
		t.Errorf("expected 2 parents (merge commit), got %d", parentCount)
	}
}

// TestCommitConflictResolution_DoesNotSweepUntrackedFiles: `add -u` path skips untracked files.
func TestCommitConflictResolution_DoesNotSweepUntrackedFiles(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "no-sweep-task"
	task := &types.Task{
		ID:               taskID,
		ProjectID:        projectID,
		Title:            "No Sweep Test",
		Prompt:           "resolve conflicts",
		Status:           types.StatusInProgress,
		Branch:           "task/no-sweep",
		BaseRef:          "master",
		LlamaServerURL:   "http://localhost:8080",
		ModelName:        "test-model",
		HasMergeConflict: true,
		AutoMerge:        true,
		CurrentThreadID:  "thread-1",
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	// Task branch with a committed change.
	testgit.Git(t, repoDir, "checkout", "-b", "task/no-sweep")
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("task change\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "task edit")
	testgit.Git(t, repoDir, "checkout", "master")

	// Worktree on the task branch, in a final-merge conflict state (no MERGE_HEAD).
	wtPath, err := te.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", wtPath, "task/no-sweep")
	testgit.Git(t, wtPath, "checkout", "task/no-sweep")
	if isMergeInProgress(wtPath) {
		t.Fatal("precondition: no merge should be in progress")
	}

	// The LLM's resolution edit (tracked, unstaged) plus a user's untracked file.
	os.WriteFile(filepath.Join(wtPath, "shared.txt"), []byte("resolved\n"), 0o644)
	os.WriteFile(filepath.Join(wtPath, "untracked.txt"), []byte("unstaged content\n"), 0o644)

	res := te.merge.commitConflictResolution(projectID, taskID, wtPath, task)
	if !res.committed {
		t.Fatal("commitConflictResolution should succeed")
	}

	// The resolution edit must be committed and merged into the project branch.
	if out := runGitCmd(repoDir, "show", "master:shared.txt").output; strings.TrimSpace(out) != "resolved" {
		t.Errorf("master shared.txt should hold the resolution, got %q", out)
	}
	// The untracked file must NOT be committed or merged into the project.
	if out := runGitCmd(repoDir, "log", "--all", "--oneline", "--", "untracked.txt").output; strings.TrimSpace(out) != "" {
		t.Errorf("untracked.txt must not be committed, but it is in history: %s", out)
	}
	if res := runGitCmd(repoDir, "cat-file", "-e", "master:untracked.txt"); res.exitCode == 0 {
		t.Error("untracked.txt must not be on the project's master branch")
	}
	// It must remain in the worktree, still untracked.
	if _, err := os.Stat(filepath.Join(wtPath, "untracked.txt")); err != nil {
		t.Error("untracked.txt should remain in the worktree")
	}
	if status := runGitCmd(wtPath, "status", "--porcelain").output; !strings.Contains(status, "untracked.txt") {
		t.Errorf("untracked.txt should still be untracked in the worktree, status:\n%s", status)
	}
}

// syncConflict is a task worktree sitting in a sync-merge conflict: the task branch and the target
// branch changed the same line, the target was merged in, and nothing has resolved it yet.
type syncConflict struct {
	te        *TaskExecutor
	repoDir   string
	projectID string
	taskID    string
	wtPath    string
}

// syncResolved resolves the conflict the fixture creates.
const syncResolved = "line 1\ncombined task and master\nline 3\n"

// newSyncConflictFixture builds that state. autoMerge is the setting the tests vary: a click merges
// whatever it says.
func newSyncConflictFixture(t *testing.T, taskID, branch string, autoMerge bool) syncConflict {
	t.Helper()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	task := &types.Task{
		ID:               taskID,
		ProjectID:        projectID,
		Title:            "Sync conflict task",
		Prompt:           "resolve conflicts",
		Status:           types.StatusInProgress,
		Branch:           branch,
		BaseRef:          "master",
		LlamaServerURL:   "http://localhost:8080",
		ModelName:        "test-model",
		HasMergeConflict: true,
		AutoMerge:        autoMerge,
		CurrentThreadID:  "thread-1",
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Task branch changes the middle line.
	testgit.Git(t, repoDir, "checkout", "-b", branch)
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("line 1\ntask change\nline 3\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "task edit")
	testgit.Git(t, repoDir, "checkout", "master")

	wtPath, err := te.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", wtPath, branch)
	testgit.Git(t, wtPath, "checkout", branch)

	// The target branch changes the same line, then gets merged into the task branch.
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("line 1\nmaster change\nline 3\n"), 0o644)
	testgit.Git(t, repoDir, "add", "shared.txt")
	testgit.Git(t, repoDir, "commit", "-m", "master edit")
	runGitCmd(wtPath, "merge", "master", "--no-edit")

	if !isMergeInProgress(wtPath) {
		t.Fatal("fixture: the task worktree should be mid-merge with conflicts")
	}
	if out := runGitCmd(wtPath, "status", "--porcelain").output; !strings.Contains(out, "shared.txt") {
		t.Fatalf("fixture: shared.txt should be conflicted, status:\n%s", out)
	}
	return syncConflict{te: te, repoDir: repoDir, projectID: projectID, taskID: taskID, wtPath: wtPath}
}

// TestCommitConflictResolution_CommitAndDoneClickMergesWithAutoMergeOff: the click means commit,
// merge, Done, so a resolution has to reach the target branch even when auto_merge says no.
func TestCommitConflictResolution_CommitAndDoneClickMergesWithAutoMergeOff(t *testing.T) {
	t.Parallel()
	fx := newSyncConflictFixture(t, "click-merge-task", "task/click-merge", false)
	os.WriteFile(filepath.Join(fx.wtPath, "shared.txt"), []byte(syncResolved), 0o644)
	fx.te.ArmCommitAndDone(fx.taskID)

	task, err := fx.te.store.GetTaskByID(fx.taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	res := fx.te.merge.commitConflictResolution(fx.projectID, fx.taskID, fx.wtPath, task)

	if !res.committed {
		t.Fatal("the resolution was not committed")
	}
	if res.mergeErr != nil {
		t.Fatalf("the click's merge failed: %v", res.mergeErr)
	}
	if res.task.HasMergeConflict {
		t.Fatal("the conflict flag should be cleared")
	}
	if !res.commitDone {
		t.Error("the result does not report the click that produced this conflict")
	}
	if !res.finishesTask() {
		t.Error("finishing the conflict did not finish the click")
	}
	if fx.te.CommitAndDonePending(fx.taskID) {
		t.Error("the click survived the resolution it asked for")
	}
	// The point: the work reached the target branch.
	if out := runGitCmd(fx.repoDir, "show", "master:shared.txt").output; !strings.Contains(out, "combined task and master") {
		t.Errorf("master must hold the resolution, got:\n%s", out)
	}
}

// TestCommitConflictResolution_NoClickNoMergeWithAutoMergeOff: with no click behind the conflict,
// auto_merge alone decides, and false keeps the resolution local and hands the task to the user.
func TestCommitConflictResolution_NoClickNoMergeWithAutoMergeOff(t *testing.T) {
	t.Parallel()
	fx := newSyncConflictFixture(t, "noclick-merge-task", "task/noclick-merge", false)
	os.WriteFile(filepath.Join(fx.wtPath, "shared.txt"), []byte(syncResolved), 0o644)

	task, err := fx.te.store.GetTaskByID(fx.taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	res := fx.te.merge.commitConflictResolution(fx.projectID, fx.taskID, fx.wtPath, task)

	if !res.committed {
		t.Fatal("the resolution was not committed")
	}
	if res.commitDone || res.finishesTask() {
		t.Error("a conflict nobody clicked about reported itself as finishing a click")
	}
	if out := runGitCmd(fx.repoDir, "show", "master:shared.txt").output; strings.Contains(out, "combined task and master") {
		t.Errorf("master must NOT hold the resolution without auto_merge or a click, got:\n%s", out)
	}
}

// TestCommitConflictResolution_StillConflictedKeepsTheClick: a resolution that hits a fresh conflict
// is not over, and spending the click here would leave the turn that does resolve it in Review.
func TestCommitConflictResolution_StillConflictedKeepsTheClick(t *testing.T) {
	t.Parallel()
	fx := newSyncConflictFixture(t, "reconflict-task", "task/reconflict", false)
	os.WriteFile(filepath.Join(fx.wtPath, "shared.txt"), []byte(syncResolved), 0o644)

	// The target branch moves again while the resolution is in flight, so the merge the click asks
	// for conflicts once more.
	os.WriteFile(filepath.Join(fx.repoDir, "shared.txt"), []byte("line 1\nmaster moved again\nline 3\n"), 0o644)
	testgit.Git(t, fx.repoDir, "add", "shared.txt")
	testgit.Git(t, fx.repoDir, "commit", "-m", "master moved")

	fx.te.ArmCommitAndDone(fx.taskID)
	task, err := fx.te.store.GetTaskByID(fx.taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	res := fx.te.merge.commitConflictResolution(fx.projectID, fx.taskID, fx.wtPath, task)

	if !res.committed {
		t.Fatal("fixture: the first resolution should have been committed")
	}
	if !res.task.HasMergeConflict {
		t.Fatal("fixture: the merge should have re-conflicted against the moved target branch")
	}
	if res.finishesTask() {
		t.Error("a still-conflicted task finished")
	}
	if !fx.te.CommitAndDonePending(fx.taskID) {
		t.Error("a resolution that is not over spent the click")
	}

	// Resolve the new conflict and run it again: the click still completes.
	os.WriteFile(filepath.Join(fx.wtPath, "shared.txt"), []byte(syncResolved), 0o644)
	task, err = fx.te.store.GetTaskByID(fx.taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	res = fx.te.merge.commitConflictResolution(fx.projectID, fx.taskID, fx.wtPath, task)
	if !res.finishesTask() {
		t.Errorf("the second resolution did not finish the click: committed=%v mergeErr=%v conflicted=%v",
			res.committed, res.mergeErr, res.task.HasMergeConflict)
	}
	if fx.te.CommitAndDonePending(fx.taskID) {
		t.Error("the click survived the resolution that finished it")
	}
	if out := runGitCmd(fx.repoDir, "show", "master:shared.txt").output; !strings.Contains(out, "combined task and master") {
		t.Errorf("master must hold the resolution, got:\n%s", out)
	}
}

// gitCmdResult holds the result of a git command.
type gitCmdResult struct {
	output   string
	exitCode int
}

// runGitCmd runs a git command and returns the result.
func runGitCmd(dir string, args ...string) gitCmdResult {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return gitCmdResult{
		output:   string(out),
		exitCode: exitCode,
	}
}
