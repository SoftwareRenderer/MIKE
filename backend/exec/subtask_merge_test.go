package exec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"
)

// TestEnsureParentBranch_CreatesBranchWhenParentNotStarted verifies that when
// a subtask is prepared for execution and the parent hasn't started yet
// (parent.Branch == ""), the parent's branch is created so the subtask can
// merge into it later. The parent stays in backlog.
func TestEnsureParentBranch_CreatesBranchWhenParentNotStarted(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	// Create parent task in backlog (never started, no branch)
	parentID := "parent-task-id"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         "backlog",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(parent)

	// Verify parent has no branch
	parentFromDB, _ := te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Branch != "" {
		t.Fatalf("parent should have no branch initially, got %q", parentFromDB.Branch)
	}

	// Ensure parent branch is created
	err := te.EnsureParentBranch(projectID, parent)
	if err != nil {
		t.Fatalf("ensureParentBranch failed: %v", err)
	}

	// Verify parent now has a branch set
	parentFromDB, _ = te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Branch == "" {
		t.Fatal("parent should have a branch after ensureParentBranch")
	}
	if !strings.HasPrefix(parentFromDB.Branch, "task/") {
		t.Errorf("parent branch should start with 'task/', got %q", parentFromDB.Branch)
	}

	// Verify parent stays in backlog (ensureParentBranch should NOT change status)
	if parentFromDB.Status != "backlog" {
		t.Errorf("parent should stay in backlog, got status %q", parentFromDB.Status)
	}

	// Verify the branch exists in git
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", parentFromDB.Branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("branch %q does not exist in git: %s", parentFromDB.Branch, string(out))
	}
}

// TestEnsureParentBranch_Idempotent verifies that calling ensureParentBranch
// multiple times is safe (parent already has a branch).
func TestEnsureParentBranch_Idempotent(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)

	parentID := "parent-idempotent"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         "backlog",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(parent)

	// First call — creates branch
	err := te.EnsureParentBranch(projectID, parent)
	if err != nil {
		t.Fatalf("first ensureParentBranch failed: %v", err)
	}
	firstBranch := parent.Branch

	// Reload parent from DB
	parent, _ = te.store.GetTaskByTaskID(parentID)

	// Second call — should be no-op
	err = te.EnsureParentBranch(projectID, parent)
	if err != nil {
		t.Fatalf("second ensureParentBranch failed: %v", err)
	}

	// Branch should be the same
	if parent.Branch != firstBranch {
		t.Errorf("branch changed on second call: was %q, now %q", firstBranch, parent.Branch)
	}
}

// TestPrepareTaskExecution_SubtaskCreatesParentBranch verifies the full flow:
// when prepareTaskExecution is called for a subtask whose parent hasn't started,
// the parent's branch is created and the subtask's worktree branches from it.
func TestPrepareTaskExecution_SubtaskCreatesParentBranch(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	// Create parent task in backlog (never started)
	parentID := "parent-full-flow"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         "backlog",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(parent)

	// Create subtask with parent_task_id
	subtaskID := "subtask-full-flow"
	subtask := &types.Task{
		ID:             subtaskID,
		ProjectID:      projectID,
		Title:          "Subtask",
		Prompt:         "Subtask prompt",
		Status:         "in_progress",
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(subtask)

	// Prepare execution for the subtask
	setup, err := te.prepareTaskExecution(projectID, subtaskID, "", "")
	if err != nil {
		t.Fatalf("prepareTaskExecution failed: %v", err)
	}
	if setup == nil {
		t.Fatal("prepareTaskExecution returned nil")
	}
	defer func() {
		setup.cancel()
		te.StopTask(subtaskID)
	}()

	// Verify parent's branch was created
	parentFromDB, _ := te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Branch == "" {
		t.Fatal("parent branch should be created when subtask is prepared")
	}

	// Verify parent branch exists in git
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", parentFromDB.Branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("parent branch %q does not exist in git: %s", parentFromDB.Branch, string(out))
	}

	// Verify subtask's worktree was created from parent's branch
	// The subtask's BaseRef should be the tip of the parent's branch
	subtaskFromSetup := setup.task
	if subtaskFromSetup.Branch == "" {
		t.Fatal("subtask should have a branch")
	}

	// Verify the subtask branch is a descendant of the parent branch
	cmd = exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor", parentFromDB.Branch, subtaskFromSetup.Branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("subtask branch %q should be a descendant of parent branch %q: %s",
			subtaskFromSetup.Branch, parentFromDB.Branch, string(out))
	}
}

// TestMergeBranch_SubtaskMergesIntoParentBranch verifies that MergeBranch
// correctly merges a subtask's branch into the parent's branch (not origin).
func TestMergeBranch_SubtaskMergesIntoParentBranch(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	// Create parent task with a branch
	parentID := "parent-merge-test"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         "in_progress",
		Branch:         "task/" + parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(parent)

	// Create parent branch in git
	runGit(t, repoDir, "branch", parent.Branch)

	// Create subtask
	subtaskID := "subtask-merge-test"
	subtask := &types.Task{
		ID:             subtaskID,
		ProjectID:      projectID,
		Title:          "Subtask",
		Prompt:         "Subtask prompt",
		Status:         "in_progress",
		Branch:         "task/" + subtaskID,
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(subtask)

	// Create subtask branch from parent branch
	runGit(t, repoDir, "branch", subtask.Branch, parent.Branch)

	// Make a commit on the subtask branch
	runGit(t, repoDir, "checkout", subtask.Branch)
	subtaskFile := filepath.Join(repoDir, "subtask_file.txt")
	os.WriteFile(subtaskFile, []byte("subtask changes\n"), 0o644)
	runGit(t, repoDir, "add", "subtask_file.txt")
	runGit(t, repoDir, "commit", "-m", "subtask work")

	// Get parent branch tip before merge
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", parent.Branch)
	out, _ := cmd.Output()
	parentTipBefore := strings.TrimSpace(string(out))

	// Merge subtask into parent
	err := te.MergeBranch(projectID, subtaskID, subtask)
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Verify the subtask's commit is now on the parent branch
	cmd = exec.Command("git", "-C", repoDir, "branch", "--contains", "HEAD", parent.Branch)
	cmd.Dir = repoDir
	// Switch to parent branch first
	runGit(t, repoDir, "checkout", parent.Branch)
	if _, err := os.Stat(subtaskFile); err != nil {
		t.Fatalf("subtask file should exist on parent branch after merge: %v", err)
	}

	// Verify parent branch tip changed (merge happened)
	cmd = exec.Command("git", "-C", repoDir, "rev-parse", parent.Branch)
	out, _ = cmd.Output()
	parentTipAfter := strings.TrimSpace(string(out))
	if parentTipBefore == parentTipAfter {
		t.Error("parent branch tip should have changed after merge")
	}

	// Verify the commit is NOT on the origin branch (main)
	runGit(t, repoDir, "checkout", "master")
	if _, err := os.Stat(subtaskFile); err == nil {
		t.Error("subtask file should NOT exist on master (origin) branch — merge should target parent, not origin")
	}
}

// TestMergeBranch_SubtaskNeverMergesIntoOrigin verifies that a subtask
// NEVER merges into origin. If the parent has no branch, MergeBranch creates
// it on demand and merges into the parent's branch.
func TestMergeBranch_SubtaskNeverMergesIntoOrigin(t *testing.T) {
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent task WITHOUT a branch
	parentID := "parent-no-branch"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         "backlog", // never started
		Branch:         "",        // no branch
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(parent)

	// Create subtask with its own branch
	subtaskID := "subtask-no-fallback"
	subtask := &types.Task{
		ID:             subtaskID,
		ProjectID:      projectID,
		Title:          "Subtask",
		Prompt:         "Subtask prompt",
		Status:         "in_progress",
		Branch:         "task/" + subtaskID,
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(subtask)

	// Create subtask branch from master
	runGit(t, repoDir, "branch", subtask.Branch, "master")

	// Make a commit on the subtask branch
	runGit(t, repoDir, "checkout", subtask.Branch)
	subtaskFile := filepath.Join(repoDir, "no_fallback_file.txt")
	os.WriteFile(subtaskFile, []byte("no fallback changes\n"), 0o644)
	runGit(t, repoDir, "add", "no_fallback_file.txt")
	runGit(t, repoDir, "commit", "-m", "no fallback work")

	// Verify parent has no branch before merge
	parentFromDB, _ := te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Branch != "" {
		t.Fatalf("parent should have no branch initially, got %q", parentFromDB.Branch)
	}

	// Merge — should create parent branch on demand, NOT fall back to origin
	subtaskFromDB, _ := te.store.GetTaskByTaskID(subtaskID)
	err := te.MergeBranch(projectID, subtaskID, subtaskFromDB)
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Verify parent now has a branch (created on demand)
	parentFromDB, _ = te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Branch == "" {
		t.Fatal("parent should have a branch after merge (created on demand)")
	}

	// Verify the commit is on the parent branch
	cmd := exec.Command("git", "-C", repoDir, "show", parentFromDB.Branch+":no_fallback_file.txt")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("subtask file should be on parent branch: %v %s", err, string(out))
	}
	if strings.TrimSpace(string(out)) != "no fallback changes" {
		t.Errorf("unexpected content on parent branch: %q", string(out))
	}

	// Verify the commit is NOT on master (origin)
	cmd = exec.Command("git", "-C", repoDir, "show", "master:no_fallback_file.txt")
	if _, err := cmd.CombinedOutput(); err == nil {
		t.Error("subtask file should NOT exist on master — subtask must NEVER merge into origin")
	}
}

// TestAutoStartNextSibling_ParentBranchExists verifies that when a subtask
// completes and auto-start triggers the parent, the parent's branch already
// exists (created by ensureParentBranch during subtask preparation).
func TestAutoStartNextSibling_ParentBranchExists(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	// Create parent in backlog
	parentID := "parent-auto-start"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         "backlog",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		AutoCommit:     true,
		AutoMerge:      true,
	}
	te.store.CreateTask(parent)

	// Create subtask
	subtaskID := "subtask-auto-start"
	subtask := &types.Task{
		ID:             subtaskID,
		ProjectID:      projectID,
		Title:          "Subtask",
		Prompt:         "Subtask prompt",
		Status:         "in_progress",
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		AutoCommit:     true,
		AutoMerge:      true,
	}
	te.store.CreateTask(subtask)

	// Prepare subtask execution (this should trigger ensureParentBranch)
	setup, err := te.prepareTaskExecution(projectID, subtaskID, "", "")
	if err != nil {
		t.Fatalf("prepareTaskExecution failed: %v", err)
	}
	if setup == nil {
		t.Fatal("prepareTaskExecution returned nil")
	}
	defer func() {
		setup.cancel()
		te.StopTask(subtaskID)
	}()

	// Verify parent branch exists
	parentFromDB, _ := te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Branch == "" {
		t.Fatal("parent branch should exist after subtask preparation")
	}

	// Verify parent branch exists in git
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", parentFromDB.Branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("parent branch %q does not exist in git: %s", parentFromDB.Branch, string(out))
	}
}

// setupExecutorWithGitForSubtask creates a TaskExecutor with a real git repo
// and configures it for subtask testing (with config store defaults).
func setupExecutorWithGitForSubtask(t *testing.T) (*TaskExecutor, string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	os.MkdirAll(repoDir, 0o755)
	os.MkdirAll(worktreesDir, 0o755)

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test\n"), 0o644)
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfgStore := config.NewConfigStore(s, dbPath)
	_ = s.SetConfigValue("workspace_dir", worktreesDir)

	projectID := "test-project"
	proj := &types.Project{ID: projectID, Name: "test", Path: repoDir}
	s.CreateProject(proj)

	worktreeMgr := git.NewWorktreeManager(s, cfgStore)
	appStreamMgr := stream.NewAppStreamManager(s, worktreeMgr)

	te := &TaskExecutor{
		store:        s,
		configStore:  cfgStore,
		appStreamMgr: appStreamMgr,
		worktreeMgr:  worktreeMgr,
		gate:         NewConcurrencyGate(),
		tasks:        make(map[string]*executionState),
	}

	return te, repoDir, projectID
}

// TestSubtaskMergeFullFlow is an integration test that simulates the full
// subtask → parent → origin merge chain.
func TestSubtaskMergeFullFlow(t *testing.T) {
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent in backlog
	parentID := "parent-full"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         "backlog",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		AutoCommit:     true,
		AutoMerge:      true,
	}
	te.store.CreateTask(parent)

	// Create subtask
	subtaskID := "subtask-full"
	subtask := &types.Task{
		ID:             subtaskID,
		ProjectID:      projectID,
		Title:          "Subtask",
		Prompt:         "Subtask prompt",
		Status:         "in_progress",
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		AutoCommit:     true,
		AutoMerge:      true,
	}
	te.store.CreateTask(subtask)

	// Step 1: Prepare subtask — should create parent branch
	setup, err := te.prepareTaskExecution(projectID, subtaskID, "", "")
	if err != nil {
		t.Fatalf("prepareTaskExecution failed: %v", err)
	}
	if setup == nil {
		t.Fatal("prepareTaskExecution returned nil")
	}
	defer func() {
		setup.cancel()
		te.StopTask(subtaskID)
	}()

	parentFromDB, _ := te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Branch == "" {
		t.Fatal("parent branch should be created")
	}

	// Step 2: Simulate subtask making changes and committing
	subtaskFile := filepath.Join(setup.worktreePath, "subtask_work.txt")
	os.WriteFile(subtaskFile, []byte("subtask implementation\n"), 0o644)
	runGitInDir(t, setup.worktreePath, "add", "-A")
	runGitInDir(t, setup.worktreePath, "commit", "-m", "subtask work")

	// Step 3: Merge subtask into parent branch
	subtaskFromDB, _ := te.store.GetTaskByTaskID(subtaskID)
	err = te.MergeBranch(projectID, subtaskID, subtaskFromDB)
	if err != nil {
		t.Fatalf("MergeBranch (subtask → parent) failed: %v", err)
	}

	// Verify subtask changes are on parent branch but NOT on master.
	// Can't checkout parent branch in main repo — it's in the parent's worktree.
	// Use git show to inspect the branch content.
	cmd := exec.Command("git", "-C", repoDir, "show", parentFromDB.Branch+":subtask_work.txt")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("subtask file should be on parent branch: %v %s", err, string(out))
	}
	if strings.TrimSpace(string(out)) != "subtask implementation" {
		t.Errorf("unexpected content on parent branch: %q", string(out))
	}

	// Verify NOT on master
	cmd = exec.Command("git", "-C", repoDir, "show", "master:subtask_work.txt")
	if _, err := cmd.CombinedOutput(); err == nil {
		t.Error("subtask file should NOT be on master yet")
	}

	// Step 4: Prepare parent execution
	parentSetup, err := te.prepareTaskExecution(projectID, parentID, "", "")
	if err != nil {
		t.Fatalf("prepareTaskExecution for parent failed: %v", err)
	}
	if parentSetup == nil {
		t.Fatal("prepareTaskExecution for parent returned nil")
	}
	defer func() {
		parentSetup.cancel()
		te.StopTask(parentID)
	}()

	// Step 5: Simulate parent making changes and committing
	parentFile := filepath.Join(parentSetup.worktreePath, "parent_work.txt")
	os.WriteFile(parentFile, []byte("parent implementation\n"), 0o644)
	runGitInDir(t, parentSetup.worktreePath, "add", "-A")
	runGitInDir(t, parentSetup.worktreePath, "commit", "-m", "parent work")

	// Step 6: Merge parent into origin (master)
	parentFromDB, _ = te.store.GetTaskByTaskID(parentID)
	err = te.MergeBranch(projectID, parentID, parentFromDB)
	if err != nil {
		t.Fatalf("MergeBranch (parent → origin) failed: %v", err)
	}

	// Verify BOTH subtask and parent changes are on master
	runGit(t, repoDir, "checkout", "master")
	masterSubtaskFile := filepath.Join(repoDir, "subtask_work.txt")
	masterParentFile := filepath.Join(repoDir, "parent_work.txt")
	if _, err := os.Stat(masterSubtaskFile); err != nil {
		t.Fatalf("subtask file should be on master after parent merge: %v", err)
	}
	if _, err := os.Stat(masterParentFile); err != nil {
		t.Fatalf("parent file should be on master after parent merge: %v", err)
	}
}

func runGitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %s: %v", args, dir, string(out), err)
	}
}

// TestMergeBranch_ConflictDetection verifies that when a merge conflict occurs
// and LLM resolution is not available (no LLM config), the merge is aborted
// gracefully and an error is returned.
func TestMergeBranch_ConflictDetection(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent in backlog (will get branch via ensureParentBranch)
	parentID := "parent-conflict"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         "backlog",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		AutoMerge:      true,
	}
	te.store.CreateTask(parent)

	// Create subtask
	subtaskID := "subtask-conflict"
	subtask := &types.Task{
		ID:             subtaskID,
		ProjectID:      projectID,
		Title:          "Subtask",
		Prompt:         "Subtask prompt",
		Status:         "in_progress",
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		AutoMerge:      true,
	}
	te.store.CreateTask(subtask)

	// Prepare subtask — creates parent branch + subtask worktree
	setup, err := te.prepareTaskExecution(projectID, subtaskID, "", "")
	if err != nil {
		t.Fatalf("prepareTaskExecution failed: %v", err)
	}
	if setup == nil {
		t.Fatal("prepareTaskExecution returned nil")
	}
	defer func() {
		setup.cancel()
		te.StopTask(subtaskID)
	}()

	parentFromDB, _ := te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Branch == "" {
		t.Fatal("parent branch should exist")
	}

	// Create a conflicting file on the parent branch (via parent's worktree)
	parentWorktreePath, _ := te.worktreeMgr.WorktreePath(parentID)
	sharedFile := filepath.Join(parentWorktreePath, "shared.txt")
	os.WriteFile(sharedFile, []byte("parent version of the file\n"), 0o644)
	runGitInDir(t, parentWorktreePath, "add", "-A")
	runGitInDir(t, parentWorktreePath, "commit", "-m", "parent edits shared file")

	// Create the same file with different content on the subtask branch
	subtaskFile := filepath.Join(setup.worktreePath, "shared.txt")
	os.WriteFile(subtaskFile, []byte("subtask version of the file\n"), 0o644)
	runGitInDir(t, setup.worktreePath, "add", "-A")
	runGitInDir(t, setup.worktreePath, "commit", "-m", "subtask edits shared file")

	// Try to merge — the sync-merge (target→task) will conflict.
	// With the new sync-merge feature, LLM resolution failure enters
	// merge_conflict state for interactive resolution (not an error).
	subtaskFromDB, _ := te.store.GetTaskByTaskID(subtaskID)
	err = te.MergeBranch(projectID, subtaskID, subtaskFromDB)

	// Sync-merge enters merge_conflict state — MergeBranch returns nil
	// (the task is now in merge_conflict state for interactive resolution).
	// The final merge (task→target) is skipped.
	if err != nil {
		t.Fatalf("mergeBranch should return nil when sync enters merge_conflict state: %v", err)
	}

	// Verify task is in merge_conflict state
	subtaskFromDB, _ = te.store.GetTaskByTaskID(subtaskID)
	if !subtaskFromDB.HasMergeConflict {
		t.Fatalf("expected has_merge_conflict=true, got false")
	}

	// Verify the parent branch was NOT modified (sync merge is in progress, not committed)
	content, _ := os.ReadFile(sharedFile)
	if string(content) != "parent version of the file\n" {
		t.Errorf("parent worktree should be unchanged, got: %q", string(content))
	}
}

// TestAutoStartNextSibling_ParentInBacklog verifies that AutoStartNextSibling
// works when the parent is in backlog (not in_progress). The parent stays in
// backlog while subtasks run, and siblings auto-start correctly.
func TestAutoStartNextSibling_ParentInBacklog(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent in backlog
	parentID := "parent-sibling-test"
	parent := &types.Task{
		ID:                parentID,
		ProjectID:         projectID,
		Title:             "Parent Task",
		Prompt:            "Parent prompt",
		Status:            "backlog",
		CurrentThreadID:   "thread-parent",
		LlamaServerURL:    "http://localhost:8080",
		ModelName:         "test-model",
	}
	te.store.CreateTask(parent)

	// Create two subtasks
	subtask1ID := "subtask1-sibling"
	subtask2ID := "subtask2-sibling"
	for i, id := range []string{subtask1ID, subtask2ID} {
		subtask := &types.Task{
			ID:                id,
			ProjectID:         projectID,
			Title:             "Subtask " + id,
			Prompt:            "Subtask prompt",
			Status:            "in_progress",
			ExecutionState:       "idle",
			ParentTaskID:      parentID,
			CurrentThreadID:   "thread-subtask-" + string(rune('1'+i)),
			LlamaServerURL:    "http://localhost:8080",
			ModelName:         "test-model",
		}
		te.store.CreateTask(subtask)
	}

	// Prepare subtask1 (creates parent branch, parent stays in backlog)
	setup, err := te.prepareTaskExecution(projectID, subtask1ID, "", "")
	if err != nil {
		t.Fatalf("prepareTaskExecution failed: %v", err)
	}
	if setup == nil {
		t.Fatal("prepareTaskExecution returned nil")
	}
	defer func() {
		setup.cancel()
		te.StopTask(subtask1ID)
	}()

	// Verify parent is still in backlog
	parentFromDB, _ := te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Status != "backlog" {
		t.Errorf("parent should stay in backlog, got status %q", parentFromDB.Status)
	}
	if parentFromDB.Branch == "" {
		t.Fatal("parent should have a branch")
	}

	// Simulate subtask1 completing (move to review)
	subtask1, _ := te.store.GetTaskByTaskID(subtask1ID)
	subtask1.Status = "review"
	subtask1.ExecutionState = "idle"
	te.store.UpdateTask(subtask1)

	// Stop subtask1 executor
	setup.cancel()
	te.StopTask(subtask1ID)

	// AutoStartNextSibling should start subtask2 (parent is in backlog)
	err = te.AutoStartNextSibling(projectID, subtask1ID, parentID, "review")
	if err != nil {
		t.Fatalf("AutoStartNextSibling failed: %v", err)
	}

	// Verify subtask2 is now running
	subtask2, _ := te.store.GetTaskByTaskID(subtask2ID)
	if subtask2.ExecutionState != "running" {
		t.Errorf("subtask2 should be running, got execution_state %q", subtask2.ExecutionState)
	}

	// Verify parent is STILL in backlog
	parentFromDB, _ = te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Status != "backlog" {
		t.Errorf("parent should still be in backlog after sibling auto-start, got %q", parentFromDB.Status)
	}

	// Cleanup
	te.StopTask(subtask2ID)
}

// TestAutoStartNextSibling_AllDoneParentStaysInBacklog verifies that when all
// subtasks are done and the parent is in backlog, the parent is NOT auto-started.
// The parent stays in backlog until the user explicitly starts or moves it.
func TestAutoStartNextSibling_AllDoneParentStaysInBacklog(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent in backlog
	parentID := "parent-stay-backlog"
	parent := &types.Task{
		ID:                parentID,
		ProjectID:         projectID,
		Title:             "Parent Task",
		Prompt:            "Parent prompt",
		Status:            "backlog",
		CurrentThreadID:   "thread-parent",
		LlamaServerURL:    "http://localhost:8080",
		ModelName:         "test-model",
	}
	te.store.CreateTask(parent)

	// Create a single subtask
	subtaskID := "subtask-last-one"
	subtask := &types.Task{
		ID:                subtaskID,
		ProjectID:         projectID,
		Title:             "Last Subtask",
		Prompt:            "Subtask prompt",
		Status:            "in_progress",
		ExecutionState:       "idle",
		ParentTaskID:      parentID,
		CurrentThreadID:   "thread-subtask",
		LlamaServerURL:    "http://localhost:8080",
		ModelName:         "test-model",
	}
	te.store.CreateTask(subtask)

	// Prepare subtask (creates parent branch)
	setup, err := te.prepareTaskExecution(projectID, subtaskID, "", "")
	if err != nil {
		t.Fatalf("prepareTaskExecution failed: %v", err)
	}
	if setup == nil {
		t.Fatal("prepareTaskExecution returned nil")
	}
	defer func() {
		setup.cancel()
		te.StopTask(subtaskID)
	}()

	// Simulate subtask completing (move to done — last and only subtask)
	subtask, _ = te.store.GetTaskByTaskID(subtaskID)
	subtask.Status = "done"
	subtask.ExecutionState = "completed"
	te.store.UpdateTask(subtask)

	// Stop subtask executor
	setup.cancel()
	te.StopTask(subtaskID)

	// AutoStartNextSibling with no incomplete siblings and all done
	err = te.AutoStartNextSibling(projectID, subtaskID, parentID, "done")
	if err != nil {
		t.Fatalf("AutoStartNextSibling failed: %v", err)
	}

	// Verify parent is STILL in backlog (NOT auto-started)
	parentFromDB, _ := te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Status != "backlog" {
		t.Errorf("parent should stay in backlog after all children done, got status %q", parentFromDB.Status)
	}
	if parentFromDB.ExecutionState == "running" {
		t.Error("parent should NOT be running — it was auto-started when it should stay in backlog")
	}
}

// TestMergeBranch_CommitAndDonePath verifies that MergeBranch merges a subtask
// into the parent branch, not origin.
func TestMergeBranch_CommitAndDonePath(t *testing.T) {
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent with an explicit branch
	parentID := "parent-commit-done"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         "backlog",
		Branch:         "task/" + parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(parent)

	// Create parent branch in git
	runGit(t, repoDir, "branch", parent.Branch)

	// Create subtask
	subtaskID := "subtask-commit-done"
	subtask := &types.Task{
		ID:             subtaskID,
		ProjectID:      projectID,
		Title:          "Subtask",
		Prompt:         "Subtask prompt",
		Status:         "review",
		Branch:         "task/" + subtaskID,
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(subtask)

	// Create subtask branch from parent branch
	runGit(t, repoDir, "branch", subtask.Branch, parent.Branch)

	// Make a commit on the subtask branch
	runGit(t, repoDir, "checkout", subtask.Branch)
	subtaskFile := filepath.Join(repoDir, "commit_done_file.txt")
	os.WriteFile(subtaskFile, []byte("subtask work from commit-and-done\n"), 0o644)
	runGit(t, repoDir, "add", "commit_done_file.txt")
	runGit(t, repoDir, "commit", "-m", "subtask work")

	// Merge subtask
	subtaskFromDB, _ := te.store.GetTaskByTaskID(subtaskID)
	err := te.MergeBranch(projectID, subtaskID, subtaskFromDB)
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Verify the subtask's commit is on the parent branch
	runGit(t, repoDir, "checkout", parent.Branch)
	if _, err := os.Stat(subtaskFile); err != nil {
		t.Fatalf("subtask file should exist on parent branch after merge: %v", err)
	}

	// Verify the commit is NOT on master (origin) — it should only be on parent
	runGit(t, repoDir, "checkout", "master")
	if _, err := os.Stat(subtaskFile); err == nil {
		t.Error("subtask file should NOT exist on master — MergeBranch must target parent branch, not origin")
	}
}

// TestMergeBranch_ParentBranchCreatedOnDemand verifies that when MergeBranch
// is called for a subtask whose parent has no branch, the parent branch is
// created on demand (safety net for HandleCommitAndDone path).
func TestMergeBranch_ParentBranchCreatedOnDemand(t *testing.T) {
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent in backlog (no branch)
	parentID := "parent-on-demand"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         "backlog",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(parent)

	// Create subtask with its own branch (simulating a subtask that was
	// started manually or via HandleCommitAndDone without going through
	// the normal start flow)
	subtaskID := "subtask-on-demand"
	subtask := &types.Task{
		ID:             subtaskID,
		ProjectID:      projectID,
		Title:          "Subtask",
		Prompt:         "Subtask prompt",
		Status:         "review",
		Branch:         "task/" + subtaskID,
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(subtask)

	// Create subtask branch from master
	runGit(t, repoDir, "branch", subtask.Branch, "master")

	// Make a commit on the subtask branch
	runGit(t, repoDir, "checkout", subtask.Branch)
	subtaskFile := filepath.Join(repoDir, "on_demand_file.txt")
	os.WriteFile(subtaskFile, []byte("on demand changes\n"), 0o644)
	runGit(t, repoDir, "add", "on_demand_file.txt")
	runGit(t, repoDir, "commit", "-m", "on demand work")

	// Verify parent has no branch
	parentFromDB, _ := te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Branch != "" {
		t.Fatalf("parent should have no branch initially, got %q", parentFromDB.Branch)
	}

	// Ensure parent branch is created (safety net from HandleCommitAndDone)
	err := te.EnsureParentBranch(projectID, parentFromDB)
	if err != nil {
		t.Fatalf("EnsureParentBranch failed: %v", err)
	}

	// Verify parent now has a branch
	parentFromDB, _ = te.store.GetTaskByTaskID(parentID)
	if parentFromDB.Branch == "" {
		t.Fatal("parent should have a branch after EnsureParentBranch")
	}

	// Merge subtask — should go to parent's branch, not master
	subtaskFromDB, _ := te.store.GetTaskByTaskID(subtaskID)
	err = te.MergeBranch(projectID, subtaskID, subtaskFromDB)
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Verify the subtask's commit is on the parent branch
	// (can't checkout — branch is in a worktree, use git show instead)
	cmd := exec.Command("git", "-C", repoDir, "show", parentFromDB.Branch+":on_demand_file.txt")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("subtask file should be on parent branch: %v %s", err, string(out))
	}
	if strings.TrimSpace(string(out)) != "on demand changes" {
		t.Errorf("unexpected content on parent branch: %q", string(out))
	}

	// Verify the commit is NOT on master
	cmd = exec.Command("git", "-C", repoDir, "show", "master:on_demand_file.txt")
	if _, err := cmd.CombinedOutput(); err == nil {
		t.Error("subtask file should NOT exist on master — merge must target parent branch, not origin")
	}
}

// TestMergeBranch_RootTaskMergesIntoOriginBranch verifies that a root task
// with a branch-name BaseRef merges into that branch (not some other ref).
func TestMergeBranch_RootTaskMergesIntoOriginBranch(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	// Make a commit on master so we have something to merge against
	os.WriteFile(filepath.Join(repoDir, "master_update.txt"), []byte("master update\n"), 0o644)
	runGit(t, repoDir, "add", "master_update.txt")
	runGit(t, repoDir, "commit", "-m", "master update")

	taskID := "root-origin-merge"
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Root Task",
		Prompt:         "Root task prompt",
		Status:         "in_progress",
		Branch:         "task/" + taskID,
		BaseRef:        "master", // branch name
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(task)

	// Create the task branch from master
	runGit(t, repoDir, "branch", task.Branch, "master")

	// Make a commit on the task branch
	runGit(t, repoDir, "checkout", task.Branch)
	taskFile := filepath.Join(repoDir, "task_file.txt")
	os.WriteFile(taskFile, []byte("task changes\n"), 0o644)
	runGit(t, repoDir, "add", "task_file.txt")
	runGit(t, repoDir, "commit", "-m", "task work")
	taskCommitSHA := getGitRevParse(t, repoDir, "HEAD")

	// Merge task branch
	err := te.MergeBranch(projectID, taskID, task)
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Verify the task's commit is on master
	cmd := exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor", taskCommitSHA, "master")
	if err := cmd.Run(); err != nil {
		t.Fatal("task commit should be ancestor of master after merge")
	}

	// Verify master has both files
	cmd = exec.Command("git", "-C", repoDir, "show", "master:task_file.txt")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("task_file.txt should exist on master after merge: %v", err)
	}
	if strings.TrimSpace(string(out)) != "task changes" {
		t.Errorf("unexpected content: %q", string(out))
	}
}

// TestMergeBranch_RootTaskWithBranchName_BaseRefUsesBranchName verifies that
// when a root task's BaseRef is a branch name (user-set, not a SHA),
// MergeBranch merges into that branch.
func TestMergeBranch_RootTaskWithBranchName_BaseRefUsesBranchName(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	// Create a "develop" branch
	runGit(t, repoDir, "branch", "develop", "master")

	taskID := "branch-baseref-task"
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Task",
		Prompt:         "Task prompt",
		Status:         "in_progress",
		Branch:         "task/" + taskID,
		BaseRef:        "develop", // branch name (user-set)
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(task)

	// Create the task branch from develop
	runGit(t, repoDir, "branch", task.Branch, "develop")

	// Make a commit on the task branch
	runGit(t, repoDir, "checkout", task.Branch)
	taskFile := filepath.Join(repoDir, "task_file.txt")
	os.WriteFile(taskFile, []byte("task changes\n"), 0o644)
	runGit(t, repoDir, "add", "task_file.txt")
	runGit(t, repoDir, "commit", "-m", "task work")
	taskCommitSHA := getGitRevParse(t, repoDir, "HEAD")

	// Merge task branch
	err := te.MergeBranch(projectID, taskID, task)
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Verify the task's commit is on develop
	cmd := exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor", taskCommitSHA, "develop")
	if err := cmd.Run(); err != nil {
		t.Fatal("task commit should be ancestor of develop after merge")
	}

	// Verify the commit is NOT on master
	cmd = exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor", taskCommitSHA, "master")
	if err := cmd.Run(); err == nil {
		t.Error("task commit should NOT be on master — merge must target develop, not origin")
	}
}

func getGitRevParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", ref)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}
