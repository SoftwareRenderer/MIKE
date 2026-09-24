package api

// GET /tasks/{id}/diff/file names a path and no refs, so every request for a task must answer for the one
// comparison its Changes panel is showing.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/types"
)

type taskDiffFixture struct {
	h        *GitHandler
	mux      *http.ServeMux
	store    *store.Store
	task     *types.Task
	projID   string
	taskID   string
	projPath string
	wtPath   string
	// base is the project commit every task in these tests started from.
	base string
	// targetBranch is the branch the task records as its base; other work moves it forward.
	targetBranch string
}

// newTaskDiffFixture builds a project, a task on its own branch, and a real worktree for it.
func newTaskDiffFixture(t *testing.T, suffix, taskBranch string) *taskDiffFixture {
	t.Helper()
	h, s, _, worktreesDir := newTestGitHandler(t)

	projID, taskID := "diffident-proj-"+suffix, "diffident-task-"+suffix
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	if err := s.CreateProject(&types.Project{ID: projID, Name: projID, Path: projPath}); err != nil {
		t.Fatal(err)
	}
	targetBranch := strings.TrimSpace(mustOut(t, projPath, "git", "rev-parse", "--abbrev-ref", "HEAD"))
	task := &types.Task{ID: taskID, ProjectID: projID, Title: "identity", Status: types.StatusInProgress,
		Branch: taskBranch, BaseRef: targetBranch}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	wtPath := filepath.Join(worktreesDir, projID, taskID)
	createWorktreeAt(t, projPath, wtPath, taskBranch)

	return &taskDiffFixture{
		h: h, mux: taskDiffMux(h), store: s, task: task, projID: projID, taskID: taskID,
		projPath: projPath, wtPath: wtPath, targetBranch: targetBranch,
		base: strings.TrimSpace(mustOut(t, projPath, "git", "rev-parse", "HEAD")),
	}
}

// movedBranchFixture is a task whose file was committed on its own branch, changed by someone else on the
// target branch, then rewritten past the truncation limit. Its recorded BaseRef names a branch that has moved.
func movedBranchFixture(t *testing.T, suffix string) *taskDiffFixture {
	t.Helper()
	f := newTaskDiffFixture(t, suffix, "task-x")

	var taskLines []string
	for i := 1; i <= 10; i++ {
		taskLines = append(taskLines, fmt.Sprintf("TASK-LINE-%d", i))
	}
	f.commitInWorktree(t, "big.txt", strings.Join(taskLines, "\n")+"\n", "task wrote big.txt")

	f.write(t, f.projPath, "big.txt", strings.Join(taskLines, "\n")+"\nFOREIGN-BYTES\n")
	mustRun(t, f.projPath, "git", "add", "big.txt")
	mustRun(t, f.projPath, "git", "commit", "-m", "someone else moved the target branch")

	var work []string
	for i := 1; i <= 80; i++ {
		work = append(work, fmt.Sprintf("WORK-%d", i))
	}
	f.write(t, f.wtPath, "big.txt", strings.Join(work, "\n")+"\n")

	f.task.BaseCommit = f.base
	f.saveTask(t)

	entry, ok := f.manifest(t).Files["big.txt"]
	if !ok {
		t.Fatal("the manifest does not list big.txt")
	}
	if !entry.Truncated {
		t.Fatalf("big.txt is not truncated, so nothing here exercises the expand path: %+v", entry)
	}
	return f
}

func (f *taskDiffFixture) commitInWorktree(t *testing.T, name, content, message string) string {
	t.Helper()
	f.write(t, f.wtPath, name, content)
	mustRun(t, f.wtPath, "git", "add", name)
	mustRun(t, f.wtPath, "git", "commit", "-m", message)
	return strings.TrimSpace(mustOut(t, f.wtPath, "git", "rev-parse", "HEAD"))
}

func (f *taskDiffFixture) write(t *testing.T, dir, name, content string) {
	t.Helper()
	createFile(t, dir, name, content)
}

func (f *taskDiffFixture) saveTask(t *testing.T) {
	t.Helper()
	if err := f.store.UpdateTask(f.task); err != nil {
		t.Fatal(err)
	}
}

func taskDiffMux(h *GitHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff", h.HandleTaskDiff)
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff/file", h.HandleTaskDiffFile)
	return mux
}

type taskDiffManifestBody struct {
	Files map[string]struct {
		Hash      string `json:"hash"`
		Type      string `json:"type"`
		Added     int    `json:"added"`
		Removed   int    `json:"removed"`
		Truncated bool   `json:"truncated"`
	} `json:"files"`
	BaseRef string `json:"base_ref"`
}

type taskDiffFileBody struct {
	Rows          []git.DiffRow `json:"rows"`
	CommittedRows []git.DiffRow `json:"committedRows"`
	Truncated     bool          `json:"truncated"`
}

func (b taskDiffFileBody) String() string {
	var sb strings.Builder
	for _, r := range b.Rows {
		fmt.Fprintf(&sb, " %s:%s\n", r.Variant, r.Text)
	}
	for _, r := range b.CommittedRows {
		fmt.Fprintf(&sb, "c%s:%s\n", r.Variant, r.Text)
	}
	return sb.String()
}

func (f *taskDiffFixture) file(t *testing.T, path, query string) taskDiffFileBody {
	t.Helper()
	url := fmt.Sprintf("/api/projects/%s/tasks/%s/diff/file?path=%s%s", f.projID, f.taskID, path, query)
	rr := doRequest(t, f.mux, http.MethodGet, url)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", url, rr.Code, rr.Body.String())
	}
	var body taskDiffFileBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, rr.Body.String())
	}
	return body
}

func (f *taskDiffFixture) manifest(t *testing.T) taskDiffManifestBody {
	t.Helper()
	rr := doRequest(t, f.mux, http.MethodGet, "/api/projects/"+f.projID+"/tasks/"+f.taskID+"/diff")
	if rr.Code != http.StatusOK {
		t.Fatalf("manifest: %d %s", rr.Code, rr.Body.String())
	}
	var m taskDiffManifestBody
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal manifest: %v\n%s", err, rr.Body.String())
	}
	return m
}

func (f *taskDiffFixture) compare(t *testing.T, base, head string) string {
	t.Helper()
	url := fmt.Sprintf("/api/projects/%s/tasks/%s/diff?base=%s&head=%s&parsed=1", f.projID, f.taskID, base, head)
	rr := doRequest(t, f.mux, http.MethodGet, url)
	if rr.Code != http.StatusOK {
		t.Fatalf("comparison: %d %s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

// The panel lists files from the working-tree manifest, then fetches rows by path; a comparison opened in
// between must not change what that fetch answers.
func TestTaskDiffFileRowsSurviveACommitComparison(t *testing.T) {
	f := newTaskDiffFixture(t, "rows", "main")
	tip := f.commitInWorktree(t, "a.txt", "COMMITTED-BY-TASK\n", "task commit")
	f.write(t, f.wtPath, "a.txt", "COMMITTED-BY-TASK\nWORKTREE-ONLY\n")
	f.task.BaseCommit = f.base
	f.saveTask(t)

	if _, ok := f.manifest(t).Files["a.txt"]; !ok {
		t.Fatal("the manifest does not list a.txt")
	}
	if body := f.compare(t, f.base, tip); !strings.Contains(body, "COMMITTED-BY-TASK") {
		t.Fatalf("the comparison lost its own content: %s", body)
	}

	got := f.file(t, "a.txt", "")
	if !strings.Contains(got.String(), "WORKTREE-ONLY") {
		t.Errorf("the panel's file fetch answered with the commit comparison, so the uncommitted change is "+
			"gone from a file the panel still lists:\n%s", got)
	}
}

// A different baseline here would show commits the task did not make, since its recorded BaseRef has moved.
func TestTaskDiffExpandKeepsTheTaskBaseline(t *testing.T) {
	f := movedBranchFixture(t, "expand")

	before := f.file(t, "big.txt", "")
	after := f.file(t, "big.txt", "&full=1")

	if !strings.Contains(before.String(), "TASK-LINE-1") {
		t.Errorf("the committed half does not carry the task's own commit:\n%s", before)
	}
	if strings.Contains(after.String(), "FOREIGN-BYTES") {
		t.Errorf("expanding re-diffed against the moved branch, so another task's commit appears in this "+
			"file's rows:\n%s", after)
	}
	if !strings.Contains(after.String(), "TASK-LINE-1") {
		t.Errorf("expanding lost the task's own committed change:\n%s", after)
	}
}

// A client that learned its file list from the broadcast manifest must be able to fetch rows from the REST one.
func TestTaskDiffBroadcastManifestMatchesThePanelManifest(t *testing.T) {
	f := movedBranchFixture(t, "broadcast")

	rest := f.manifest(t)
	task, err := f.store.GetTaskByID(f.taskID)
	if err != nil {
		t.Fatal(err)
	}
	broadcast, err := f.h.GetDiffManifest(task)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(broadcast)
	if err != nil {
		t.Fatal(err)
	}
	var got taskDiffManifestBody
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rest, got) {
		t.Errorf("the broadcast manifest is a different comparison than the panel's\nrest:      %+v\nbroadcast: %+v", rest, got)
	}
	for path := range rest.Files {
		body := f.file(t, path, "")
		if len(body.Rows) == 0 && len(body.CommittedRows) == 0 {
			t.Errorf("%s is listed in the manifest but answers with no rows", path)
		}
	}
}
