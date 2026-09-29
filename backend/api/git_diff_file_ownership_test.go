package api

import (
	"net/http"
	"strings"
	"testing"

	"mike/backend/types"
)

func TestHandleTaskDiffFileRefusesAnotherProjectsTask(t *testing.T) {
	t.Parallel()
	h, mux, _ := taskDiffFileFixture(t, "diffown-b", "diffown-task")
	cacheOneUntracked(h, "diffown-task", "notes.txt")

	// A second project, which does not own the task.
	otherPath := t.TempDir()
	initGitRepo(t, otherPath)
	if err := h.store.CreateProject(&types.Project{ID: "diffown-a", Name: "diffown-a", Path: otherPath}); err != nil {
		t.Fatal(err)
	}

	rr := doRequest(t, mux, http.MethodGet, "/api/projects/diffown-a/tasks/diffown-task/diff/file?path=notes.txt")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a task of another project: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "cached line") {
		t.Errorf("another project's cached diff was served: %s", rr.Body.String())
	}

	// The owning project still gets its entry.
	rr = doRequest(t, mux, http.MethodGet, "/api/projects/diffown-b/tasks/diffown-task/diff/file?path=notes.txt")
	if rr.Code != http.StatusOK {
		t.Fatalf("the owning project was refused: %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "cached line") {
		t.Errorf("the cached lines were not served to the owner: %s", rr.Body.String())
	}
}
