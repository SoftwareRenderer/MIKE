package exec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// newResolvedConflictFixture is a task worktree mid-sync-merge with TWO conflicted files, the model's
// resolution written for both, and the work that must not ride along on the resolution commit: a
// modification to a tracked file and an untracked file.
type resolvedConflict struct {
	te        *TaskExecutor
	repoDir   string
	projectID string
	taskID    string
	branch    string
	wtPath    string
}

const (
	resolvedShared = "line 1\ncombined task and master\nline 3\n"
	resolvedOther  = "line 1\ncombined elsewhere\nline 3\n"
	baseNote       = "committed note\n"
	modifiedNote   = "committed note\nthe model's unrelated edit\n"
)

func newResolvedConflictFixture(t *testing.T, taskID, branch string) resolvedConflict {
	t.Helper()
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	if err := te.store.CreateTask(&types.Task{
		ID: taskID, ProjectID: projectID, Title: "Resolve two conflicts", Prompt: "resolve",
		Status: types.StatusInProgress, Branch: branch, BaseRef: "master",
		LlamaServerURL: "http://localhost:8080", ModelName: "test-model",
		HasMergeConflict: true, CurrentThreadID: "thread-1",
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// The base holds both conflictable files plus one the model edits without conflicting.
	testgit.WriteFile(t, repoDir, "shared.txt", "line 1\nbase\nline 3\n")
	testgit.WriteFile(t, repoDir, "other.txt", "line 1\nbase\nline 3\n")
	testgit.WriteFile(t, repoDir, "tracked_note.txt", baseNote)
	testgit.Git(t, repoDir, "add", "-A")
	testgit.Git(t, repoDir, "commit", "-m", "base files")

	testgit.Git(t, repoDir, "checkout", "-b", branch)
	testgit.WriteFile(t, repoDir, "shared.txt", "line 1\ntask change\nline 3\n")
	testgit.WriteFile(t, repoDir, "other.txt", "line 1\ntask elsewhere\nline 3\n")
	testgit.Git(t, repoDir, "commit", "-qam", "task edits")
	testgit.Git(t, repoDir, "checkout", "master")

	wtPath, err := te.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", "--detach", wtPath, branch)
	testgit.Git(t, wtPath, "checkout", branch)

	// The target branch changes the same two lines, then is merged in.
	testgit.WriteFile(t, repoDir, "shared.txt", "line 1\nmaster change\nline 3\n")
	testgit.WriteFile(t, repoDir, "other.txt", "line 1\nmaster elsewhere\nline 3\n")
	testgit.Git(t, repoDir, "commit", "-qam", "master edits")
	if res := runGitCmd(wtPath, "merge", "master", "--no-edit"); res.exitCode == 0 {
		t.Fatal("fixture: the merge should have conflicted")
	}
	if !isMergeInProgress(wtPath) {
		t.Fatal("fixture: the task worktree should be mid-merge with conflicts")
	}
	if got := GetConflictedFiles(wtPath); len(got) != 2 {
		t.Fatalf("fixture: two files should be conflicted, got %v", got)
	}

	// The model resolves both, edits an unrelated tracked file, and leaves a new file behind.
	testgit.WriteFile(t, wtPath, "shared.txt", resolvedShared)
	testgit.WriteFile(t, wtPath, "other.txt", resolvedOther)
	testgit.WriteFile(t, wtPath, "tracked_note.txt", modifiedNote)
	testgit.WriteFile(t, wtPath, "untracked.txt", "left behind\n")

	return resolvedConflict{te: te, repoDir: repoDir, projectID: projectID, taskID: taskID, branch: branch, wtPath: wtPath}
}

// TestCommitMergeConflictResolution_RecordsOnlyTheResolution: the commit that ends a conflicted
// merge carries the resolved files and nothing else. Sweeping untracked or unrelated tracked work
// here would commit what the person never asked to keep, and a resolution that is not the merge
// commit leaves the branch unmerged while the task reads as resolved.
func TestCommitMergeConflictResolution_RecordsOnlyTheResolution(t *testing.T) {
	t.Parallel()
	fx := newResolvedConflictFixture(t, "stage-only-task", "task/stage-only")

	if !fx.te.merge.stageResolvedConflicts(fx.taskID, fx.wtPath) {
		t.Fatal("stageResolvedConflicts failed")
	}
	if !fx.te.merge.commitMergeConflictResolution(fx.taskID, fx.wtPath) {
		t.Fatal("commitMergeConflictResolution failed")
	}

	if isMergeInProgress(fx.wtPath) {
		t.Error("the merge is still in progress after the resolution commit")
	}
	if got := strings.TrimSpace(runGitCmd(fx.wtPath, "log", "-1", "--format=%s").output); got != "Resolve merge conflicts" {
		t.Errorf("resolution commit subject = %q, want %q", got, "Resolve merge conflicts")
	}
	// The resolution completes the merge, so it has both parents.
	if parents := strings.Count(runGitCmd(fx.wtPath, "cat-file", "-p", "HEAD").output, "parent "); parents != 2 {
		t.Errorf("resolution commit has %d parents, want 2 (a merge commit)", parents)
	}

	// Only the two resolved files are in it.
	listed := strings.Fields(runGitCmd(fx.wtPath, "show", "--name-only", "--format=", "HEAD").output)
	if len(listed) != 2 || !strings.Contains(strings.Join(listed, " "), "shared.txt") ||
		!strings.Contains(strings.Join(listed, " "), "other.txt") {
		t.Errorf("resolution commit touches %q, want just the two resolved files", listed)
	}

	// The resolved content is on the branch; the unrelated work is not.
	if out := runGitCmd(fx.repoDir, "show", fx.branch+":shared.txt"); !strings.Contains(out.output, "combined task and master") {
		t.Errorf("the branch lacks the resolution: %s", out.output)
	}
	if out := runGitCmd(fx.repoDir, "show", fx.branch+":tracked_note.txt"); out.output != baseNote {
		t.Errorf("the unrelated tracked edit was committed: %q", out.output)
	}
	if res := runGitCmd(fx.repoDir, "cat-file", "-e", fx.branch+":untracked.txt"); res.exitCode == 0 {
		t.Error("the untracked file was committed")
	}
	if _, err := os.Stat(filepath.Join(fx.wtPath, "untracked.txt")); err != nil {
		t.Error("the untracked file should still be in the worktree")
	}
	if status := runGitCmd(fx.wtPath, "status", "--porcelain").output; !strings.Contains(status, "untracked.txt") ||
		!strings.Contains(status, "tracked_note.txt") {
		t.Errorf("the work that stayed out should still be waiting in the worktree, status:\n%s", status)
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

// TestGetResolvedConflictedFiles_WaitsForEveryConflict: git will not make the merge commit until
// every conflicted file is clean, so a resolution that leaves markers anywhere — in the only
// conflicted file, or in one of several — reports nothing to stage. Committing a half-resolved
// file would put conflict markers on the branch.
func TestGetResolvedConflictedFiles_WaitsForEveryConflict(t *testing.T) {
	t.Parallel()
	const markers = "<<<<<<< HEAD\ntask version\n=======\nmaster version\n>>>>>>> master\n"

	tests := []struct {
		name    string
		resolve func(t *testing.T, wt string)
	}{
		{
			name: "the only conflicted file still has markers",
			resolve: func(t *testing.T, wt string) {
				testgit.WriteFile(t, wt, "shared.txt", markers)
			},
		},
		{
			name: "one of two conflicted files still has markers",
			resolve: func(t *testing.T, wt string) {
				testgit.WriteFile(t, wt, "shared.txt", syncResolved)
				testgit.WriteFile(t, wt, "other.txt", markers)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newResolvedConflictFixture(t, "unresolved-task", "task/unresolved")
			tc.resolve(t, fx.wtPath)

			if got := GetResolvedConflictedFiles(fx.wtPath); len(got) != 0 {
				t.Errorf("GetResolvedConflictedFiles = %v, want nothing to stage", got)
			}
			if GetAllConflictsResolved(fx.wtPath) {
				t.Error("GetAllConflictsResolved reported a clean tree while markers remain")
			}
			if !isMergeInProgress(fx.wtPath) {
				t.Error("the merge was abandoned although the resolution is not over")
			}
		})
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

// TestCommitConflictResolution_MergeFollowsTheClick: the click means commit, merge, Done, so a
// resolution has to reach the target branch even when auto_merge says no. With no click behind the
// conflict, auto_merge alone decides — false keeps the resolution local and hands the task back.
func TestCommitConflictResolution_MergeFollowsTheClick(t *testing.T) {
	t.Parallel()
	for _, clicked := range []bool{true, false} {
		name := "no_click_leaves_the_resolution_local"
		if clicked {
			name = "a_click_merges_whatever_auto_merge_says"
		}
		t.Run(name, func(t *testing.T) {
			fx := newSyncConflictFixture(t, "click-merge-task", "task/click-merge", false)
			os.WriteFile(filepath.Join(fx.wtPath, "shared.txt"), []byte(syncResolved), 0o644)
			if clicked {
				fx.te.ArmCommitAndDone(fx.taskID)
			}

			task, err := fx.te.store.GetTaskByID(fx.taskID)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			res := fx.te.merge.commitConflictResolution(fx.projectID, fx.taskID, fx.wtPath, task)

			if !res.committed {
				t.Fatal("the resolution was not committed")
			}
			if res.commitDone != clicked {
				t.Errorf("commitDone = %v, want %v", res.commitDone, clicked)
			}
			if res.finishesTask() != clicked {
				t.Errorf("finishesTask = %v, want %v", res.finishesTask(), clicked)
			}
			if clicked && res.mergeErr != nil {
				t.Fatalf("the click's merge failed: %v", res.mergeErr)
			}
			if res.task.HasMergeConflict {
				t.Error("the conflict flag should be cleared")
			}
			if fx.te.CommitAndDonePending(fx.taskID) {
				t.Error("the click survived the resolution it asked for")
			}
			// The point: the work reached the target branch only when the click asked for it.
			landed := strings.Contains(runGitCmd(fx.repoDir, "show", "master:shared.txt").output, "combined task and master")
			if landed != clicked {
				t.Errorf("master holds the resolution = %v, want %v:\n%s", landed, clicked,
					runGitCmd(fx.repoDir, "show", "master:shared.txt").output)
			}
		})
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
