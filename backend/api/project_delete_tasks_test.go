package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"mike/backend/sandbox"
	"mike/backend/store"
	"mike/backend/types"
)

// A deleted project's tasks own containers, worktrees and upload directories outside the database.
// These tests hold which tasks a project deletion names.

// recordingTaskDeleter answers the project handler's delete path with the batch it was handed.
type recordingTaskDeleter struct {
	batches [][]string
}

func (r *recordingTaskDeleter) deleteTaskTrees(projectID string, taskIDs ...string) {
	r.batches = append(r.batches, append([]string{projectID}, taskIDs...))
}

func deleteProject(t *testing.T, h *ProjectHandler, projectID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/projects/"+projectID, nil)
	req.SetPathValue("projectID", projectID)
	rr := httptest.NewRecorder()
	h.HandleByID(rr, req)
	return rr
}

// seedTaskTree creates a root with a child and a grandchild, and returns all three.
func seedTaskTree(t *testing.T, s *store.Store, projectID string) (root, child, grandchild string) {
	t.Helper()
	for _, spec := range []struct{ id, parent string }{
		{"root-task", ""},
		{"child-task", "root-task"},
		{"grandchild-task", "child-task"},
	} {
		task := &types.Task{ID: spec.id, ProjectID: projectID, Title: spec.id, Status: types.StatusBacklog, ParentTaskID: spec.parent}
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create task %s: %v", spec.id, err)
		}
	}
	return "root-task", "child-task", "grandchild-task"
}

// Naming only the roots is complete only while a parent is always one of these rows. It is:
// parent_task_id is a foreign key.
func TestATaskCannotNameAParentThatIsAbsent(t *testing.T) {
	_, projID, s := testProjectHandlerWithStore(t)
	err := s.CreateTask(&types.Task{
		ID: "orphan-task", ProjectID: projID, Title: "orphan",
		Status: types.StatusBacklog, ParentTaskID: "a-parent-that-is-gone",
	})
	if err == nil {
		t.Error("a task stored with a parent that does not exist; naming only the roots would skip this one")
	}
}

func TestProjectDeleteNamesOnlyTheRootTasks(t *testing.T) {
	h, projID, s := testProjectHandlerWithStore(t)
	recorder := &recordingTaskDeleter{}
	h.tasks = recorder
	_, child, grandchild := seedTaskTree(t, s, projID)

	rr := deleteProject(t, h, projID)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete project = %d: %s", rr.Code, rr.Body.String())
	}

	if len(recorder.batches) != 1 {
		t.Fatalf("task teardown ran %d batches, want one for the project", len(recorder.batches))
	}
	batch := recorder.batches[0]
	if batch[0] != projID {
		t.Errorf("teardown named project %q, want %q", batch[0], projID)
	}
	got := map[string]bool{}
	for _, id := range batch[1:] {
		got[id] = true
	}
	if !got["root-task"] {
		t.Errorf("the root was not deleted; batch was %v", batch[1:])
	}
	for _, unwanted := range []string{child, grandchild} {
		if got[unwanted] {
			t.Errorf("%s was deleted on its own, so its subtree is torn down twice", unwanted)
		}
	}
	if _, err := s.GetProject(projID); err == nil {
		t.Error("the project row survived the delete")
	}
}

// Without the task path the row still goes — refusing would strand the project — and the WARN is the
// only signal that those tasks' containers are still running.
func TestProjectDeleteWithoutTaskPathStillDeletesTheRow(t *testing.T) {
	h, projID, s := testProjectHandlerWithStore(t)
	seedTaskTree(t, s, projID)

	rr := deleteProject(t, h, projID)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete project with no task path = %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := s.GetProject(projID); err == nil {
		t.Error("the project row survived the delete")
	}
	tasks, err := s.GetTasksByProject(projID)
	if err != nil {
		t.Fatal(err)
	}
	// The rows go with the project by CASCADE; the containers and worktrees are what nobody owns.
	if len(tasks) != 0 {
		t.Errorf("%d task rows survived the CASCADE", len(tasks))
	}
}

// recordingSandbox answers the project handler's sandbox calls with the batches it was handed.
type recordingSandbox struct {
	stoppedProjects []string
}

func (r *recordingSandbox) BuildImageWithOptions(string, string, sandbox.BuildOptions, func(string)) error {
	return nil
}

func (r *recordingSandbox) StopProjectContainers(projectID string, _ []string) {
	r.stoppedProjects = append(r.stoppedProjects, projectID)
}

// The project's own container is keyed "proj:"+projectID, and a file-browser listing starts it with no
// task and no terminal open: a delete that skips it leaves it running on a checkout with no rows left.
func TestProjectDeleteStopsTheProjectsOwnSandboxContainer(t *testing.T) {
	h, projID, s := testProjectHandlerWithStore(t)
	h.tasks = &recordingTaskDeleter{}
	recorder := &recordingSandbox{}
	h.sandboxMgr = recorder
	seedTaskTree(t, s, projID)

	if rr := deleteProject(t, h, projID); rr.Code != http.StatusNoContent {
		t.Fatalf("delete project = %d: %s", rr.Code, rr.Body.String())
	}
	if len(recorder.stoppedProjects) != 1 {
		t.Fatalf("project containers were stopped for %v, want this project once", recorder.stoppedProjects)
	}
	if recorder.stoppedProjects[0] != projID {
		t.Errorf("containers stopped for %q, want %q", recorder.stoppedProjects[0], projID)
	}
}

// With nothing to delete there is no batch, so no closing board broadcast either.
func TestProjectDeleteOfAnEmptyProjectAsksForNoTaskTeardown(t *testing.T) {
	h, projID, _ := testProjectHandlerWithStore(t)
	recorder := &recordingTaskDeleter{}
	h.tasks = recorder

	if rr := deleteProject(t, h, projID); rr.Code != http.StatusNoContent {
		t.Fatalf("delete empty project = %d: %s", rr.Code, rr.Body.String())
	}
	if len(recorder.batches) != 0 {
		t.Errorf("an empty project handed tasks something to delete: %v", recorder.batches)
	}
}
