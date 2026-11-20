package api

// The submodule endpoints exist so a person can see what a task is waiting on and answer it. A listing that
// disagrees with the failure the task reported would send them looking in the wrong place, so both readings
// come out of the same walk in backend/git.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/testutil/testgit"
	"mike/backend/testutil/teststore"
	"mike/backend/types"
)

const (
	submoduleProjectID = "sub-proj"
	submoduleTaskID    = "sub-task"
)

// newSubmoduleHandler is a project handler over a real checkout, with the worktree manager the task-scoped
// read asks for and one task row to name in the path.
func newSubmoduleHandler(t *testing.T, projectPath string) *ProjectHandler {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	worktreesDir := filepath.Join(dir, "worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := teststore.StoreIn(t, dbPath)
	cfgStore := teststore.ConfigStore(t, s, dbPath)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)

	return &ProjectHandler{
		store:       s,
		configStore: cfgStore,
		worktreeMgr: git.NewWorktreeManager(s, cfgStore),
		hostRoot:    fsutil.NewHostRoot(true, os.TempDir()),
	}
}

// recordSubmodule makes a checkout at project that records upstream at subPath and never fetched it: the
// `.gitmodules` section is named something other than the path, which is the case a lookup derived from the
// path loses, and the gitlink is written by `update-index` because `git add -A` prunes a gitlink whose
// directory is not on disk.
func recordSubmodule(t *testing.T, project, upstream, subPath string) {
	t.Helper()
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.RepoAt(t, project)
	testgit.Git(t, project, "config", "-f", ".gitmodules", "submodule.mods.path", subPath)
	testgit.Git(t, project, "config", "-f", ".gitmodules", "submodule.mods.url", upstream)
	testgit.Git(t, project, "add", ".gitmodules")
	testgit.Git(t, project, "commit", "-m", "gitmodules")
	testgit.Git(t, project, "update-index", "--add", "--cacheinfo",
		"160000,"+testgit.GitOutput(t, upstream, "rev-parse", "HEAD")+","+subPath)
	testgit.Git(t, project, "commit", "-m", "record the submodule")
}

// seedSubmoduleProject creates the project row and one task for it.
func seedSubmoduleProject(t *testing.T, h *ProjectHandler, projectPath string) {
	t.Helper()
	if err := h.store.CreateProject(&types.Project{ID: submoduleProjectID, Name: "sub", Path: projectPath}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.CreateTask(&types.Task{ID: submoduleTaskID, ProjectID: submoduleProjectID, Title: "t", Status: types.StatusBacklog}); err != nil {
		t.Fatal(err)
	}
}

func doGetSubmodules(t *testing.T, h *ProjectHandler) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+submoduleProjectID+"/tasks/"+submoduleTaskID+"/submodules", nil)
	req.SetPathValue("projectID", submoduleProjectID)
	req.SetPathValue("taskID", submoduleTaskID)
	rr := httptest.NewRecorder()
	h.HandleTaskSubmodules(rr, req)
	return rr.Code, rr.Body.String()
}

func doInitSubmodules(t *testing.T, h *ProjectHandler) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+submoduleProjectID+"/submodules/init", nil)
	req.SetPathValue("projectID", submoduleProjectID)
	rr := httptest.NewRecorder()
	h.HandleInitSubmodules(rr, req)
	return rr.Code, rr.Body.String()
}

// TestHandleTaskSubmodulesNamesTheMissingCopyAndItsURL: the card's notice is built from this listing, so the
// path, the address and the reason all have to be in it.
func TestHandleTaskSubmodulesNamesTheMissingCopyAndItsURL(t *testing.T) {
	tmp := t.TempDir()
	upstream := filepath.Join(tmp, "up")
	makeCommittedRepo(t, upstream, 1)
	project := filepath.Join(tmp, "super")
	recordSubmodule(t, project, upstream, "vendor/lib")
	h := newSubmoduleHandler(t, project)
	seedSubmoduleProject(t, h, project)

	code, body := doGetSubmodules(t, h)
	if code != http.StatusOK {
		t.Fatalf("listing the submodules of a task: %d %s", code, body)
	}
	var out struct {
		Submodules []git.SubmoduleState `json:"submodules"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Submodules) != 1 {
		t.Fatalf("submodules = %v, want the one the commit records", out.Submodules)
	}
	row := out.Submodules[0]
	if row.Path != "vendor/lib" || row.Populated {
		t.Errorf("row = %+v, want an unpopulated vendor/lib", row)
	}
	if row.URL != upstream {
		t.Errorf("URL = %q, want the address git records for that path (%s)", row.URL, upstream)
	}
	if row.Note == "" {
		t.Errorf("Note is empty, want the reason a fetch would not bring it down either")
	}
}

// TestHandleTaskSubmodulesReadsTheTasksOwnCommit: a task waiting on a gitlink only its own commit names must
// say so, rather than report the checkout as clean.
func TestHandleTaskSubmodulesReadsTheTasksOwnCommit(t *testing.T) {
	tmp := t.TempDir()
	upstream := filepath.Join(tmp, "up")
	makeCommittedRepo(t, upstream, 1)
	project := filepath.Join(tmp, "super")
	recordSubmodule(t, project, upstream, "vendor/lib")
	h := newSubmoduleHandler(t, project)
	seedSubmoduleProject(t, h, project)

	// The task's worktree, at the path its worktree manager would use, with a submodule of its own added.
	wt, _, err := h.worktreeMgr.CreateWorktree(submoduleProjectID, submoduleTaskID, "task/"+submoduleTaskID, "")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	other := filepath.Join(tmp, "other")
	makeCommittedRepo(t, other, 1)
	testgit.Git(t, wt, "config", "-f", ".gitmodules", "submodule.extra.path", "vendor/extra")
	testgit.Git(t, wt, "config", "-f", ".gitmodules", "submodule.extra.url", other)
	testgit.Git(t, wt, "add", ".gitmodules")
	testgit.Git(t, wt, "commit", "-m", "model added a submodule")
	testgit.Git(t, wt, "update-index", "--add", "--cacheinfo", "160000,"+testgit.GitOutput(t, other, "rev-parse", "HEAD")+",vendor/extra")
	testgit.Git(t, wt, "commit", "-m", "record it")

	code, body := doGetSubmodules(t, h)
	if code != http.StatusOK {
		t.Fatalf("listing the submodules of a task: %d %s", code, body)
	}
	var out struct {
		Submodules []git.SubmoduleState `json:"submodules"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, s := range out.Submodules {
		paths = append(paths, s.Path)
	}
	if !strings.Contains(strings.Join(paths, ","), "vendor/extra") {
		t.Errorf("the listing reads the checkout, not the task: %v", paths)
	}
}

// TestHandleInitSubmodulesReportsWhatStayedMissing: the click cannot be answered with a 500 when an address
// was refused or an upstream is gone — the person has to be told which, and stay able to act on it.
func TestHandleInitSubmodulesReportsWhatStayedMissing(t *testing.T) {
	tmp := t.TempDir()
	upstream := filepath.Join(tmp, "up")
	makeCommittedRepo(t, upstream, 1)
	project := filepath.Join(tmp, "super")
	recordSubmodule(t, project, upstream, "vendor/lib")
	h := newSubmoduleHandler(t, project)
	seedSubmoduleProject(t, h, project)

	code, body := doInitSubmodules(t, h)
	if code != http.StatusOK {
		t.Fatalf("initializing submodules: %d %s", code, body)
	}
	var out struct {
		Populated []string             `json:"populated"`
		Reported  []string             `json:"reported"`
		Missing   []git.SubmoduleState `json:"missing"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Populated) != 0 {
		t.Errorf("populated = %v, want nothing: the address is one MIKE will not dial", out.Populated)
	}
	if len(out.Missing) != 1 || out.Missing[0].Path != "vendor/lib" {
		t.Fatalf("missing = %v, want the submodule that stayed out", out.Missing)
	}
	if len(out.Reported) == 0 || !strings.Contains(out.Reported[0], "vendor/lib") {
		t.Errorf("reported = %v, want git's reason for the one that stayed out", out.Reported)
	}
	if _, err := os.Stat(filepath.Join(project, "vendor", "lib", "data.txt")); err == nil {
		t.Error("the refused submodule's files were fetched anyway")
	}
}

// TestHandleSubmodulesAnswerEmptyForAProjectWithNoSubmodules: an empty listing is an empty array, so the card
// can tell "this project has no submodules" from a request that said nothing.
func TestHandleSubmodulesAnswerEmptyForAProjectWithNoSubmodules(t *testing.T) {
	project := filepath.Join(t.TempDir(), "plain")
	makeCommittedRepo(t, project, 1)
	h := newSubmoduleHandler(t, project)
	seedSubmoduleProject(t, h, project)

	code, body := doGetSubmodules(t, h)
	if code != http.StatusOK {
		t.Fatalf("listing the submodules of a task: %d %s", code, body)
	}
	if !strings.Contains(body, `"submodules":[]`) {
		t.Errorf("body = %s, want an empty array", body)
	}

	code, body = doInitSubmodules(t, h)
	if code != http.StatusOK {
		t.Fatalf("initializing the submodules of a project with none: %d %s", code, body)
	}
}

// TestHandleTaskSubmodulesRefusesAnotherProjectsTask: the task in the path is read through the project in the
// path, so another project's task is not a way to read a checkout nobody pointed at.
func TestHandleTaskSubmodulesRefusesAnotherProjectsTask(t *testing.T) {
	project := filepath.Join(t.TempDir(), "plain")
	makeCommittedRepo(t, project, 1)
	h := newSubmoduleHandler(t, project)
	seedSubmoduleProject(t, h, project)
	if err := h.store.CreateProject(&types.Project{ID: "other-proj", Name: "other", Path: filepath.Join(t.TempDir(), "elsewhere")}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/other-proj/tasks/"+submoduleTaskID+"/submodules", nil)
	req.SetPathValue("projectID", "other-proj")
	req.SetPathValue("taskID", submoduleTaskID)
	rr := httptest.NewRecorder()
	h.HandleTaskSubmodules(rr, req)
	if rr.Code == http.StatusOK {
		t.Errorf("another project's task was listed: %s", rr.Body.String())
	}
}
