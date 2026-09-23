package exec

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/config"
	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/types"
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
		Status:         types.StatusBacklog,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(parent)

	// Verify parent has no branch
	parentFromDB, _ := te.store.GetTaskByID(parentID)
	if parentFromDB.Branch != "" {
		t.Fatalf("parent should have no branch initially, got %q", parentFromDB.Branch)
	}

	// Ensure parent branch is created
	err := te.EnsureParentBranch(projectID, parent)
	if err != nil {
		t.Fatalf("ensureParentBranch failed: %v", err)
	}

	// Verify parent now has a branch set
	parentFromDB, _ = te.store.GetTaskByID(parentID)
	if parentFromDB.Branch == "" {
		t.Fatal("parent should have a branch after ensureParentBranch")
	}
	if !strings.HasPrefix(parentFromDB.Branch, "task/") {
		t.Errorf("parent branch should start with 'task/', got %q", parentFromDB.Branch)
	}

	// Verify parent stays in backlog (ensureParentBranch should NOT change status)
	if parentFromDB.Status != types.StatusBacklog {
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
		Status:         types.StatusBacklog,
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
	parent, _ = te.store.GetTaskByID(parentID)

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
		Status:         types.StatusBacklog,
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
		Status:         types.StatusInProgress,
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(subtask)

	// Prepare execution for the subtask
	setup, err := te.prepareTaskExecution(projectID, subtaskID, "", "", true)
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
	parentFromDB, _ := te.store.GetTaskByID(parentID)
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
		Status:         types.StatusInProgress,
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
		Status:         types.StatusInProgress,
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

	// Verify the subtask's commit is now on the parent branch.
	// Merge happens in the parent's worktree, so check there.
	parentWtPath, err := te.worktreeMgr.WorktreePath(parentID)
	if err != nil || parentWtPath == "" {
		// Fallback: check via git (works regardless of worktree)
		cmd = exec.Command("git", "-C", repoDir, "show", parent.Branch+":subtask_file.txt")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("subtask file should exist on parent branch after merge: %v", err)
		}
		_ = out
	} else {
		wtSubtaskFile := filepath.Join(parentWtPath, "subtask_file.txt")
		if _, err := os.Stat(wtSubtaskFile); err != nil {
			t.Fatalf("subtask file should exist in parent worktree after merge: %v", err)
		}
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

// TestMergeBranch_CreatesMissingBaseBranch verifies that a root task with an
// explicitly configured base branch that doesn't exist yet (a custom name
// typed into Edit Task for a separate exploration branch) gets the branch
// created from the project HEAD before the merge — so commit-and-done lands
// the task's work on the new branch instead of failing at checkout.
func TestMergeBranch_CreatesMissingBaseBranch(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("cannot determine main branch: %v", err)
	}
	mainBranch := strings.TrimSpace(string(out))
	if mainBranch == "" || mainBranch == "HEAD" {
		t.Skipf("cannot determine main branch: %q", mainBranch)
	}

	taskID := "root-custom-base"
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Root with custom base",
		Prompt:         "Explore an idea",
		Status:         types.StatusReview,
		Branch:         "task/" + taskID,
		BaseRef:        "explore/idea", // custom name, not yet a branch in the repo
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(task)

	// Task branch with a commit on it; the project checkout stays on main.
	runGit(t, repoDir, "branch", task.Branch)
	runGit(t, repoDir, "checkout", task.Branch)
	taskFile := filepath.Join(repoDir, "idea_file.txt")
	os.WriteFile(taskFile, []byte("idea work\n"), 0o644)
	runGit(t, repoDir, "add", "idea_file.txt")
	runGit(t, repoDir, "commit", "-m", "idea work")
	runGit(t, repoDir, "checkout", mainBranch)

	if out, err := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "explore/idea").CombinedOutput(); err == nil {
		t.Fatalf("precondition failed: explore/idea should not exist yet: %s", out)
	}

	if err := te.MergeBranch(projectID, taskID, task); err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// The base branch now exists and contains the task's commit.
	cmd = exec.Command("git", "-C", repoDir, "show", "explore/idea:idea_file.txt")
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("idea_file.txt should exist on explore/idea after merge: %v (%s)", err, out)
	}
	if strings.TrimSpace(string(out)) != "idea work" {
		t.Errorf("unexpected content on explore/idea: %q", string(out))
	}

	// The base branch was created from the project HEAD (main), not from the
	// task branch: main itself must NOT contain the task's file.
	if out, err := exec.Command("git", "-C", repoDir, "show", mainBranch+":idea_file.txt").CombinedOutput(); err == nil {
		t.Errorf("main branch should NOT contain the task file after merging into the custom base: %s", out)
	}

	// The project's main checkout must be back on the branch it was on before
	// the merge (mergeInto restores it).
	cmd = exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("cannot determine checkout after merge: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != mainBranch {
		t.Errorf("main checkout should be restored to %s, got %q", mainBranch, got)
	}
}

// TestMergeInto_RestoresProjectCheckout verifies that a root-task merge
// restores the project's main checkout to where the user was working —
// branch and uncommitted changes — after merging into the target branch.
func TestMergeInto_RestoresProjectCheckout(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("cannot determine main branch: %v", err)
	}
	mainBranch := strings.TrimSpace(string(out))
	if mainBranch == "" || mainBranch == "HEAD" {
		t.Skipf("cannot determine main branch: %q", mainBranch)
	}

	// The user is working on their own branch with uncommitted changes.
	runGit(t, repoDir, "checkout", "-b", "user-feature")
	localFile := filepath.Join(repoDir, "local_edit.txt")
	os.WriteFile(localFile, []byte("user's in-progress work\n"), 0o644)

	// A task branch (forked from main) with a commit to merge into main. The
	// main checkout stays on user-feature — so the merge runs in the project
	// directory and must restore the checkout afterwards.
	taskID := "restore-checkout"
	taskBranch := "task/" + taskID
	runGit(t, repoDir, "checkout", "-b", taskBranch, mainBranch)
	taskFile := filepath.Join(repoDir, "task_file.txt")
	os.WriteFile(taskFile, []byte("task work\n"), 0o644)
	runGit(t, repoDir, "add", "task_file.txt")
	runGit(t, repoDir, "commit", "-m", "task work")
	runGit(t, repoDir, "checkout", "user-feature")

	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Restore checkout",
		Prompt:         "Merge test",
		Status:         types.StatusReview,
		Branch:         taskBranch,
		BaseRef:        mainBranch,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(task)

	if err := te.MergeBranch(projectID, taskID, task); err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// The merge landed on the main branch...
	cmd = exec.Command("git", "-C", repoDir, "show", mainBranch+":task_file.txt")
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("task_file.txt should exist on %s after merge: %v (%s)", mainBranch, err, out)
	}
	if strings.TrimSpace(string(out)) != "task work" {
		t.Errorf("unexpected content on %s: %q", mainBranch, string(out))
	}

	// ...and the main checkout is back on the user's branch...
	cmd = exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("cannot determine checkout after merge: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "user-feature" {
		t.Errorf("main checkout should be restored to user-feature, got %q", got)
	}

	// ...with the user's uncommitted changes back in the working tree.
	data, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("user's uncommitted file should be restored: %v", err)
	}
	if strings.TrimSpace(string(data)) != "user's in-progress work" {
		t.Errorf("unexpected restored content: %q", string(data))
	}

	// The user's branch itself must not have received the merge.
	if out, err := exec.Command("git", "-C", repoDir, "show", "user-feature:task_file.txt").CombinedOutput(); err == nil {
		t.Errorf("user-feature should NOT contain the task file: %s", out)
	}
}

// TestMergeBranch_ConcurrentProjectMerges verifies that simultaneous root-task
// merges on the same project don't clobber each other's main-checkout state:
// both succeed (including a shared custom base that only one of them may
// create), each lands on the shared base, main stays clean, and the checkout
// is restored.
func TestMergeBranch_ConcurrentProjectMerges(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("cannot determine main branch: %v", err)
	}
	mainBranch := strings.TrimSpace(string(out))
	if mainBranch == "" || mainBranch == "HEAD" {
		t.Skipf("cannot determine main branch: %q", mainBranch)
	}

	// Two tasks that merge into the SAME not-yet-existing custom base — the
	// create-branch race and the main-checkout race in one.
	mkTask := func(taskID, file, content string) *types.Task {
		taskBranch := "task/" + taskID
		runGit(t, repoDir, "checkout", "-b", taskBranch, mainBranch)
		os.WriteFile(filepath.Join(repoDir, file), []byte(content+"\n"), 0o644)
		runGit(t, repoDir, "add", file)
		runGit(t, repoDir, "commit", "-m", file)
		runGit(t, repoDir, "checkout", mainBranch)
		task := &types.Task{
			ID:             taskID,
			ProjectID:      projectID,
			Title:          taskID,
			Prompt:         "p",
			Status:         types.StatusReview,
			Branch:         taskBranch,
			BaseRef:        "shared-base",
			LlamaServerURL: "http://localhost:8080",
			ModelName:      "test-model",
		}
		te.store.CreateTask(task)
		return task
	}
	taskA := mkTask("conc-a", "file_a.txt", "A")
	taskB := mkTask("conc-b", "file_b.txt", "B")

	// Start both merges at the same instant.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, task := range []*types.Task{taskA, taskB} {
		i, task := i, task
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = te.MergeBranch(projectID, task.ID, task)
		}()
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("concurrent merges should both succeed: a=%v b=%v", errs[0], errs[1])
	}

	// Both files landed on the shared base...
	for _, f := range []string{"file_a.txt", "file_b.txt"} {
		out, err := exec.Command("git", "-C", repoDir, "show", "shared-base:"+f).Output()
		if err != nil {
			t.Fatalf("%s should be on shared-base: %v (%s)", f, err, out)
		}
	}

	// ...main stayed clean...
	if out, err := exec.Command("git", "-C", repoDir, "show", mainBranch+":file_a.txt").CombinedOutput(); err == nil {
		t.Errorf("main must NOT contain file_a.txt: %s", out)
	}

	// ...and the main checkout was restored to where both merges started.
	out, err = exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("cannot determine checkout after merges: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != mainBranch {
		t.Errorf("main checkout should be restored to %s, got %q", mainBranch, got)
	}
}

// TestMergeBranch_ConcurrentDifferentTargets exposes the main-checkout
// clobbering race: two root tasks merging into DIFFERENT not-yet-existing
// bases at the same time. Without serialization, one merge can run after the
// other's `git checkout -f` switched the shared checkout — landing on the
// wrong branch — and the stashed working-tree changes can pop onto the wrong
// branch.
func TestMergeBranch_ConcurrentDifferentTargets(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("cannot determine main branch: %v", err)
	}
	mainBranch := strings.TrimSpace(string(out))
	if mainBranch == "" || mainBranch == "HEAD" {
		t.Skipf("cannot determine main branch: %q", mainBranch)
	}

	// Uncommitted work in the main checkout — must survive both merges and
	// pop back onto the user's branch, not a merge target.
	localFile := filepath.Join(repoDir, "local_edit.txt")
	os.WriteFile(localFile, []byte("user's in-progress work\n"), 0o644)

	mkTask := func(taskID, base, file, content string) *types.Task {
		taskBranch := "task/" + taskID
		runGit(t, repoDir, "checkout", "-b", taskBranch, mainBranch)
		os.WriteFile(filepath.Join(repoDir, file), []byte(content+"\n"), 0o644)
		runGit(t, repoDir, "add", file)
		runGit(t, repoDir, "commit", "-m", file)
		runGit(t, repoDir, "checkout", mainBranch)
		task := &types.Task{
			ID:             taskID,
			ProjectID:      projectID,
			Title:          taskID,
			Prompt:         "p",
			Status:         types.StatusReview,
			Branch:         taskBranch,
			BaseRef:        base,
			LlamaServerURL: "http://localhost:8080",
			ModelName:      "test-model",
		}
		te.store.CreateTask(task)
		return task
	}
	taskA := mkTask("conc-x", "base-x", "file_x.txt", "X")
	taskB := mkTask("conc-y", "base-y", "file_y.txt", "Y")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, task := range []*types.Task{taskA, taskB} {
		i, task := i, task
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = te.MergeBranch(projectID, task.ID, task)
		}()
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("concurrent merges should both succeed: x=%v y=%v", errs[0], errs[1])
	}

	// Each file must be on ITS OWN target — and only there.
	checks := []struct {
		ref  string
		file string
		want bool
	}{
		{"base-x", "file_x.txt", true},
		{"base-x", "file_y.txt", false},
		{"base-y", "file_y.txt", true},
		{"base-y", "file_x.txt", false},
		{mainBranch, "file_x.txt", false},
		{mainBranch, "file_y.txt", false},
	}
	for _, c := range checks {
		_, err := exec.Command("git", "-C", repoDir, "show", c.ref+":"+c.file).Output()
		if c.want && err != nil {
			t.Errorf("%s should be on %s (wrong-branch merge?)", c.file, c.ref)
		}
		if !c.want && err == nil {
			t.Errorf("%s should NOT be on %s (wrong-branch merge?)", c.file, c.ref)
		}
	}

	// The user's uncommitted changes must be back on the main branch.
	data, err := os.ReadFile(localFile)
	if err != nil || strings.TrimSpace(string(data)) != "user's in-progress work" {
		t.Errorf("user's uncommitted changes should be restored (err=%v, data=%q)", err, localFile)
	}

	// And the checkout is back on the main branch.
	out, err = exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("cannot determine checkout after merges: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != mainBranch {
		t.Errorf("main checkout should be restored to %s, got %q", mainBranch, got)
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
		Status:         types.StatusBacklog, // never started
		Branch:         "",                  // no branch
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
		Status:         types.StatusInProgress,
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
	parentFromDB, _ := te.store.GetTaskByID(parentID)
	if parentFromDB.Branch != "" {
		t.Fatalf("parent should have no branch initially, got %q", parentFromDB.Branch)
	}

	// Merge — should create parent branch on demand, NOT fall back to origin
	subtaskFromDB, _ := te.store.GetTaskByID(subtaskID)
	err := te.MergeBranch(projectID, subtaskID, subtaskFromDB)
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Verify parent now has a branch (created on demand)
	parentFromDB, _ = te.store.GetTaskByID(parentID)
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
		Status:         types.StatusBacklog,
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
		Status:         types.StatusInProgress,
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		AutoCommit:     true,
		AutoMerge:      true,
	}
	te.store.CreateTask(subtask)

	// Prepare subtask execution (this should trigger ensureParentBranch)
	setup, err := te.prepareTaskExecution(projectID, subtaskID, "", "", true)
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
	parentFromDB, _ := te.store.GetTaskByID(parentID)
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

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)

	projectID := "test-project"
	proj := &types.Project{ID: projectID, Name: "test", Path: repoDir}
	s.CreateProject(proj)

	worktreeMgr := git.NewWorktreeManager(s, cfgStore)
	appStreamMgr := newTestStreamMgr(t, s, worktreeMgr)

	te := newTestExecutorWith(t, Deps{
		Store:        s,
		ConfigStore:  cfgStore,
		AppStreamMgr: appStreamMgr,
		WorktreeMgr:  worktreeMgr,
	})

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
		Status:         types.StatusBacklog,
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
		Status:         types.StatusInProgress,
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		AutoCommit:     true,
		AutoMerge:      true,
	}
	te.store.CreateTask(subtask)

	// Step 1: Prepare subtask — should create parent branch
	setup, err := te.prepareTaskExecution(projectID, subtaskID, "", "", true)
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

	parentFromDB, _ := te.store.GetTaskByID(parentID)
	if parentFromDB.Branch == "" {
		t.Fatal("parent branch should be created")
	}

	// Step 2: Simulate subtask making changes and committing
	subtaskFile := filepath.Join(setup.worktreePath, "subtask_work.txt")
	os.WriteFile(subtaskFile, []byte("subtask implementation\n"), 0o644)
	runGitInDir(t, setup.worktreePath, "add", "-A")
	runGitInDir(t, setup.worktreePath, "commit", "-m", "subtask work")

	// Step 3: Merge subtask into parent branch
	subtaskFromDB, _ := te.store.GetTaskByID(subtaskID)
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
	parentSetup, err := te.prepareTaskExecution(projectID, parentID, "", "", true)
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
	parentFromDB, _ = te.store.GetTaskByID(parentID)
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
		Status:         types.StatusBacklog,
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
		Status:         types.StatusInProgress,
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		AutoMerge:      true,
	}
	te.store.CreateTask(subtask)

	// Prepare subtask — creates parent branch + subtask worktree
	setup, err := te.prepareTaskExecution(projectID, subtaskID, "", "", true)
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

	parentFromDB, _ := te.store.GetTaskByID(parentID)
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
	subtaskFromDB, _ := te.store.GetTaskByID(subtaskID)
	err = te.MergeBranch(projectID, subtaskID, subtaskFromDB)

	// Sync-merge enters merge_conflict state — MergeBranch returns nil
	// (the task is now in merge_conflict state for interactive resolution).
	// The final merge (task→target) is skipped.
	if err != nil {
		t.Fatalf("mergeBranch should return nil when sync enters merge_conflict state: %v", err)
	}

	// Verify task is in merge_conflict state
	subtaskFromDB, _ = te.store.GetTaskByID(subtaskID)
	if !subtaskFromDB.HasMergeConflict {
		t.Fatalf("expected has_merge_conflict=true, got false")
	}

	// Verify the parent branch was NOT modified (sync merge is in progress, not committed)
	content, _ := os.ReadFile(sharedFile)
	if string(content) != "parent version of the file\n" {
		t.Errorf("parent worktree should be unchanged, got: %q", string(content))
	}
}

// TestAutoStartNextSibling_ParentInBacklog verifies that when the parent is in
// backlog, AutoStartNextSibling does NOT auto-start sibling tasks. Only the first
// subtask runs; siblings stay idle in In Progress until the user explicitly moves
// the parent to In Progress.
func TestAutoStartNextSibling_ParentInBacklog(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent in backlog
	parentID := "parent-sibling-test"
	parent := &types.Task{
		ID:              parentID,
		ProjectID:       projectID,
		Title:           "Parent Task",
		Prompt:          "Parent prompt",
		Status:          types.StatusBacklog,
		CurrentThreadID: "thread-parent",
		LlamaServerURL:  "http://localhost:8080",
		ModelName:       "test-model",
	}
	te.store.CreateTask(parent)

	// Create two subtasks — moved to in_progress by moveAllDescendantsToInProgress
	subtask1ID := "subtask1-sibling"
	subtask2ID := "subtask2-sibling"
	for i, id := range []string{subtask1ID, subtask2ID} {
		subtask := &types.Task{
			ID:              id,
			ProjectID:       projectID,
			Title:           "Subtask " + id,
			Prompt:          "Subtask prompt",
			Status:          types.StatusInProgress,
			ExecutionState:  types.ExecStateIdle,
			ParentTaskID:    parentID,
			CurrentThreadID: "thread-subtask-" + string(rune('1'+i)),
			LlamaServerURL:  "http://localhost:8080",
			ModelName:       "test-model",
		}
		te.store.CreateTask(subtask)
	}

	// Prepare subtask1 (creates parent branch, parent stays in backlog)
	setup, err := te.prepareTaskExecution(projectID, subtask1ID, "", "", true)
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
	parentFromDB, _ := te.store.GetTaskByID(parentID)
	if parentFromDB.Status != types.StatusBacklog {
		t.Errorf("parent should stay in backlog, got status %q", parentFromDB.Status)
	}
	if parentFromDB.Branch == "" {
		t.Fatal("parent should have a branch")
	}

	// Simulate subtask1 completing (move to review)
	subtask1, _ := te.store.GetTaskByID(subtask1ID)
	subtask1.Status = types.StatusReview
	subtask1.ExecutionState = types.ExecStateIdle
	te.store.UpdateTask(subtask1)

	// Stop subtask1 executor
	setup.cancel()
	te.StopTask(subtask1ID)

	// AutoStartNextSibling should NOT start subtask2 because parent is in backlog
	err = te.AutoStartNextSibling(projectID, subtask1ID, parentID)
	if err != nil {
		t.Fatalf("AutoStartNextSibling failed: %v", err)
	}

	// Verify subtask2 is NOT registered in the executor's in-memory task map
	te.taskMu.Lock()
	_, exists2 := te.tasks[subtask2ID]
	te.taskMu.Unlock()
	if exists2 {
		t.Error("subtask2 should NOT be registered in executor task map when parent is in backlog")
	}

	// Verify subtask2 is NOT running in DB
	subtask2, _ := te.store.GetTaskByID(subtask2ID)
	if subtask2.ExecutionState == types.ExecStateRunning {
		t.Error("subtask2 should NOT be running — sibling should not auto-start when parent is in backlog")
	}
	if subtask2.Status != types.StatusInProgress {
		t.Errorf("subtask2 should stay in in_progress, got status %q", subtask2.Status)
	}
	if subtask2.ExecutionState != types.ExecStateIdle {
		t.Errorf("subtask2 should be idle, got execution_state %q", subtask2.ExecutionState)
	}

	// Verify parent is STILL in backlog
	parentFromDB, _ = te.store.GetTaskByID(parentID)
	if parentFromDB.Status != types.StatusBacklog {
		t.Errorf("parent should still be in backlog, got %q", parentFromDB.Status)
	}
}

// TestAutoStartNextSibling_ParentInProgress verifies that when the parent is
// in_progress and a subtask completes, the LLM selects the next sibling and
// starts it.
func TestAutoStartNextSibling_ParentInProgress(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent in in_progress
	parentID := "parent-inprogress-test"
	parent := &types.Task{
		ID:              parentID,
		ProjectID:       projectID,
		Title:           "Parent Task",
		Prompt:          "Parent prompt",
		Status:          types.StatusInProgress,
		ExecutionState:  types.ExecStateIdle,
		CurrentThreadID: "thread-parent",
		LlamaServerURL:  "http://localhost:8080",
		ModelName:       "test-model",
	}
	te.store.CreateTask(parent)

	// Create two subtasks in in_progress
	subtask1ID := "subtask1-inprogress"
	subtask2ID := "subtask2-inprogress"
	for i, id := range []string{subtask1ID, subtask2ID} {
		subtask := &types.Task{
			ID:              id,
			ProjectID:       projectID,
			Title:           "Subtask " + id,
			Prompt:          "Subtask prompt",
			Status:          types.StatusInProgress,
			ExecutionState:  types.ExecStateIdle,
			ParentTaskID:    parentID,
			CurrentThreadID: "thread-subtask-" + string(rune('1'+i)),
			LlamaServerURL:  "http://localhost:8080",
			ModelName:       "test-model",
		}
		te.store.CreateTask(subtask)
	}

	// Prepare subtask1
	setup, err := te.prepareTaskExecution(projectID, subtask1ID, "", "", true)
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

	// Verify parent is in in_progress
	parentFromDB, _ := te.store.GetTaskByID(parentID)
	if parentFromDB.Status != types.StatusInProgress {
		t.Errorf("parent should be in_progress, got status %q", parentFromDB.Status)
	}

	// Simulate subtask1 completing (move to review)
	subtask1, _ := te.store.GetTaskByID(subtask1ID)
	subtask1.Status = types.StatusReview
	subtask1.ExecutionState = types.ExecStateIdle
	te.store.UpdateTask(subtask1)

	// Stop subtask1 executor
	setup.cancel()
	te.StopTask(subtask1ID)

	// AutoStartNextSibling should start subtask2 (parent is in_progress)
	err = te.AutoStartNextSibling(projectID, subtask1ID, parentID)
	if err != nil {
		t.Fatalf("AutoStartNextSibling failed: %v", err)
	}

	// Verify subtask2 is registered in the executor's in-memory task map
	te.taskMu.Lock()
	state2, exists2 := te.tasks[subtask2ID]
	te.taskMu.Unlock()
	if !exists2 {
		t.Fatal("subtask2 should be registered in executor task map")
	}
	if state2.executorType != "llm" {
		t.Errorf("subtask2 executor type should be 'llm', got %q", state2.executorType)
	}

	// Verify subtask2 is running in DB
	subtask2, _ := te.store.GetTaskByID(subtask2ID)
	if subtask2.ExecutionState != types.ExecStateRunning {
		t.Errorf("subtask2 should be running, got execution_state %q", subtask2.ExecutionState)
	}
	if subtask2.Status != types.StatusInProgress {
		t.Errorf("subtask2 status should be in_progress, got %q", subtask2.Status)
	}
	// Auto-started from a parent via sibling continuation => Autostarted must be true
	if !subtask2.Autostarted {
		t.Errorf("subtask2 should be marked autostarted when started via AutoStartNextSibling")
	}

	// Verify parent is still in in_progress
	parentFromDB, _ = te.store.GetTaskByID(parentID)
	if parentFromDB.Status != types.StatusInProgress {
		t.Errorf("parent should still be in_progress, got %q", parentFromDB.Status)
	}

	te.StopTask(subtask2ID)
}

// TestStartTaskUserInitiatedNotAutostarted verifies that a direct user-initiated
// StartTask leaves the task's Autostarted flag false, even overwriting a prior true.
func TestStartTaskUserInitiatedNotAutostarted(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "user-start-task"
	task := &types.Task{
		ID:              taskID,
		ProjectID:       projectID,
		Title:           "User Started Task",
		Prompt:          "Task prompt",
		Status:          types.StatusInProgress,
		ExecutionState:  types.ExecStateIdle,
		CurrentThreadID: "thread-user-start",
		LlamaServerURL:  "http://localhost:8080",
		ModelName:       "test-model",
		// Seed with true to prove a user-initiated start overwrites it to false.
		Autostarted: true,
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := te.StartTask(projectID, taskID, false); err != nil {
		t.Fatalf("StartTask failed: %v", err)
	}
	defer te.StopTask(taskID)

	// Verify the flag is persisted as false for a user-initiated start.
	got, err := te.store.GetTaskByID(taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Autostarted {
		t.Errorf("user-initiated StartTask should leave Autostarted=false")
	}
}

// TestStartSubtask_InheritsModelConfig verifies that when a subtask
// doesn't have its own LLM config, it inherits from the parent. This ensures
// subtasks created by plan-mode decomposition can start without explicit model settings.
func TestStartSubtask_InheritsModelConfig(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent with model config
	parentID := "parent-model-inherit"
	parent := &types.Task{
		ID:              parentID,
		ProjectID:       projectID,
		Title:           "Parent Task",
		Prompt:          "Parent prompt",
		Status:          types.StatusInProgress,
		ExecutionState:  types.ExecStateIdle,
		CurrentThreadID: "thread-parent",
		LlamaServerURL:  "http://localhost:8080",
		ModelName:       "test-model",
	}
	te.store.CreateTask(parent)

	// Create subtask WITHOUT model config (simulates plan-mode created subtask)
	subtaskID := "subtask-no-model"
	subtask := &types.Task{
		ID:              subtaskID,
		ProjectID:       projectID,
		Title:           "Subtask No Model",
		Prompt:          "Subtask prompt",
		Status:          types.StatusInProgress,
		ExecutionState:  types.ExecStateIdle,
		ParentTaskID:    parentID,
		CurrentThreadID: "thread-subtask",
		// LlamaServerURL and ModelName are intentionally empty
	}
	te.store.CreateTask(subtask)

	// Verify subtask has no model config
	subtaskFromDB, _ := te.store.GetTaskByID(subtaskID)
	if subtaskFromDB.LlamaServerURL != "" {
		t.Fatalf("subtask should have no LlamaServerURL initially, got %q", subtaskFromDB.LlamaServerURL)
	}
	if subtaskFromDB.ModelName != "" {
		t.Fatalf("subtask should have no ModelName initially, got %q", subtaskFromDB.ModelName)
	}

	// StartSubtask should inherit model config from parent
	err := te.StartSubtask(projectID, subtaskID, false)
	if err != nil {
		t.Fatalf("StartSubtask failed: %v", err)
	}

	// Verify subtask now has inherited model config
	subtaskFromDB, _ = te.store.GetTaskByID(subtaskID)
	if subtaskFromDB.LlamaServerURL != "http://localhost:8080" {
		t.Errorf("subtask should inherit LlamaServerURL from parent, got %q", subtaskFromDB.LlamaServerURL)
	}
	if subtaskFromDB.ModelName != "test-model" {
		t.Errorf("subtask should inherit ModelName from parent, got %q", subtaskFromDB.ModelName)
	}

	// Verify subtask is registered in executor
	te.taskMu.Lock()
	_, exists := te.tasks[subtaskID]
	te.taskMu.Unlock()
	if !exists {
		t.Error("subtask should be registered in executor task map")
	}

	te.StopTask(subtaskID)
}

// TestAutoStartNextSibling_AllDoneParentStaysInBacklog verifies that when all
// subtasks are done and the parent is in backlog, the parent is NOT auto-started.
// The parent stays in backlog until the user explicitly starts or moves it.
func TestAutoStartNextSibling_AllDoneParentStaysInBacklog(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent in backlog
	parentID := "parent-stay-backlog"
	parent := &types.Task{
		ID:              parentID,
		ProjectID:       projectID,
		Title:           "Parent Task",
		Prompt:          "Parent prompt",
		Status:          types.StatusBacklog,
		CurrentThreadID: "thread-parent",
		LlamaServerURL:  "http://localhost:8080",
		ModelName:       "test-model",
	}
	te.store.CreateTask(parent)

	// Create a single subtask
	subtaskID := "subtask-last-one"
	subtask := &types.Task{
		ID:              subtaskID,
		ProjectID:       projectID,
		Title:           "Last Subtask",
		Prompt:          "Subtask prompt",
		Status:          types.StatusInProgress,
		ExecutionState:  types.ExecStateIdle,
		ParentTaskID:    parentID,
		CurrentThreadID: "thread-subtask",
		LlamaServerURL:  "http://localhost:8080",
		ModelName:       "test-model",
	}
	te.store.CreateTask(subtask)

	// Prepare subtask (creates parent branch)
	setup, err := te.prepareTaskExecution(projectID, subtaskID, "", "", true)
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
	subtask, _ = te.store.GetTaskByID(subtaskID)
	subtask.Status = types.StatusDone
	subtask.ExecutionState = types.ExecStateCompleted
	te.store.UpdateTask(subtask)

	// Stop subtask executor
	setup.cancel()
	te.StopTask(subtaskID)

	// AutoStartNextSibling with no incomplete siblings and all done
	err = te.AutoStartNextSibling(projectID, subtaskID, parentID)
	if err != nil {
		t.Fatalf("AutoStartNextSibling failed: %v", err)
	}

	// Verify parent is STILL in backlog (NOT auto-started)
	parentFromDB, _ := te.store.GetTaskByID(parentID)
	if parentFromDB.Status != types.StatusBacklog {
		t.Errorf("parent should stay in backlog after all children done, got status %q", parentFromDB.Status)
	}
	if parentFromDB.ExecutionState == types.ExecStateRunning {
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
		Status:         types.StatusBacklog,
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
		Status:         types.StatusReview,
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
	subtaskFromDB, _ := te.store.GetTaskByID(subtaskID)
	err := te.MergeBranch(projectID, subtaskID, subtaskFromDB)
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Verify the subtask's commit is on the parent branch.
	// Merge happens in the parent's worktree, so check there.
	parentWtPath2, wtErr := te.worktreeMgr.WorktreePath(parentID)
	if wtErr != nil || parentWtPath2 == "" {
		showCmd := exec.Command("git", "-C", repoDir, "show", parent.Branch+":commit_done_file.txt")
		_, showErr := showCmd.Output()
		if showErr != nil {
			t.Fatalf("subtask file should exist on parent branch after merge: %v", showErr)
		}
	} else {
		wtSubtaskFile2 := filepath.Join(parentWtPath2, "commit_done_file.txt")
		if _, err := os.Stat(wtSubtaskFile2); err != nil {
			t.Fatalf("subtask file should exist in parent worktree after merge: %v", err)
		}
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
		Status:         types.StatusBacklog,
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
		Status:         types.StatusReview,
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
	parentFromDB, _ := te.store.GetTaskByID(parentID)
	if parentFromDB.Branch != "" {
		t.Fatalf("parent should have no branch initially, got %q", parentFromDB.Branch)
	}

	// Ensure parent branch is created (safety net from HandleCommitAndDone)
	err := te.EnsureParentBranch(projectID, parentFromDB)
	if err != nil {
		t.Fatalf("EnsureParentBranch failed: %v", err)
	}

	// Verify parent now has a branch
	parentFromDB, _ = te.store.GetTaskByID(parentID)
	if parentFromDB.Branch == "" {
		t.Fatal("parent should have a branch after EnsureParentBranch")
	}

	// Merge subtask — should go to parent's branch, not master
	subtaskFromDB, _ := te.store.GetTaskByID(subtaskID)
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

// TestMergeBranch_ParentDiffStatsAfterSubtaskMerge verifies that after a subtask
// merges into its parent's branch, the parent's diff stats reflect the merged
// committed changes (drives the card's "Commit and move to done" button). The
// parent was never started: it has a branch but no recorded base_ref/base_commit.
func TestMergeBranch_ParentDiffStatsAfterSubtaskMerge(t *testing.T) {
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	parentID := "parent-stats"
	parent := &types.Task{
		ID:        parentID,
		ProjectID: projectID,
		Title:     "Parent Task",
		Prompt:    "Parent prompt",
		Status:    types.StatusBacklog,
	}
	te.store.CreateTask(parent)

	subtaskID := "subtask-stats"
	subtask := &types.Task{
		ID:           subtaskID,
		ProjectID:    projectID,
		Title:        "Subtask",
		Prompt:       "Subtask prompt",
		Status:       types.StatusReview,
		Branch:       "task/" + subtaskID,
		ParentTaskID: parentID,
	}
	te.store.CreateTask(subtask)

	// Subtask branch with its own commit (branched from master).
	runGit(t, repoDir, "branch", subtask.Branch, "master")
	runGit(t, repoDir, "checkout", subtask.Branch)
	os.WriteFile(filepath.Join(repoDir, "subtask_merged.txt"), []byte("subtask work\n"), 0o644)
	runGit(t, repoDir, "add", "subtask_merged.txt")
	runGit(t, repoDir, "commit", "-m", "subtask work")
	// The project's main checkout is on master when the parent branch is created.
	runGit(t, repoDir, "checkout", "master")

	// Parent branch + worktree are auto-created (parent never started — no
	// base_ref/base_commit recorded).
	parentFromDB, _ := te.store.GetTaskByID(parentID)
	if err := te.EnsureParentBranch(projectID, parentFromDB); err != nil {
		t.Fatalf("EnsureParentBranch failed: %v", err)
	}
	parentFromDB, _ = te.store.GetTaskByID(parentID)
	if parentFromDB.Branch == "" {
		t.Fatal("parent should have a branch after EnsureParentBranch")
	}

	// No changes yet — no stats.
	if ev := te.computeTaskDiffStats(projectID, parentID); ev != nil {
		t.Fatalf("expected no diff stats before the merge, got %+v", ev)
	}

	// Merge the subtask into the parent branch.
	subtaskFromDB, _ := te.store.GetTaskByID(subtaskID)
	if err := te.MergeBranch(projectID, subtaskID, subtaskFromDB); err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// The parent's stats must now surface the merged commit.
	ev := te.computeTaskDiffStats(projectID, parentID)
	if ev == nil {
		t.Fatal("expected diff stats for the parent after the subtask merge")
	}
	if ev.TaskID != parentID || ev.ProjectID != projectID {
		t.Errorf("unexpected event identity: task=%q project=%q", ev.TaskID, ev.ProjectID)
	}
	if !ev.HasCommittedChanges {
		t.Error("expected HasCommittedChanges for the merged subtask changes")
	}
	if ev.FilesN != 1 {
		t.Errorf("expected 1 changed file, got %d", ev.FilesN)
	}
	if ev.TrackedAdd != 1 {
		t.Errorf("expected 1 added line, got %d", ev.TrackedAdd)
	}
	if _, ok := ev.ChangedHashes["subtask_merged.txt"]; !ok {
		t.Errorf("expected subtask_merged.txt in changed hashes, got keys: %v", hashKeys(ev.ChangedHashes))
	}
	if ev.AlreadyMerged {
		t.Error("parent branch is not yet merged into main — AlreadyMerged must be false")
	}
}

func hashKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
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
		Status:         types.StatusInProgress,
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
		Status:         types.StatusInProgress,
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

// TestSubtaskManualStart_MergeDiagnostic reproduces the scenario where
// a parent is in backlog and a subtask is started manually.
// It inspects every intermediate state to diagnose why the merge might fail.
func TestSubtaskManualStart_MergeDiagnostic(t *testing.T) {
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent in backlog (never started, no branch)
	parentID := "parent-diag"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         types.StatusBacklog,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		AutoMerge:      true,
	}
	te.store.CreateTask(parent)

	// Create subtask
	subtaskID := "subtask-diag"
	subtask := &types.Task{
		ID:             subtaskID,
		ProjectID:      projectID,
		Title:          "Subtask",
		Prompt:         "Subtask prompt",
		Status:         types.StatusBacklog,
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		AutoMerge:      true,
	}
	te.store.CreateTask(subtask)

	// Verify initial state
	parentBefore, _ := te.store.GetTaskByID(parentID)
	if parentBefore.Branch != "" {
		t.Fatalf("parent should have no branch initially, got %q", parentBefore.Branch)
	}

	// Step 1: Prepare subtask execution (simulates manual start)
	setup, err := te.prepareTaskExecution(projectID, subtaskID, "", "", true)
	if err != nil {
		t.Fatalf("prepareTaskExecution failed: %v", err)
	}
	defer func() {
		setup.cancel()
		te.StopTask(subtaskID)
	}()

	// Step 2: Verify parent branch was created
	parentAfter, _ := te.store.GetTaskByID(parentID)
	if parentAfter.Branch == "" {
		t.Fatal("parent branch should be created after subtask preparation")
	}
	t.Logf("Parent branch: %q", parentAfter.Branch)

	// Verify parent branch exists in git
	if err := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", parentAfter.Branch).Run(); err != nil {
		t.Fatalf("parent branch %q does not exist in git", parentAfter.Branch)
	}

	// Step 3: Verify parent worktree exists
	parentWtPath, err := te.worktreeMgr.WorktreePath(parentID)
	if err != nil {
		t.Fatalf("WorktreePath for parent failed: %v", err)
	}
	if _, err := os.Stat(parentWtPath); err != nil {
		t.Fatalf("parent worktree does not exist at %s: %v", parentWtPath, err)
	}
	t.Logf("Parent worktree: %s", parentWtPath)

	// Step 4: Verify subtask worktree and branch
	subtaskAfter, _ := te.store.GetTaskByID(subtaskID)
	if subtaskAfter.Branch == "" {
		t.Fatal("subtask should have a branch")
	}
	t.Logf("Subtask branch: %q, BaseRef: %q", subtaskAfter.Branch, subtaskAfter.BaseRef)

	subtaskWtPath, err := te.worktreeMgr.WorktreePath(subtaskID)
	if err != nil {
		t.Fatalf("WorktreePath for subtask failed: %v", err)
	}
	if _, err := os.Stat(subtaskWtPath); err != nil {
		t.Fatalf("subtask worktree does not exist at %s: %v", subtaskWtPath, err)
	}

	// Verify subtask branch is descendant of parent branch
	cmd := exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor", parentAfter.Branch, subtaskAfter.Branch)
	if err := cmd.Run(); err != nil {
		t.Fatalf("subtask branch should be descendant of parent branch: %v", err)
	}

	// Step 5: Simulate subtask work (make a commit)
	subtaskFile := filepath.Join(subtaskWtPath, "subtask_file.txt")
	os.WriteFile(subtaskFile, []byte("subtask work\n"), 0o644)
	runGitInDir(t, subtaskWtPath, "add", "-A")
	runGitInDir(t, subtaskWtPath, "commit", "-m", "subtask work")

	// Get parent branch tip before merge
	parentTipBefore := getGitRevParse(t, repoDir, parentAfter.Branch)

	// Step 6: Merge subtask into parent
	subtaskForMerge, _ := te.store.GetTaskByID(subtaskID)
	err = te.MergeBranch(projectID, subtaskID, subtaskForMerge)
	if err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Step 7: Verify merge result
	parentTipAfter := getGitRevParse(t, repoDir, parentAfter.Branch)
	if parentTipBefore == parentTipAfter {
		t.Fatal("parent branch tip should have changed after merge")
	}

	// Verify subtask's commit is on parent branch
	cmd = exec.Command("git", "-C", repoDir, "show", parentAfter.Branch+":subtask_file.txt")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("subtask file should be on parent branch: %v", err)
	}
	if strings.TrimSpace(string(out)) != "subtask work" {
		t.Errorf("unexpected content on parent branch: %q", string(out))
	}

	// Verify NOT on master
	cmd = exec.Command("git", "-C", repoDir, "show", "master:subtask_file.txt")
	if _, err := cmd.CombinedOutput(); err == nil {
		t.Error("subtask file should NOT be on master")
	}
}

// TestPrepareTaskExecution_BranchPersists verifies that prepareTaskExecution
// persists the branch to the DB and that the handler's reload-before-update
// pattern preserves it. The move handler reloads the task after StartTask
// to pick up the branch set by prepareTaskExecution, avoiding the stale
// object bug where UpdateTask would overwrite branch back to "".
func TestPrepareTaskExecution_BranchPersists(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent in backlog
	parentID := "parent-persist"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Status:         types.StatusBacklog,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.store.CreateTask(parent)

	// Create subtask
	subtaskID := "subtask-persist"
	subtask := &types.Task{
		ID:             subtaskID,
		ProjectID:      projectID,
		Title:          "Subtask",
		Status:         types.StatusBacklog,
		ParentTaskID:   parentID,
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		AutoMerge:      true,
	}
	te.store.CreateTask(subtask)

	// Simulate the move handler pattern:
	// 1. Load task (branch="")
	staleTask, _ := te.store.GetTaskByID(subtaskID)
	if staleTask.Branch != "" {
		t.Fatalf("subtask should have no branch initially, got %q", staleTask.Branch)
	}

	// 2. UpdateTask with stale data (status change, branch still "")
	// This simulates the handler setting status="in_progress" before StartTask
	staleTask.Status = types.StatusInProgress
	if err := te.store.UpdateTask(staleTask); err != nil {
		t.Fatalf("UpdateTask failed: %v", err)
	}

	// 3. prepareTaskExecution (sets branch, calls UpdateTask)
	setup, err := te.prepareTaskExecution(projectID, subtaskID, "", "", true)
	if err != nil {
		t.Fatalf("prepareTaskExecution failed: %v", err)
	}
	defer func() {
		setup.cancel()
		te.StopTask(subtaskID)
	}()

	// 4. Verify branch is set in DB (not just in memory)
	taskFromDB, _ := te.store.GetTaskByID(subtaskID)
	if taskFromDB.Branch == "" {
		t.Fatal("branch should be set in DB after prepareTaskExecution")
	}
	branch := taskFromDB.Branch

	// 5. Simulate the fixed handler: reload task before UpdateTask.
	// The handler reloads to pick up the branch set by prepareTaskExecution,
	// avoiding the stale object bug where UpdateTask would overwrite branch.
	reloaded, err := te.store.GetTaskByID(subtaskID)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	reloaded.Status = types.StatusInProgress // handler's status
	if err := te.store.UpdateTask(reloaded); err != nil {
		t.Fatalf("UpdateTask failed: %v", err)
	}

	// 6. Verify branch is STILL set in DB after the reload-and-update pattern
	taskFromDB, _ = te.store.GetTaskByID(subtaskID)
	if taskFromDB.Branch == "" {
		t.Fatal("branch should NOT be cleared by reload-and-update")
	}
	if taskFromDB.Branch != branch {
		t.Errorf("branch changed: was %q, now %q", branch, taskFromDB.Branch)
	}
}

// TestEnsureParentBranch_RecoverMissingBranch verifies that when the parent's
// branch is set in DB but missing in git (e.g., CreateWorktree failed on a
// previous call), EnsureParentBranch recovers by creating the branch.
func TestEnsureParentBranch_RecoverMissingBranch(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	parentID := "parent-recover-branch"
	branchName := "task/parent-recover-branch"
	parent := &types.Task{
		ID:        parentID,
		ProjectID: projectID,
		Title:     "Parent Recover",
		Prompt:    "Parent prompt",
		Status:    types.StatusBacklog,
		Branch:    branchName, // pre-set branch that doesn't exist in git
	}
	te.store.CreateTask(parent)

	// Branch name is set but doesn't exist in git.
	// EnsureParentBranch should recover by creating it.
	err := te.EnsureParentBranch(projectID, parent)
	if err != nil {
		t.Fatalf("EnsureParentBranch should recover missing branch: %v", err)
	}

	// Verify the branch now exists in git.
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", branchName)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("branch %q should exist after recovery: %s", branchName, string(out))
	}
}

// TestEnsureParentBranch_FailsWhenRepoCorrupted verifies that when the git
// repo is corrupted, EnsureParentBranch fails (can't create the branch).
func TestEnsureParentBranch_FailsWhenRepoCorrupted(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	parentID := "parent-corrupted-repo"
	parent := &types.Task{
		ID:        parentID,
		ProjectID: projectID,
		Title:     "Parent Corrupted",
		Prompt:    "Parent prompt",
		Status:    types.StatusBacklog,
	}
	te.store.CreateTask(parent)

	// Corrupt the git repo so CreateWorktree will fail.
	os.RemoveAll(filepath.Join(repoDir, ".git"))

	err := te.EnsureParentBranch(projectID, parent)
	if err == nil {
		t.Fatal("EnsureParentBranch should fail when git repo is corrupted")
	}
}

// TestMergeBranch_RecoverMissingBranch verifies that when a task's branch
// is missing in git (e.g., cleared and regenerated), MergeBranch finds the
// old branch with the same slug and updates the task's branch reference.
func TestMergeBranch_RecoverMissingBranch(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)

	// Create a task with a branch name that doesn't exist in git.
	// Simulates the case where InitTask cleared the branch and a new
	// random suffix was generated.
	parentID := "parent-missing-branch"
	oldBranch := "task/count-files-abc123" // exists in git
	newBranch := "task/count-files-xyz789" // in DB but missing in git

	// Create the old branch in git (with some work).
	runGit(t, repoDir, "branch", oldBranch, "HEAD")
	// Add a commit to the old branch to simulate child merge work.
	runGit(t, repoDir, "checkout", oldBranch)
	os.WriteFile(filepath.Join(repoDir, "child-work.txt"), []byte("child work\n"), 0o644)
	runGit(t, repoDir, "add", "child-work.txt")
	runGit(t, repoDir, "commit", "-m", "Child merge work")
	runGit(t, repoDir, "checkout", "master")

	// Create task with the NEW (missing) branch name.
	parent := &types.Task{
		ID:        parentID,
		ProjectID: projectID,
		Title:     "Count Files",
		Prompt:    "Count files",
		Status:    types.StatusDone,
		Branch:    newBranch, // doesn't exist in git
		BaseRef:   "master",
	}
	te.store.CreateTask(parent)

	// Verify the new branch doesn't exist.
	if te.merge.branchExists(projectID, newBranch) {
		t.Fatal("new branch should not exist in git")
	}

	// Call MergeBranch — it should recover the old branch.
	err := te.MergeBranch(projectID, parentID, parent)
	if err != nil {
		t.Fatalf("MergeBranch should succeed after recovery: %v", err)
	}

	// Verify the task's branch was updated in DB.
	reloaded, _ := te.store.GetTaskByID(parentID)
	if reloaded.Branch != oldBranch {
		t.Fatalf("branch should be updated to old branch: expected %q, got %q", oldBranch, reloaded.Branch)
	}

	// Verify the merge happened (old branch merged into master).
	cmd := exec.Command("git", "-C", repoDir, "log", "--oneline", "master")
	out, _ := cmd.Output()
	if !strings.Contains(string(out), "Child merge work") {
		t.Fatalf("master should contain child merge work:\n%s", string(out))
	}
}

// --- Autostart chain: readiness-aware selection ---

// seedThread seeds a task's thread with a user prompt followed by an
// assistant reply so that StartTask persists the autostarted flag and returns
// early without invoking the LLM.
func seedThread(t *testing.T, s *store.Store, taskID, threadID, prompt string) {
	t.Helper()
	now := time.Now().UTC()
	for _, m := range []*types.TaskMessage{
		{ID: taskID + "-u", TaskID: taskID, ThreadID: threadID, Role: "user", Content: prompt, CreatedAt: now},
		{ID: taskID + "-a", TaskID: taskID, ThreadID: threadID, Role: "assistant", Content: "Done.", CreatedAt: now},
	} {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatal(err)
		}
	}
}

func containsTaskID(tasks []types.Task, id string) bool {
	return slices.ContainsFunc(tasks, func(task types.Task) bool { return task.ID == id })
}

// TestFindDeepestStartableSubtasks_DeepestFirst verifies that only the
// startable subtasks at the deepest level are returned: a shallower startable
// sibling waits while a deeper branch still has pending work, and becomes a
// candidate once its subtree is done.
func TestFindDeepestStartableSubtasks_DeepestFirst(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	c1ID, c2ID, g1ID, g2ID := "c1-depth", "c2-depth", "g1-depth", "g2-depth"
	tasks := []*types.Task{
		{ID: c1ID, ProjectID: projectID, Title: "C1", Prompt: "c1", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle},
		{ID: c2ID, ProjectID: projectID, Title: "C2", Prompt: "c2", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle},
		{ID: g1ID, ProjectID: projectID, Title: "G1", Prompt: "g1", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ParentTaskID: c1ID},
		{ID: g2ID, ProjectID: projectID, Title: "G2", Prompt: "g2", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ParentTaskID: c1ID},
	}
	for _, tk := range tasks {
		if err := te.store.CreateTask(tk); err != nil {
			t.Fatal(err)
		}
	}
	c1, _ := te.store.GetTaskByID(c1ID)
	c2, _ := te.store.GetTaskByID(c2ID)

	// Deepest startable subtasks: G1 and G2 (C2, a shallower startable leaf,
	// waits for C1's subtree).
	deepest := te.FindDeepestStartableSubtasks([]types.Task{*c1, *c2})
	if len(deepest) != 2 || !containsTaskID(deepest, g1ID) || !containsTaskID(deepest, g2ID) {
		t.Fatalf("deepest = %v, want [g1 g2]", idsOf(deepest))
	}
	if containsTaskID(deepest, c2ID) {
		t.Errorf("c2 should not be a candidate while g1/g2 are pending")
	}

	// Once C1's children are done, C1 itself becomes startable at its level.
	for _, id := range []string{g1ID, g2ID} {
		tk, _ := te.store.GetTaskByID(id)
		tk.Status = types.StatusDone
		tk.ExecutionState = types.ExecStateCompleted
		te.store.UpdateTask(tk)
	}
	c1, _ = te.store.GetTaskByID(c1ID)
	c2, _ = te.store.GetTaskByID(c2ID)
	deepest = te.FindDeepestStartableSubtasks([]types.Task{*c1, *c2})
	if len(deepest) != 2 || !containsTaskID(deepest, c1ID) || !containsTaskID(deepest, c2ID) {
		t.Fatalf("deepest = %v, want [c1 c2]", idsOf(deepest))
	}
}

// TestFindDeepestStartableSubtasks_SkipsReviewBlocked verifies that a task with
// a child in Review is NOT startable (it must wait for the child to be moved
// to Done), and that its other pending children are found instead.
func TestFindDeepestStartableSubtasks_SkipsReviewBlocked(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	c1ID, r1ID, r2ID := "c1-review", "r1-review", "r2-review"
	tasks := []*types.Task{
		{ID: c1ID, ProjectID: projectID, Title: "C1", Prompt: "c1", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle},
		{ID: r1ID, ProjectID: projectID, Title: "R1", Prompt: "r1", Status: types.StatusReview, ExecutionState: types.ExecStateIdle, ParentTaskID: c1ID},
	}
	for _, tk := range tasks {
		if err := te.store.CreateTask(tk); err != nil {
			t.Fatal(err)
		}
	}
	c1, _ := te.store.GetTaskByID(c1ID)

	// C1 has a child in review — neither C1 nor R1 is startable.
	deepest := te.FindDeepestStartableSubtasks([]types.Task{*c1})
	if len(deepest) != 0 {
		t.Fatalf("deepest = %v, want none (C1 blocked on review child)", idsOf(deepest))
	}

	// A second child in a startable state IS found.
	if err := te.store.CreateTask(&types.Task{ID: r2ID, ProjectID: projectID, Title: "R2", Prompt: "r2", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ParentTaskID: c1ID}); err != nil {
		t.Fatal(err)
	}
	c1, _ = te.store.GetTaskByID(c1ID)
	deepest = te.FindDeepestStartableSubtasks([]types.Task{*c1})
	if len(deepest) != 1 || !containsTaskID(deepest, r2ID) {
		t.Fatalf("deepest = %v, want [r2]", idsOf(deepest))
	}
}

// TestFindStartableSubtasks_AllDepths checks that every ready task is a candidate at whatever depth
// its branch is ready at, so a ready leaf does not wait on a deeper branch. No task is returned
// together with one of its own descendants.
func TestFindStartableSubtasks_AllDepths(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	aID, bID, cID, g1ID, g2ID := "fa-a", "fa-b", "fa-c", "fa-g1", "fa-g2"
	revID, revChildID := "fa-review", "fa-review-child"
	tasks := []*types.Task{
		{ID: aID, Title: "A", Prompt: "a", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ProjectID: projectID},
		{ID: bID, Title: "B", Prompt: "b", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ProjectID: projectID},
		{ID: cID, Title: "C", Prompt: "c", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ProjectID: projectID},
		{ID: g1ID, Title: "G1", Prompt: "g1", Status: types.StatusBacklog, ProjectID: projectID, ParentTaskID: cID},
		{ID: g2ID, Title: "G2", Prompt: "g2", Status: types.StatusBacklog, ProjectID: projectID, ParentTaskID: cID},
		{ID: revID, Title: "Review", Prompt: "r", Status: types.StatusReview, ExecutionState: types.ExecStateIdle, ProjectID: projectID},
		{ID: revChildID, Title: "ReviewChild", Prompt: "rc", Status: types.StatusBacklog, ProjectID: projectID, ParentTaskID: revID},
	}
	for _, tk := range tasks {
		if err := te.store.CreateTask(tk); err != nil {
			t.Fatal(err)
		}
	}

	var roots []types.Task
	for _, id := range []string{aID, bID, cID, revID} {
		tk, err := te.store.GetTaskByID(id)
		if err != nil {
			t.Fatal(err)
		}
		roots = append(roots, *tk)
	}

	candidates := te.FindStartableSubtasks(roots)
	got := map[string]bool{}
	for _, c := range candidates {
		got[c.ID] = true
	}
	for _, want := range []string{aID, bID, g1ID, g2ID} {
		if !got[want] {
			t.Errorf("%s missing from candidates %v", want, idsOf(candidates))
		}
	}
	for _, unwanted := range []string{cID, revID, revChildID} {
		if got[unwanted] {
			t.Errorf("%s must not be a candidate, got %v", unwanted, idsOf(candidates))
		}
	}
}

// TestAutoStartNextSibling_ParallelStartsEachBranch checks that with the setting on, one finish
// releases every ready branch: the ready leaf and the ready task inside the sibling that is waiting
// on children. That waiting sibling itself stays unstarted.
func TestAutoStartNextSibling_ParallelStartsEachBranch(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	if err := te.store.SetConfigValue("run_siblings_parallel", "true"); err != nil {
		t.Fatal(err)
	}

	fixture := newParallelChainFixture(t, te, projectID)

	if err := te.AutoStartNextSibling(projectID, fixture.finishedID, fixture.parentID); err != nil {
		t.Fatalf("AutoStartNextSibling failed: %v", err)
	}

	for _, id := range []string{fixture.leafID, fixture.deepID} {
		tk, err := te.store.GetTaskByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if !tk.Autostarted {
			t.Errorf("%s should have started autostarted: state=%q", id, tk.ExecutionState)
		}
	}
	waiter, _ := te.store.GetTaskByID(fixture.waitID)
	if waiter.Autostarted || te.hasLiveExecutor(fixture.waitID) {
		t.Errorf("%s must stay unstarted while its own child is unfinished", fixture.waitID)
	}
}

// TestAutoStartNextSibling_SequentialStartsOneBranch is the same tree with the setting off: one
// branch starts and the ready leaf keeps waiting for the next finish.
func TestAutoStartNextSibling_SequentialStartsOneBranch(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	fixture := newParallelChainFixture(t, te, projectID)

	if err := te.AutoStartNextSibling(projectID, fixture.finishedID, fixture.parentID); err != nil {
		t.Fatalf("AutoStartNextSibling failed: %v", err)
	}

	leaf, _ := te.store.GetTaskByID(fixture.leafID)
	if leaf.Autostarted || te.hasLiveExecutor(fixture.leafID) {
		t.Errorf("%s must not start in sequential mode", fixture.leafID)
	}
	deep, _ := te.store.GetTaskByID(fixture.deepID)
	if !deep.Autostarted {
		t.Errorf("%s should have started (deepest ready task): state=%q", fixture.deepID, deep.ExecutionState)
	}
}

// TestAutoStartNextSibling_QueuedDescendantKeepsItsQueuePlace checks that a task already waiting for
// a provider slot is not started a second time, which would replace its executor and lose its place
// in that queue. Such a task can be a candidate because it waits inside a sibling that is itself only
// started-but-pending.
func TestAutoStartNextSibling_QueuedDescendantKeepsItsQueuePlace(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	if err := te.store.SetConfigValue("run_siblings_parallel", "true"); err != nil {
		t.Fatal(err)
	}
	fixture := newParallelChainFixture(t, te, projectID)
	defer queueSubtaskForSlot(t, te, projectID, fixture.deepID)()

	before := liveExecutor(te, fixture.deepID)
	if before == nil {
		t.Fatal("expected a live executor for the queued task")
	}

	if err := te.AutoStartNextSibling(projectID, fixture.finishedID, fixture.parentID); err != nil {
		t.Fatalf("AutoStartNextSibling failed: %v", err)
	}

	if liveExecutor(te, fixture.deepID) != before {
		t.Errorf("queued task %s had its executor replaced", fixture.deepID)
	}
	reloaded, _ := te.store.GetTaskByID(fixture.deepID)
	if reloaded.Autostarted {
		t.Errorf("queued task %s was started again", fixture.deepID)
	}
	// The guard skips one candidate, not the whole list.
	leaf, _ := te.store.GetTaskByID(fixture.leafID)
	if !leaf.Autostarted {
		t.Errorf("%s should still start alongside the skipped candidate", fixture.leafID)
	}
}

// threeLeafParent makes an in_progress parent with three backlog leaf subtasks. The parent has no LLM
// config, so a sequential tie-break falls back instead of calling out.
func threeLeafParent(t *testing.T, te *TaskExecutor, projectID, parentID string) *types.Task {
	t.Helper()
	parent := &types.Task{ID: parentID, ProjectID: projectID, Title: "Parent", Prompt: "parent prompt", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle}
	if err := te.store.CreateTask(parent); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parentID + "-a", parentID + "-b", parentID + "-c"} {
		if err := te.store.CreateTask(&types.Task{ID: id, ProjectID: projectID, Title: "sub", Prompt: "sub prompt", Status: types.StatusBacklog, ParentTaskID: parent.ID, LlamaServerURL: "http://localhost:8080", ModelName: "test-model"}); err != nil {
			t.Fatal(err)
		}
	}
	return parent
}

// Sequential (default) mode: one subtask, the deepest.
func TestSubtasksToStart_Sequential(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	parent := threeLeafParent(t, te, projectID, "st-seq-parent")
	children, err := te.store.GetChildren(parent.ID)
	if err != nil {
		t.Fatal(err)
	}

	if ids := te.subtasksToStart(parent, children); len(ids) != 1 {
		t.Fatalf("expected 1 subtask in sequential mode, got %d: %v", len(ids), ids)
	}
}

// Parallel mode: every currently startable subtask, so they all begin at once.
func TestSubtasksToStart_Parallel(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	parent := threeLeafParent(t, te, projectID, "st-par-parent")
	if err := te.store.SetConfigValue("run_siblings_parallel", "true"); err != nil {
		t.Fatal(err)
	}
	children, err := te.store.GetChildren(parent.ID)
	if err != nil {
		t.Fatal(err)
	}

	if ids := te.subtasksToStart(parent, children); len(ids) != 3 {
		t.Fatalf("expected 3 subtasks in parallel mode, got %d: %v", len(ids), ids)
	}
}

// Parallel mode over a nested tree: the ready leaves are picked alongside the ready task inside a
// sibling still waiting on its child.
func TestSubtasksToStart_ParallelMixedDepth(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	parent := threeLeafParent(t, te, projectID, "st-par-nest")
	deep := &types.Task{ID: "st-par-nest-c-deep", ProjectID: projectID, Title: "deep", Prompt: "deep prompt", Status: types.StatusBacklog, ParentTaskID: "st-par-nest-c"}
	if err := te.store.CreateTask(deep); err != nil {
		t.Fatal(err)
	}
	if err := te.store.SetConfigValue("run_siblings_parallel", "true"); err != nil {
		t.Fatal(err)
	}
	children, err := te.store.GetChildren(parent.ID)
	if err != nil {
		t.Fatal(err)
	}

	ids := te.subtasksToStart(parent, children)
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	for _, want := range []string{"st-par-nest-a", "st-par-nest-b", deep.ID} {
		if !got[want] {
			t.Errorf("%s should start in parallel mode, got %v", want, ids)
		}
	}
	if got["st-par-nest-c"] {
		t.Errorf("st-par-nest-c must not start while %s is unfinished, got %v", deep.ID, ids)
	}
}

// StartReadySubtasks is what a start route calls. Every ready branch starts, and the one already
// queued for a slot does not.
func TestStartReadySubtasks_StartsReadyBranchesAndLeavesQueuedAlone(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	if err := te.store.SetConfigValue("run_siblings_parallel", "true"); err != nil {
		t.Fatal(err)
	}
	parent := threeLeafParent(t, te, projectID, "srs-parent")
	queuedID := parent.ID + "-a"
	defer queueSubtaskForSlot(t, te, projectID, queuedID)()
	t.Cleanup(func() {
		for _, id := range []string{parent.ID + "-b", parent.ID + "-c", parent.ID} {
			_ = te.StopTask(id)
		}
	})

	started, firstErr := te.StartReadySubtasks(projectID, parent.ID)
	if firstErr != nil {
		t.Fatalf("StartReadySubtasks failed: %v", firstErr)
	}
	got := map[string]bool{}
	for _, id := range started {
		got[id] = true
	}
	for _, want := range []string{parent.ID + "-b", parent.ID + "-c"} {
		if !got[want] {
			t.Errorf("%s should have started, got %v", want, started)
		}
	}
	if got[queuedID] {
		t.Errorf("queued subtask %s was started a second time", queuedID)
	}
	reloaded, _ := te.store.GetTaskByID(queuedID)
	if reloaded.Autostarted {
		t.Errorf("queued subtask %s was started again", queuedID)
	}
}

// queueSubtaskForSlot leaves a task as one waiting for a provider slot: a live executor, and the
// in_progress+idle row acquireTaskSlot writes while it waits. The returned function cancels it.
func queueSubtaskForSlot(t *testing.T, te *TaskExecutor, projectID, taskID string) func() {
	t.Helper()
	setup, err := te.prepareTaskExecution(projectID, taskID, "", "", true)
	if err != nil {
		t.Fatalf("prepareTaskExecution failed: %v", err)
	}
	row, err := te.store.GetTaskByID(taskID)
	if err != nil {
		t.Fatal(err)
	}
	row.Status = types.StatusInProgress
	row.ExecutionState = types.ExecStateIdle
	if err := te.store.UpdateTask(row); err != nil {
		t.Fatal(err)
	}
	return setup.cancel
}

// liveExecutor returns the in-flight registry entry for a task, nil when it has none.
func liveExecutor(te *TaskExecutor, taskID string) *executionState {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	return te.tasks[taskID]
}

type parallelChainFixture struct {
	parentID, finishedID, leafID, waitID, deepID string
}

// newParallelChainFixture makes the tree the sibling scheduler is judged on: a parent with a finished
// child, a ready leaf sibling, and a sibling waiting on its own ready child.
func newParallelChainFixture(t *testing.T, te *TaskExecutor, projectID string) parallelChainFixture {
	t.Helper()
	fx := parallelChainFixture{
		parentID:   "pc-parent",
		finishedID: "pc-finished",
		leafID:     "pc-leaf",
		waitID:     "pc-waiting",
		deepID:     "pc-deep",
	}
	tasks := []*types.Task{
		{ID: fx.parentID, ProjectID: projectID, Title: "P", Prompt: "p", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, CurrentThreadID: "thread-pc-parent", LlamaServerURL: "http://localhost:8080", ModelName: "test-model"},
		{ID: fx.finishedID, ProjectID: projectID, Title: "finished", Prompt: "f", Status: types.StatusReview, ExecutionState: types.ExecStateIdle, ParentTaskID: fx.parentID, CurrentThreadID: "thread-pc-finished", LlamaServerURL: "http://localhost:8080", ModelName: "test-model"},
		{ID: fx.leafID, ProjectID: projectID, Title: "leaf", Prompt: "l", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ParentTaskID: fx.parentID, CurrentThreadID: "thread-pc-leaf", LlamaServerURL: "http://localhost:8080", ModelName: "test-model"},
		{ID: fx.waitID, ProjectID: projectID, Title: "waiting", Prompt: "w", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ParentTaskID: fx.parentID, CurrentThreadID: "thread-pc-waiting", LlamaServerURL: "http://localhost:8080", ModelName: "test-model"},
		{ID: fx.deepID, ProjectID: projectID, Title: "deep", Prompt: "d", Status: types.StatusBacklog, ParentTaskID: fx.waitID, CurrentThreadID: "thread-pc-deep", LlamaServerURL: "http://localhost:8080", ModelName: "test-model"},
	}
	for _, tk := range tasks {
		if err := te.store.CreateTask(tk); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, id := range []string{fx.leafID, fx.deepID, fx.waitID, fx.parentID} {
			_ = te.StopTask(id)
		}
	})
	return fx
}

// TestAllChildrenDone covers the readiness predicate: no children, all done,
// one in review, one in backlog.
func TestAllChildrenDone(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	parentID := "parent-child-done"
	if err := te.store.CreateTask(&types.Task{ID: parentID, ProjectID: projectID, Title: "P", Prompt: "p", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle}); err != nil {
		t.Fatal(err)
	}

	// No children — vacuously done.
	if ok, err := te.AllChildrenDone(parentID); err != nil || !ok {
		t.Fatalf("no children: ok=%v err=%v, want true", ok, err)
	}

	children := []*types.Task{
		{ID: "cd-1", ProjectID: projectID, Title: "A", Prompt: "a", Status: types.StatusDone, ExecutionState: types.ExecStateCompleted, ParentTaskID: parentID},
		{ID: "cd-2", ProjectID: projectID, Title: "B", Prompt: "b", Status: types.StatusDone, ExecutionState: types.ExecStateCompleted, ParentTaskID: parentID},
	}
	for _, tk := range children {
		if err := te.store.CreateTask(tk); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := te.AllChildrenDone(parentID); !ok {
		t.Fatal("all children done: ok=false, want true")
	}

	// One child in review — not done.
	tk, _ := te.store.GetTaskByID("cd-2")
	tk.Status = types.StatusReview
	tk.ExecutionState = types.ExecStateIdle
	te.store.UpdateTask(tk)
	if ok, _ := te.AllChildrenDone(parentID); ok {
		t.Fatal("child in review: ok=true, want false")
	}

	// One child in backlog — not done.
	tk.Status = types.StatusBacklog
	te.store.UpdateTask(tk)
	if ok, _ := te.AllChildrenDone(parentID); ok {
		t.Fatal("child in backlog: ok=true, want false")
	}
}

// TestAutoStartNextSibling_StartsParentWhenAllChildrenDone verifies that when
// all of an in_progress parent's children are done, the parent itself is
// autostarted.
func TestAutoStartNextSibling_StartsParentWhenAllChildrenDone(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	parentID := "parent-start-when-done"
	parent := &types.Task{
		ID: parentID, ProjectID: projectID, Title: "Parent", Prompt: "Parent prompt",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, CurrentThreadID: "thread-psd",
		LlamaServerURL: "http://localhost:8080", ModelName: "test-model",
	}
	te.store.CreateTask(parent)
	seedThread(t, te.store, parentID, "thread-psd", "Parent prompt")

	for _, id := range []string{"s1-done", "s2-done"} {
		te.store.CreateTask(&types.Task{
			ID: id, ProjectID: projectID, Title: id, Prompt: "sub",
			Status: types.StatusDone, ExecutionState: types.ExecStateCompleted, ParentTaskID: parentID,
		})
	}

	// s1 "just finished"; s2 is already done → the parent must start.
	err := te.AutoStartNextSibling(projectID, "s1-done", parentID)
	if err != nil {
		t.Fatalf("AutoStartNextSibling failed: %v", err)
	}

	p, _ := te.store.GetTaskByID(parentID)
	if p.Status != types.StatusReview || p.ExecutionState != types.ExecStateIdle {
		t.Errorf("parent with nothing to run should wait in review after all children done: status=%q state=%q", p.Status, p.ExecutionState)
	}
	if !p.Autostarted {
		t.Error("parent should be marked autostarted when started by the chain")
	}
}

// TestAutoStartNextSibling_ReviewBlocksChainThenUnblocks verifies the chain
// with a review gate (G → P → C1 → R1, plus G → C2): R1's review blocks C1
// but bubbles up to start C2; R1 → Done unblocks C1; C1 → Done starts P;
// P and C2 done → G starts (the root runs last).
func TestAutoStartNextSibling_ReviewBlocksChainThenUnblocks(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)

	gID, pID, c1ID, c2ID, r1ID := "g-root", "p-mid", "c1-mid2", "c2-side", "r1-leaf"
	tasks := []*types.Task{
		{ID: gID, ProjectID: projectID, Title: "G", Prompt: "G prompt", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, CurrentThreadID: "thread-g", LlamaServerURL: "http://localhost:8080", ModelName: "test-model"},
		{ID: pID, ProjectID: projectID, Title: "P", Prompt: "P prompt", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ParentTaskID: gID, CurrentThreadID: "thread-p", LlamaServerURL: "http://localhost:8080", ModelName: "test-model"},
		{ID: c1ID, ProjectID: projectID, Title: "C1", Prompt: "C1 prompt", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ParentTaskID: pID, CurrentThreadID: "thread-c1", LlamaServerURL: "http://localhost:8080", ModelName: "test-model"},
		{ID: c2ID, ProjectID: projectID, Title: "C2", Prompt: "C2 prompt", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ParentTaskID: gID, CurrentThreadID: "thread-c2", LlamaServerURL: "http://localhost:8080", ModelName: "test-model"},
		{ID: r1ID, ProjectID: projectID, Title: "R1", Prompt: "R1 prompt", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ParentTaskID: c1ID},
	}
	for _, tk := range tasks {
		if err := te.store.CreateTask(tk); err != nil {
			t.Fatal(err)
		}
	}
	for id, thread := range map[string]string{gID: "thread-g", pID: "thread-p", c1ID: "thread-c1", c2ID: "thread-c2"} {
		seedThread(t, te.store, id, thread, "prompt")
	}

	// R1 finishes without auto-merge → Review (human gate).
	r1, _ := te.store.GetTaskByID(r1ID)
	r1.Status = types.StatusReview
	r1.ExecutionState = types.ExecStateIdle
	te.store.UpdateTask(r1)

	// 1) C1 and P must NOT start (blocked on R1 in review); G's other branch
	//    (C2) is started via bubble-up.
	if err := te.AutoStartNextSibling(projectID, r1ID, c1ID); err != nil {
		t.Fatalf("AutoStartNextSibling (review) failed: %v", err)
	}
	c1, _ := te.store.GetTaskByID(c1ID)
	if c1.ExecutionState == types.ExecStateRunning {
		t.Fatal("C1 must not start while its child R1 is in review")
	}
	p, _ := te.store.GetTaskByID(pID)
	if p.ExecutionState == types.ExecStateRunning {
		t.Fatal("P must not start while its subtree is blocked on review")
	}
	c2, _ := te.store.GetTaskByID(c2ID)
	if c2.Status != types.StatusReview || c2.ExecutionState != types.ExecStateIdle || !c2.Autostarted {
		t.Fatalf("C2 (G's other branch) should have started via bubble-up: status=%q state=%q autostarted=%v", c2.Status, c2.ExecutionState, c2.Autostarted)
	}

	// 2) R1 moved to Done → C1 starts.
	r1, _ = te.store.GetTaskByID(r1ID)
	r1.Status = types.StatusDone
	r1.ExecutionState = types.ExecStateCompleted
	te.store.UpdateTask(r1)
	if err := te.AutoStartNextSibling(projectID, r1ID, c1ID); err != nil {
		t.Fatalf("AutoStartNextSibling (r1 done) failed: %v", err)
	}
	c1, _ = te.store.GetTaskByID(c1ID)
	if c1.Status != types.StatusReview || c1.ExecutionState != types.ExecStateIdle || !c1.Autostarted {
		t.Fatalf("C1 should start (autostarted) once R1 is done: status=%q state=%q autostarted=%v", c1.Status, c1.ExecutionState, c1.Autostarted)
	}
	p, _ = te.store.GetTaskByID(pID)
	if p.ExecutionState == types.ExecStateRunning {
		t.Fatal("P must not start while C1 is still running")
	}

	// 3) C1 done → P starts.
	c1, _ = te.store.GetTaskByID(c1ID)
	c1.Status = types.StatusDone
	c1.ExecutionState = types.ExecStateCompleted
	te.store.UpdateTask(c1)
	if err := te.AutoStartNextSibling(projectID, c1ID, pID); err != nil {
		t.Fatalf("AutoStartNextSibling (c1 done) failed: %v", err)
	}
	p, _ = te.store.GetTaskByID(pID)
	if p.Status != types.StatusReview || p.ExecutionState != types.ExecStateIdle || !p.Autostarted {
		t.Fatalf("P should start (autostarted) once C1 is done: status=%q state=%q autostarted=%v", p.Status, p.ExecutionState, p.Autostarted)
	}

	// 4) C2 done and P done → G starts (root runs last).
	c2, _ = te.store.GetTaskByID(c2ID)
	c2.Status = types.StatusDone
	c2.ExecutionState = types.ExecStateCompleted
	te.store.UpdateTask(c2)
	p, _ = te.store.GetTaskByID(pID)
	p.Status = types.StatusDone
	p.ExecutionState = types.ExecStateCompleted
	te.store.UpdateTask(p)
	if err := te.AutoStartNextSibling(projectID, pID, gID); err != nil {
		t.Fatalf("AutoStartNextSibling (p done) failed: %v", err)
	}
	g, _ := te.store.GetTaskByID(gID)
	if g.Status != types.StatusReview || g.ExecutionState != types.ExecStateIdle || !g.Autostarted {
		t.Fatalf("G should start (autostarted) once all children are done: status=%q state=%q autostarted=%v", g.Status, g.ExecutionState, g.Autostarted)
	}
}

func idsOf(tasks []types.Task) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.ID)
	}
	return out
}

// hasLiveExecutor must reflect the presence of an active executor for a task
// (either running or blocked waiting for a concurrency slot).
func TestHasLiveExecutor(t *testing.T) {
	te, _, _, _ := setupExecutorWithGit(t)

	if te.hasLiveExecutor("nope") {
		t.Fatal("expected no live executor for unknown task")
	}
	te.taskMu.Lock()
	te.tasks["live-task"] = &executionState{}
	te.taskMu.Unlock()
	if !te.hasLiveExecutor("live-task") {
		t.Fatal("expected live executor for present task")
	}
	te.taskMu.Lock()
	delete(te.tasks, "live-task")
	te.taskMu.Unlock()
	if te.hasLiveExecutor("live-task") {
		t.Fatal("expected no live executor after removal")
	}
}

// In parallel mode a sibling that was already started shows as in_progress+idle
// but still holds a live executor (queued for a slot). AutoStartNextSibling must
// NOT restart such a sibling — it would cancel and requeue it, dropping its
// position. This test proves the hasLiveExecutor guard skips them.
func TestAutoStartNextSibling_SkipsLiveExecutorSiblings(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)

	parentID := "psl-parent"
	// Parent in_progress, no LLM configured (so a hypothetical restart would
	// fall back to the first sibling without any network call).
	parent := &types.Task{ID: parentID, ProjectID: projectID, Title: "P", Prompt: "p", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle}
	te.store.CreateTask(parent)

	a := &types.Task{ID: "psl-a", ProjectID: projectID, Title: "A", Prompt: "a", Status: types.StatusReview, ExecutionState: types.ExecStateIdle, ParentTaskID: parentID}
	b := &types.Task{ID: "psl-b", ProjectID: projectID, Title: "B", Prompt: "b", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ParentTaskID: parentID}
	c := &types.Task{ID: "psl-c", ProjectID: projectID, Title: "C", Prompt: "c", Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle, ParentTaskID: parentID}
	te.store.CreateTask(a)
	te.store.CreateTask(b)
	te.store.CreateTask(c)

	// Simulate B and C having been started (queued) — they hold live executors.
	te.taskMu.Lock()
	te.tasks[b.ID] = &executionState{}
	te.tasks[c.ID] = &executionState{}
	te.taskMu.Unlock()

	// A finished: continue the chain. B and C must NOT be restarted.
	if err := te.AutoStartNextSibling(projectID, a.ID, parentID); err != nil {
		t.Fatalf("AutoStartNextSibling: %v", err)
	}

	for _, id := range []string{b.ID, c.ID} {
		db, _ := te.store.GetTaskByID(id)
		if db.ExecutionState != types.ExecStateIdle {
			t.Fatalf("sibling %s was restarted (exec state %q); expected it to stay idle", id, db.ExecutionState)
		}
	}
}

// TestResolveMergeTarget_ChildNeverAnswersItsOwnBaseRef: a base_ref left on a child must not be what
// the board, the Review message or the commit-and-done line reports as its target.
func TestResolveMergeTarget_ChildNeverAnswersItsOwnBaseRef(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	if err := te.store.UpdateProjectOriginBranch(projectID, "master"); err != nil {
		t.Fatalf("set origin branch: %v", err)
	}

	now := time.Now().UTC()
	parent := &types.Task{
		ID:             "stale-target-parent",
		ProjectID:      projectID,
		Title:          "Parent",
		Status:         types.StatusBacklog,
		Branch:         "task/stale-target-parent-x7q2",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := te.store.CreateTask(parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child := &types.Task{
		ID:             "stale-target-child",
		ProjectID:      projectID,
		Title:          "Child",
		Status:         types.StatusInProgress,
		ParentTaskID:   parent.ID,
		BaseRef:        "release",
		BaseCommit:     "0123456789abcdef0123456789abcdef01234567",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := te.store.CreateTask(child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	if got := te.ResolveMergeTarget(projectID, child); got != parent.Branch {
		t.Errorf("merge target = %q, want the parent's branch %q", got, parent.Branch)
	}

	// mergeInto creates the parent's branch later, so an unbranched parent reads as the origin branch.
	parent.Branch = ""
	if err := te.store.UpdateTask(parent); err != nil {
		t.Fatalf("clear parent branch: %v", err)
	}
	if got := te.ResolveMergeTarget(projectID, child); got != "master" {
		t.Errorf("merge target with an unbranched parent = %q, want %q", got, "master")
	}
}
