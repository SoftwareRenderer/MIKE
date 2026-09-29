package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Deleting a project has to give up each task's environment — worktree, branch, sandbox container —
// because CASCADE takes the rows and leaves all of that running. The uploads inside a worktree go with it:
// that is the whole of an upload's lifecycle, an upload is deleted with its task and with nothing else.
func TestProjectDeleteTearsDownEveryTaskEnvironment(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent")
	childID := createTestSubtask(t, s, projID, parentID, "child")

	wm := s.executor.WorktreeMgr()
	worktrees := map[string]string{}
	for _, taskID := range []string{parentID, childID} {
		branch := "task/" + taskID
		if _, _, err := wm.CreateWorktree(projID, taskID, branch, ""); err != nil {
			t.Fatalf("create worktree for %s: %v", taskID, err)
		}
		task, err := s.store.GetTask(projID, taskID)
		if err != nil {
			t.Fatal(err)
		}
		task.Branch = branch
		if err := s.store.UpdateTask(task); err != nil {
			t.Fatal(err)
		}
		path, err := wm.WorktreePath(taskID)
		if err != nil {
			t.Fatal(err)
		}
		worktrees[taskID] = path
	}

	// One dropped file per task, in the directory the upload path writes.
	uploads := map[string]string{}
	for _, taskID := range []string{parentID, childID} {
		dir := filepath.Join(worktrees[taskID], ".mike-uploads")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "dropped.pdf")
		if err := os.WriteFile(file, []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		uploads[taskID] = file
	}

	req := testRequest("DELETE", "/api/projects/"+projID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete project = %d: %s", rr.Code, rr.Body.String())
	}

	for _, taskID := range []string{parentID, childID} {
		if _, err := os.Stat(uploads[taskID]); !os.IsNotExist(err) {
			t.Errorf("%s: the task's upload survived its project (%v)", taskID, err)
		}
		if _, err := os.Stat(worktrees[taskID]); !os.IsNotExist(err) {
			t.Errorf("%s: the worktree directory survived its project (%v)", taskID, err)
		}
		if _, err := s.store.GetTask(projID, taskID); err == nil {
			t.Errorf("%s: the task row survived its project", taskID)
		}
	}

	if _, err := s.store.GetProject(projID); err == nil {
		t.Error("the project row survived the delete")
	}
}
