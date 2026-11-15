package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// fsSearchGet sends one file search the way the task view's @ picker sends it: with a project and a task.
func fsSearchGet(t *testing.T, s *Server, projectID, taskID string) *httptest.ResponseRecorder {
	t.Helper()
	v := url.Values{"project": {projectID}, "task": {taskID}, "q": {""}}
	req := testRequest(http.MethodGet, "/api/fs/search?"+v.Encode(), nil)
	cookie, csrf := loginTestCSRF(t, s)
	withAuthCSRF(req, cookie, csrf)
	return testResponse(t, s, req)
}

// fsSearchTask sends the search and reports the directory the server walked and the file paths it returned.
func fsSearchTask(t *testing.T, s *Server, projectID, taskID string) (root string, paths []string) {
	t.Helper()
	rr := fsSearchGet(t, s, projectID, taskID)
	if rr.Code != http.StatusOK {
		t.Fatalf("search of task %s: %d %s", taskID, rr.Code, rr.Body.String())
	}
	answer := mustJSON[struct {
		Root    string `json:"root"`
		Results []struct {
			Path string `json:"path"`
		} `json:"results"`
	}](t, rr.Body)
	paths = make([]string, 0, len(answer.Results))
	for _, r := range answer.Results {
		paths = append(paths, r.Path)
	}
	return answer.Root, paths
}

// fsSearchProjectRoot is the project directory as this handler reports it. The handler answers with the path
// it accepted after resolving symlinks, so the test compares against that same resolved path.
func fsSearchProjectRoot(t *testing.T, repo string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve %s: %v", repo, err)
	}
	return resolved
}

// TestFSSearchOfATaskAnswersFromItsWorktree: the @ picker in a task's chat input asks for that task's files,
// so the answer has to come from the task's worktree. The file that exists only in the project directory is
// what shows the search really was limited to the worktree.
func TestFSSearchOfATaskAnswersFromItsWorktree(t *testing.T) {
	s := testServer(t)
	repo := testgit.Repo(t)
	const projID = "fs-search-worktree"
	if err := s.store.CreateProject(&types.Project{ID: projID, Name: "FS Search Worktree", Path: repo}); err != nil {
		t.Fatal(err)
	}
	// Written but never committed, so this file is in the project directory and in no worktree made from its
	// history.
	testgit.WriteFile(t, repo, "project-only.txt", "project\n")

	taskID := createTestTask(t, s, projID, "search the worktree")
	worktree, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	testgit.WriteFile(t, worktree, "worktree-only.txt", "worktree\n")

	root, paths := fsSearchTask(t, s, projID, taskID)
	if root != filepath.Clean(worktree) {
		t.Errorf("root = %q, want the task's worktree %q", root, filepath.Clean(worktree))
	}
	for _, want := range []string{"README.md", "worktree-only.txt"} {
		if !slices.Contains(paths, want) {
			t.Errorf("results = %v, want the worktree file %q", paths, want)
		}
	}
	if slices.Contains(paths, "project-only.txt") {
		t.Errorf("results = %v, want no file that only the project checkout holds", paths)
	}
}

// TestFSSearchOfATaskWithoutAWorktreeAnswersFromTheProject: a task that has never run has no worktree
// directory, because the executor creates one when the task starts. The only directory holding files that
// task can name is the project's.
func TestFSSearchOfATaskWithoutAWorktreeAnswersFromTheProject(t *testing.T) {
	s := testServer(t)
	repo := testgit.Repo(t)
	const projID = "fs-search-no-worktree"
	if err := s.store.CreateProject(&types.Project{ID: projID, Name: "FS Search No Worktree", Path: repo}); err != nil {
		t.Fatal(err)
	}
	testgit.WriteFile(t, repo, "project-only.txt", "project\n")

	taskID := createTestTask(t, s, projID, "search before starting")

	root, paths := fsSearchTask(t, s, projID, taskID)
	if want := fsSearchProjectRoot(t, repo); root != want {
		t.Errorf("root = %q, want the project checkout %q", root, want)
	}
	for _, want := range []string{"README.md", "project-only.txt"} {
		if !slices.Contains(paths, want) {
			t.Errorf("results = %v, want the project file %q", paths, want)
		}
	}
}

// TestFSSearchOfAMissingTaskIsRefused: a task is honoured only when it belongs to the project the same
// request names. Without that check, a request naming a task that does not exist would be answered with the
// project's own files, because a missing worktree falls back to that directory.
func TestFSSearchOfAMissingTaskIsRefused(t *testing.T) {
	s := testServer(t)
	repo := testgit.Repo(t)
	const projID = "fs-search-missing-task"
	if err := s.store.CreateProject(&types.Project{ID: projID, Name: "FS Search Missing Task", Path: repo}); err != nil {
		t.Fatal(err)
	}

	rr := fsSearchGet(t, s, projID, "no-such-task")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rr.Code, strings.TrimSpace(rr.Body.String()))
	}
}

// TestFSSearchOfATaskOfAnotherProjectIsRefused: the project and the task in a request arrive uncorrelated, so
// a request that pairs one project with another project's task has to be refused. This matters here because
// the handler answers some task requests with the requesting project's own files, so a request whose task was
// never checked could be answered instead of refused. A task of another project is refused exactly as a
// missing one is, so nobody can use the status code to ask whether a task exists.
func TestFSSearchOfATaskOfAnotherProjectIsRefused(t *testing.T) {
	s := testServer(t)
	repoA := testgit.Repo(t)
	if err := s.store.CreateProject(&types.Project{ID: "fs-search-a", Name: "FS Search A", Path: repoA}); err != nil {
		t.Fatal(err)
	}
	testgit.WriteFile(t, repoA, "project-a-only.txt", "a\n")
	if err := s.store.CreateProject(&types.Project{ID: "fs-search-b", Name: "FS Search B", Path: testgit.Repo(t)}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateTask(&types.Task{ID: "tb", ProjectID: "fs-search-b", Title: "tb", Status: types.StatusBacklog}); err != nil {
		t.Fatal(err)
	}

	rr := fsSearchGet(t, s, "fs-search-a", "tb")
	if rr.Code == http.StatusOK {
		t.Fatalf("a task of another project was honoured: %s", rr.Body.String())
	}
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rr.Code, strings.TrimSpace(rr.Body.String()))
	}

	// The same request against a task of another project that does have a worktree. The worktree directory is
	// found by task id alone, so a request whose task was never checked against its project is the one that
	// would answer with a different project's files.
	const foreignWorktreeTask = "tb-with-worktree"
	if err := s.store.CreateTask(&types.Task{ID: foreignWorktreeTask, ProjectID: "fs-search-b", Title: "tb2", Status: types.StatusInProgress}); err != nil {
		t.Fatal(err)
	}
	worktreeB, _, err := s.executor.WorktreeMgr().CreateWorktree("fs-search-b", foreignWorktreeTask, "task/"+foreignWorktreeTask, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	testgit.WriteFile(t, worktreeB, "project-b-only.txt", "b\n")

	if rr := fsSearchGet(t, s, "fs-search-a", foreignWorktreeTask); rr.Code != http.StatusNotFound {
		t.Fatalf("a worktree of another project was reached: status %d, %s", rr.Code, strings.TrimSpace(rr.Body.String()))
	}
}
