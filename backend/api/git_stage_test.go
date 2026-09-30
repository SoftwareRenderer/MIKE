package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// What a batch stage answers — which paths moved, which were refused — asserted against a real repository.

// stageFixture serves the project stage route over a repo holding one committed file, README.md.
func stageFixture(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	h, s, _, _ := newTestGitHandler(t)
	repoPath := testgit.Repo(t)
	proj := &types.Project{ID: "stage-proj", Name: "stage-proj", Path: repoPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/git/stage", h.HandleProjectStagePaths)
	return mux, repoPath
}

// postStage names every path in one request, as the panel does.
func postStage(t *testing.T, mux *http.ServeMux, stage bool, paths ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := newStageRequest(t, "/api/projects/stage-proj/git/stage", stage, paths...)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// answerPaths is the answer's applied list, sorted.
func answerPaths(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	applied := append([]string(nil), decodeBatchAnswer(t, rec).Applied...)
	sort.Strings(applied)
	return applied
}

func TestHandleProjectStagePaths_StagesEveryPathInOneRequest(t *testing.T) {
	mux, repoPath := stageFixture(t)
	testgit.WriteFile(t, repoPath, "a.txt", "a\n")
	testgit.WriteFile(t, repoPath, "nested/b.txt", "b\n")
	testgit.WriteFile(t, repoPath, "README.md", "# Edited\n")

	rec := postStage(t, mux, true, "a.txt", "nested/b.txt", "README.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, want := answerPaths(t, rec), []string{"README.md", "a.txt", "nested/b.txt"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("applied = %v, want %v", got, want)
	}
	if staged := stagedFiles(t, repoPath); len(staged) != 3 {
		t.Errorf("the index holds %v, want all three", staged)
	}
}

func TestHandleProjectStagePaths_UnstagesEveryPathInOneRequest(t *testing.T) {
	mux, repoPath := stageFixture(t)
	testgit.WriteFile(t, repoPath, "a.txt", "a\n")
	testgit.WriteFile(t, repoPath, "b.txt", "b\n")
	testgit.Git(t, repoPath, "add", "--", "a.txt", "b.txt")
	testgit.WriteFile(t, repoPath, "c.txt", "never staged\n")

	rec := postStage(t, mux, false, "a.txt", "b.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, want := answerPaths(t, rec), []string{"a.txt", "b.txt"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("applied = %v, want %v", got, want)
	}
	if staged := stagedFiles(t, repoPath); len(staged) != 0 {
		t.Errorf("the index still holds %v", staged)
	}
	// Unstaging leaves the worktree alone: both files are still on disk, edited or not.
	for _, name := range []string{"a.txt", "b.txt"} {
		if readFileOrFail(t, repoPath, name) == "" {
			t.Errorf("%s was emptied", name)
		}
	}
}

// git refuses `add` outright when any pathspec matches nothing, so a path the repository no longer holds
// must cost only itself.
func TestHandleProjectStagePaths_OneRefusedPathDoesNotCostTheSelection(t *testing.T) {
	mux, repoPath := stageFixture(t)
	testgit.WriteFile(t, repoPath, "here.txt", "here\n")

	rec := postStage(t, mux, true, "here.txt", "gone-since-the-panel-read-git.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	answer := decodeBatchAnswer(t, rec)
	if strings.Join(answer.Applied, " ") != "here.txt" {
		t.Errorf("applied = %v, want just here.txt", answer.Applied)
	}
	if len(answer.Failed) != 1 || answer.Failed[0].Path != "gone-since-the-panel-read-git.txt" {
		t.Errorf("failed = %v, want just the path git has no such name for", answer.Failed)
	}
	// The refusal names MIKE's own sentence; git's words stay in the log.
	if len(answer.Failed) == 1 && !strings.HasPrefix(answer.Failed[0].Error, "git add failed") {
		t.Errorf("failed error = %q", answer.Failed[0].Error)
	}
	if staged := stagedFiles(t, repoPath); len(staged) != 1 || staged[0] != "here.txt" {
		t.Errorf("the index holds %v, want just here.txt", staged)
	}
}

// Unstaging a tracked file deleted on disk brings it back from HEAD, whatever else the request moved.
func TestHandleProjectStagePaths_UnstageRestoresADeletedFile(t *testing.T) {
	mux, repoPath := stageFixture(t)
	testgit.Git(t, repoPath, "rm", "--cached", "README.md")
	if err := os.Remove(filepath.Join(repoPath, "README.md")); err != nil {
		t.Fatalf("delete README.md: %v", err)
	}

	rec := postStage(t, mux, false, "README.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := readFileOrFail(t, repoPath, "README.md"); got != "# Test\n" {
		t.Errorf("README.md came back as %q", got)
	}
}

// An empty selection is a caller error naming its field, and a blank entry is the same.
func TestHandleProjectStagePaths_RejectsAPathListThatNamesNothing(t *testing.T) {
	mux, _ := stageFixture(t)
	for _, body := range []string{`{"stage": true}`, `{"paths": [], "stage": true}`, `{"paths": [""], "stage": true}`} {
		req := httptest.NewRequest("POST", "/api/projects/stage-proj/git/stage", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d: %s", body, rec.Code, rec.Body.String())
		}
	}
}

// The worktree handler answers the same batch against the task's own checkout.
func TestHandleStagePaths_StagesEveryPathInOneRequest(t *testing.T) {
	h, s, wm, _ := newTestGitHandler(t)
	projPath := testgit.Repo(t)
	if err := s.CreateProject(&types.Project{ID: "stage-proj", Name: "stage-proj", Path: projPath}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{ID: "stage-task", ProjectID: "stage-proj", Title: "stage", Status: types.StatusInProgress, Branch: "task/stage"}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	worktreePath, _, err := wm.CreateWorktree(task.ProjectID, task.ID, task.Branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	testgit.WriteFile(t, worktreePath, "one.txt", "1\n")
	testgit.WriteFile(t, worktreePath, "two.txt", "2\n")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/diff/stage", h.HandleStagePaths)
	req := newStageRequest(t, "/api/projects/stage-proj/tasks/stage-task/diff/stage", true, "one.txt", "two.txt")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, want := answerPaths(t, rec), []string{"one.txt", "two.txt"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("applied = %v, want %v", got, want)
	}
	if staged := stagedFiles(t, worktreePath); len(staged) != 2 {
		t.Errorf("the index holds %v, want both", staged)
	}
	// The project's own checkout is untouched — the batch ran in the worktree.
	if staged := stagedFiles(t, projPath); len(staged) != 0 {
		t.Errorf("the project repo holds %v", staged)
	}
}
