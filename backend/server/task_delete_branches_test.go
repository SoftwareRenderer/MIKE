package server

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// repoBranchNames lists the project repo's branches under prefix ("task/", "thread/"), undecorated.
func repoBranchNames(t *testing.T, repoDir, prefix string) []string {
	t.Helper()
	cmd := exec.Command("git", "for-each-ref", "--format=%(refname:short)", "refs/heads/"+prefix+"*")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list branches: %v\n%s", err, out)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if b := strings.TrimSpace(line); b != "" {
			names = append(names, b)
		}
	}
	return names
}

// repoWorktreeRegistrations counts the project repo's .git/worktrees entries.
func repoWorktreeRegistrations(t *testing.T, repoDir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoDir, ".git", "worktrees"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read worktree registrations: %v", err)
	}
	return len(entries)
}

// giveTaskWorktree creates the task's worktree and branch the way a started task does.
func giveTaskWorktree(t *testing.T, s *Server, projID, taskID string) {
	t.Helper()
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/init", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("initialize task: %d %s", rr.Code, rr.Body.String())
	}
	if branch, _ := getTaskField(t, s, projID, taskID, "branch").(string); branch == "" {
		t.Fatal("initialized task has no branch")
	}
}

func TestSingleTaskDeleteRemovesItsBranchAndWorktreeRegistration(t *testing.T) {
	s := testServer(t)
	projID, projDir := createTestProjectWithPath(t, s)
	taskID := createTestTask(t, s, projID, "delete-branch-probe")
	giveTaskWorktree(t, s, projID, taskID)

	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusNoContent {
		t.Fatalf("delete task: %d %s", rr.Code, rr.Body.String())
	}

	if left := repoBranchNames(t, projDir, "task/"); len(left) != 0 {
		t.Errorf("task branches left in the project after deleting its only task: %v", left)
	}
	if regs := repoWorktreeRegistrations(t, projDir); regs != 0 {
		t.Errorf("%d worktree registration(s) left after deleting the task", regs)
	}
}

func TestClearDoneRemovesEveryBranchAndRegistration(t *testing.T) {
	s := testServer(t)
	projID, projDir := createTestProjectWithPath(t, s)
	const done = 3
	for i := 0; i < done; i++ {
		taskID := createTestTask(t, s, projID, "clear-done-branch-probe")
		giveTaskWorktree(t, s, projID, taskID)
		moveTask(t, s, projID, taskID, "done")
	}

	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/done", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("clear done: %d %s", rr.Code, rr.Body.String())
	}
	if got := mustJSON[map[string]any](t, rr.Body)["deleted"]; got != float64(done) {
		t.Fatalf("deleted = %v, want %d", got, done)
	}

	if left := repoBranchNames(t, projDir, "task/"); len(left) != 0 {
		t.Errorf("task branches left after clearing %d done tasks: %v", done, left)
	}
	if regs := repoWorktreeRegistrations(t, projDir); regs != 0 {
		t.Errorf("%d worktree registration(s) left after clearing the column", regs)
	}
}

// A fork leaves the worktree on thread/<id> and stores that branch in task.branch, so the delete hits
// a branch the registration still holds. The inactive thread is the other half: the sweep walks every
// thread row the task owns, not just the one on screen, or a fork nobody is looking at keeps its branch.
func TestTaskDeleteRemovesForkThreadBranch(t *testing.T) {
	s := testServer(t)
	projID, projDir := createTestProjectWithPath(t, s)
	taskID := createTestTask(t, s, projID, "fork-branch-probe")
	giveTaskWorktree(t, s, projID, taskID)

	const threadID = "0f0thread0"
	threadBranch := "thread/" + threadID
	wt := taskWorktreeDir(t, projDir, taskID)
	testgit.Git(t, wt, "checkout", "-b", threadBranch)

	const dormantID = "1f0thread1"
	dormantBranch := "thread/" + dormantID
	testgit.Git(t, projDir, "branch", dormantBranch)

	task, err := s.store.GetTaskByID(taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Branch = threadBranch
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("record the fork branch on the task: %v", err)
	}
	for _, th := range []*types.TaskThread{
		{ID: threadID, TaskID: taskID, Name: "fork", IsActive: true, GitBranch: threadBranch, CreatedAt: time.Now().UTC()},
		{ID: dormantID, TaskID: taskID, Name: "fork nobody opened", IsActive: false, GitBranch: dormantBranch, CreatedAt: time.Now().UTC()},
	} {
		if err := s.store.CreateThread(th); err != nil {
			t.Fatalf("create thread row: %v", err)
		}
	}

	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusNoContent {
		t.Fatalf("delete task: %d %s", rr.Code, rr.Body.String())
	}

	if left := repoBranchNames(t, projDir, "thread/"); len(left) != 0 {
		t.Errorf("thread branches left after deleting the task that owned them: %v", left)
	}
	if left := repoBranchNames(t, projDir, "task/"); len(left) != 0 {
		t.Errorf("task branches left after deleting the task: %v", left)
	}
	if regs := repoWorktreeRegistrations(t, projDir); regs != 0 {
		t.Errorf("%d worktree registration(s) left after deleting the task", regs)
	}
}

// The branch sweep that runs with a delete names exactly the refs no remaining row holds. The deleted
// task's own branch and a task/* left by a run that died both go; a branch a live task's row still
// names stays, which is the half a sweep keyed on the prefix alone would get wrong. The registration
// goes with the directory, or git would refuse the branch as "used by worktree at …".
func TestTaskDeletePrunesTheBranchesNoRowHolds(t *testing.T) {
	s := testServer(t)
	projID, projDir := createTestProjectWithPath(t, s)
	deleting := createTestTask(t, s, projID, "sweep-deleted")
	keeping := createTestTask(t, s, projID, "sweep-kept")
	giveTaskWorktree(t, s, projID, deleting)

	testgit.Git(t, projDir, "branch", "task/orphan-abc-def-ghi") // no row names it
	testgit.Git(t, projDir, "branch", "task/"+keeping)           // this row does

	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+deleting, nil)
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusNoContent {
		t.Fatalf("delete task: %d %s", rr.Code, rr.Body.String())
	}

	if left := repoBranchNames(t, projDir, "task/"); !slices.Equal(left, []string{"task/" + keeping}) {
		t.Errorf("task branches left = %v, want only task/%s: the deleted task's own branch and the "+
			"orphan should both be gone, and a live task's branch should stay", left, keeping)
	}
	if regs := repoWorktreeRegistrations(t, projDir); regs != 0 {
		t.Errorf("%d worktree registration(s) left after deleting the task", regs)
	}
}

// taskWorktreeDir asks git where the task's worktree is rather than rebuilding the path.
func taskWorktreeDir(t *testing.T, projDir, taskID string) string {
	t.Helper()
	out := testgit.GitOutput(t, projDir, "worktree", "list", "--porcelain")
	var found string
	for _, line := range strings.Split(out, "\n") {
		if path := strings.TrimPrefix(line, "worktree "); path != line {
			if strings.HasSuffix(path, "/"+taskID) {
				found = path
			}
		}
	}
	if found == "" {
		t.Fatalf("no worktree for task %s in:\n%s", taskID, out)
	}
	return found
}
