package managers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/testutil/testgit"
	"kanban/backend/types"
)

// --- Test helpers (shared) ---

// mustRun executes a command in dir, failing the test on error.
func mustRun(t *testing.T, dir, cmd string, args ...string) {
	t.Helper()
	c := exec.Command(cmd, args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", cmd, args, err, out)
	}
}

// initGitRepo creates a git repo at path with one committed file.
// Used by tests that need git history (log, diff, etc.).
func initGitRepo(t *testing.T, path string) {
	t.Helper()
	testgit.RepoAt(t, path)
}

// initGitRepoTemp creates a temp directory, initializes a bare git repo there
// (no commits, no user config), and returns the repo path. Used by tests that
// only need a git working tree for staging.
func initGitRepoTemp(t *testing.T) string {
	t.Helper()
	return testgit.EmptyRepo(t)
}

// createFile creates a file with the given name and content inside repoPath.
func createFile(t *testing.T, repoPath, filename, content string) {
	t.Helper()
	fullPath := filepath.Join(repoPath, filename)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", filename, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", filename, err)
	}
}

// stagedFiles returns the names of files staged in the index.
func stagedFiles(t *testing.T, repoPath string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "diff", "--cached", "--name-only").Output()
	if err != nil {
		t.Fatalf("git diff --cached: %v: %s", err, out)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

// createWorktreeAt creates a git worktree at worktreePath with the given branch.
func createWorktreeAt(t *testing.T, projectPath, worktreePath, branch string) {
	t.Helper()
	mustRun(t, projectPath, "git", "worktree", "add", "-B", branch, worktreePath)
}

// gitTestStore creates a temp store and registers cleanup.
func gitTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newTestGitHandler creates a GitHandler backed by a temporary store, config, and
// worktree manager with config values set so ConfigStore.GetConfig() succeeds.
func newTestGitHandler(t *testing.T) (*GitHandler, *store.Store, *git.WorktreeManager, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	worktreesDir := filepath.Join(dir, "worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	s.SetConfigValue("providers", "[]")
	s.SetConfigValue("worker_provider", "")
	s.SetConfigValue("worker_model", "")
	s.SetConfigValue("utility_provider", "")
	s.SetConfigValue("utility_model", "")

	cfgStore := config.NewConfigStore(s, dbPath, "test")
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	wm := git.NewWorktreeManager(s, cfgStore)
	h := NewGitHandler(s, wm)

	return h, s, wm, worktreesDir
}

// newTestGitMux creates a ServeMux wired with the four git handler routes so
// that {projectID} and {taskID} path values are populated automatically.
func newTestGitMux(h *GitHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{projectID}/git/log", h.HandleLog)
	mux.HandleFunc("GET /api/projects/{projectID}/git/diff", h.HandleDiff)
	mux.HandleFunc("GET /api/projects/{projectID}/git/diff/file", h.HandleProjectDiffFile)
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/git/log", h.HandleTaskLog)
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff", h.HandleTaskDiff)
	return mux
}

// doRequest sends a request through the mux and returns the recorded response.
func doRequest(t *testing.T, mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// newStageRequest builds an HTTP POST request with a JSON body for staging.
func newStageRequest(t *testing.T, target, path string, stage bool) *http.Request {
	t.Helper()
	stageStr := "true"
	if !stage {
		stageStr = "false"
	}
	body := strings.NewReader(`{"path": "` + path + `", "stage": ` + stageStr + `}`)
	req := httptest.NewRequest("POST", target, body)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// setupHandleLogProject creates a project with a git repo and returns (handler, mux).
func setupHandleLogProject(t *testing.T) (*GitHandler, *http.ServeMux) {
	t.Helper()
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "log-char-proj", Name: "log-char-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	return h, newTestGitMux(h)
}

// TestGitRefValidation is the ref-injection matrix for every git route that takes a ref:
// project log/diff and task log/diff. A ref lands on a git command line, so two families
// of value must be rejected before that happens — option-looking refs ("-foo",
// "--format=json"), which git would parse as flags, and the rev-syntax characters git
// itself would reinterpret ("~", "^", ":", ".."). Each target carries its own fixture
// because the task routes resolve the ref inside a worktree that has to exist.
//
// The accepted side is covered separately (TestHandleLog_ValidBranch,
// TestHandleDiff_ValidRefs, TestHandleTaskLog_ValidBranch, TestHandleTaskDiff_ValidRefs),
// which also proves the 400s above come from validation and not from a broken route.
func TestGitRefValidation(t *testing.T) {
	targets := []struct {
		name string
		// setup builds the fixture and returns the mux plus a URL builder for one ref
		// position (each route exposes its refs under different parameter names).
		setup  func(t *testing.T) (mux *http.ServeMux, url func(param, value string) string)
		params []string
		// ref builds the value under test from a bare invalid character. The diff routes
		// take full revs, so the character is embedded in a rev-looking string there.
		ref func(ch string) string
	}{
		{
			name: "project log",
			setup: func(t *testing.T) (*http.ServeMux, func(string, string) string) {
				_, mux := setupHandleLogProject(t)
				return mux, func(_, value string) string {
					return "/api/projects/log-char-proj/git/log?branch=" + value
				}
			},
			params: []string{"branch"},
			ref:    func(ch string) string { return ch },
		},
		{
			name: "project diff",
			setup: func(t *testing.T) (*http.ServeMux, func(string, string) string) {
				h, s, _, _ := newTestGitHandler(t)
				projPath := t.TempDir()
				initGitRepo(t, projPath)
				proj := &types.Project{ID: "refval-diff-proj", Name: "refval-diff-proj", Path: projPath}
				if err := s.CreateProject(proj); err != nil {
					t.Fatal(err)
				}
				return newTestGitMux(h), func(param, value string) string {
					if param == "base" {
						return "/api/projects/refval-diff-proj/git/diff?base=" + value + "&head=HEAD"
					}
					return "/api/projects/refval-diff-proj/git/diff?base=HEAD&head=" + value
				}
			},
			params: []string{"base", "head"},
			ref:    func(ch string) string { return "feat" + ch + "branch" },
		},
		{
			name: "task log",
			setup: func(t *testing.T) (*http.ServeMux, func(string, string) string) {
				h, s, _, worktreesDir := newTestGitHandler(t)
				projPath := t.TempDir()
				initGitRepo(t, projPath)
				proj := &types.Project{ID: "refval-tasklog-proj", Name: "refval-tasklog-proj", Path: projPath}
				if err := s.CreateProject(proj); err != nil {
					t.Fatal(err)
				}
				task := &types.Task{ID: "refval-tasklog-task", ProjectID: proj.ID,
					Title: "log task", Status: "in_progress", Branch: "main"}
				if err := s.CreateTask(task); err != nil {
					t.Fatal(err)
				}
				createWorktreeAt(t, projPath, filepath.Join(worktreesDir, proj.ID, task.ID), "main")
				return newTestGitMux(h), func(_, value string) string {
					return "/api/projects/refval-tasklog-proj/tasks/refval-tasklog-task/git/log?branch=" + value
				}
			},
			params: []string{"branch"},
			ref:    func(ch string) string { return ch },
		},
		{
			name: "task diff",
			setup: func(t *testing.T) (*http.ServeMux, func(string, string) string) {
				h, s, _, _ := newTestGitHandler(t)
				projPath := t.TempDir()
				initGitRepo(t, projPath)
				proj := &types.Project{ID: "refval-taskdiff-proj", Name: "refval-taskdiff-proj", Path: projPath}
				if err := s.CreateProject(proj); err != nil {
					t.Fatal(err)
				}
				task := &types.Task{ID: "refval-taskdiff-task", ProjectID: proj.ID,
					Title: "diff task", Status: "in_progress", Branch: "main"}
				if err := s.CreateTask(task); err != nil {
					t.Fatal(err)
				}
				return newTestGitMux(h), func(param, value string) string {
					return "/api/projects/refval-taskdiff-proj/tasks/refval-taskdiff-task/diff?" + param + "=" + value
				}
			},
			params: []string{"base", "head"},
			ref:    func(ch string) string { return "feat" + ch + "branch" },
		},
	}

	// The space is URL-encoded because that is how it arrives from a browser.
	encoded := map[string]string{" ": "feat%20branch"}

	for _, tgt := range targets {
		t.Run(tgt.name, func(t *testing.T) {
			mux, url := tgt.setup(t)

			for _, param := range tgt.params {
				// Dash-prefixed refs are sent verbatim — that is exactly the shape git would
				// misread as an option.
				for _, option := range []string{"-foo", "--format=json"} {
					t.Run(param+"/"+option, func(t *testing.T) {
						rr := doRequest(t, mux, "GET", url(param, option))
						if rr.Code != http.StatusBadRequest {
							t.Fatalf("%s=%q: status = %d, want 400; body: %s",
								param, option, rr.Code, rr.Body.String())
						}
					})
				}

				for _, ch := range invalidBranchChars {
					value, ok := encoded[ch]
					if !ok {
						value = tgt.ref(ch)
					}
					t.Run(param+"/"+strings.TrimSpace(ch), func(t *testing.T) {
						rr := doRequest(t, mux, "GET", url(param, value))
						if rr.Code != http.StatusBadRequest {
							t.Fatalf("%s=%q: status = %d, want 400; body: %s",
								param, value, rr.Code, rr.Body.String())
						}
					})
				}
			}
		})
	}
}

func TestHandleLog_ValidBranch(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "log-proj-valid", Name: "log-proj-valid", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)

	// Valid branch (HEAD) → 200
	rr := doRequest(t, mux, "GET", "/api/projects/log-proj-valid/git/log?branch=HEAD")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid branch HEAD, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleLog_NoBranchDefaultsToHEAD(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "log-proj-default", Name: "log-proj-default", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)

	// No branch param → defaults to HEAD → 200
	rr := doRequest(t, mux, "GET", "/api/projects/log-proj-default/git/log")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 when branch param is absent (defaults to HEAD), got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleDiff_ValidRefs(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "diff-proj-valid", Name: "diff-proj-valid", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)

	// Valid refs (HEAD..HEAD → empty diff) → 200
	rr := doRequest(t, mux, "GET", "/api/projects/diff-proj-valid/git/diff?base=HEAD&head=HEAD")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid refs, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- HandleProjectDiffFile tests ---

func TestHandleProjectDiffFile_Meta_Untracked(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	// Untracked file (created but not added/committed)
	createFile(t, projPath, "untracked.txt", "line1\nline2\nline3\n")
	proj := &types.Project{ID: "diff-file-meta-proj", Name: "diff-file-meta-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)

	// meta mode returns lightweight metadata without diff/lines
	rr := doRequest(t, mux, "GET", "/api/projects/diff-file-meta-proj/git/diff/file?path=untracked.txt&meta=1")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		LineCount int   `json:"line_count"`
		FileSize  int64 `json:"file_size"`
		IsBinary  bool  `json:"isBinary"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.LineCount != 3 {
		t.Fatalf("expected line_count 3, got %d", resp.LineCount)
	}
	// Small untracked files must NOT report file_size — a present file_size means
	// "large/binary file" to the frontend (would render a "Large file" notice).
	if resp.FileSize != 0 {
		t.Fatalf("expected no file_size for small file, got %d", resp.FileSize)
	}
	if resp.IsBinary {
		t.Fatal("expected not binary")
	}
}

func TestHandleProjectDiffFile_Meta_LargeUntracked(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	// Untracked file larger than the 1MB diff threshold
	largeContent := strings.Repeat("x", 1<<20+1) + "\n"
	createFile(t, projPath, "large.txt", largeContent)
	proj := &types.Project{ID: "diff-file-meta-large-proj", Name: "diff-file-meta-large-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)

	rr := doRequest(t, mux, "GET", "/api/projects/diff-file-meta-large-proj/git/diff/file?path=large.txt&meta=1")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		LineCount int   `json:"line_count"`
		FileSize  int64 `json:"file_size"`
		IsBinary  bool  `json:"isBinary"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Large untracked files DO report file_size so the UI shows a "Large file" notice.
	if want := int64(len(largeContent)); resp.FileSize != want {
		t.Fatalf("expected file_size %d, got %d", want, resp.FileSize)
	}
	if resp.IsBinary {
		t.Fatal("expected not binary")
	}
}

func TestHandleProjectDiffFile_Content_Untracked(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	createFile(t, projPath, "untracked.txt", "hello\nworld\n")
	proj := &types.Project{ID: "diff-file-content-proj", Name: "diff-file-content-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)

	rr := doRequest(t, mux, "GET", "/api/projects/diff-file-content-proj/git/diff/file?path=untracked.txt")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Lines     []string `json:"lines"`
		LineCount int      `json:"line_count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.LineCount != 2 {
		t.Fatalf("expected line_count 2, got %d", resp.LineCount)
	}
	if len(resp.Lines) != 2 || resp.Lines[0] != "hello" || resp.Lines[1] != "world" {
		t.Fatalf("unexpected lines: %v", resp.Lines)
	}
}

func TestHandleTaskLog_ValidBranch(t *testing.T) {
	h, s, wm, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "tasklog-proj-v", Name: "tasklog-proj-v", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	task := &types.Task{
		ID:        "tasklog-task-v",
		ProjectID: proj.ID,
		Title:     "log task valid",
		Status:    "in_progress",
		Branch:    "main",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(worktreesDir, proj.ID, task.ID)
	createWorktreeAt(t, projPath, wtPath, "main")

	mux := newTestGitMux(h)
	_ = wm

	// Valid branch (HEAD) → 200
	rr := doRequest(t, mux, "GET", "/api/projects/tasklog-proj-v/tasks/tasklog-task-v/git/log?branch=HEAD")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid branch HEAD, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleTaskLog_FullSharesProjectResponse locks the ?full=1 shape that
// HandleLog and HandleTaskLog now produce from one code path
// (writeFullGitLog): every commit field, the body between the BODY markers,
// hasMore paging and the current branch. The task endpoint had no full=1
// coverage before, so this is what proves the shared implementation still
// serves a worktree exactly like a project checkout.
func TestHandleTaskLog_FullSharesProjectResponse(t *testing.T) {
	h, s, _, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	// A second commit with a multi-line body exercises the %b extraction.
	createFile(t, projPath, "second.go", "package main\n")
	mustRun(t, projPath, "git", "add", "second.go")
	mustRun(t, projPath, "git", "commit", "-m", "Add second file", "-m", "Body line one\nBody line two")

	proj := &types.Project{ID: "tasklogfull-proj", Name: "tasklogfull-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{
		ID:        "tasklogfull-task",
		ProjectID: proj.ID,
		Title:     "log full",
		Status:    "in_progress",
		Branch:    "main",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	createWorktreeAt(t, projPath, filepath.Join(worktreesDir, proj.ID, task.ID), "main")

	mux := newTestGitMux(h)

	rr := doRequest(t, mux, "GET", "/api/projects/tasklogfull-proj/tasks/tasklogfull-task/git/log?full=1&limit=1")
	if rr.Code != http.StatusOK {
		t.Fatalf("task full log: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var taskLog types.GitLogResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &taskLog); err != nil {
		t.Fatalf("decode task full log: %v (%s)", err, rr.Body.String())
	}
	if len(taskLog.Commits) != 1 {
		t.Fatalf("expected 1 commit with limit=1, got %d", len(taskLog.Commits))
	}
	if !taskLog.HasMore {
		t.Error("expected has_more=true when more commits exist than the limit")
	}
	top := taskLog.Commits[0]
	if top.Hash == "" || top.Short == "" || top.Author == "" || top.Date == "" {
		t.Errorf("expected hash/short/author/date populated, got %+v", top)
	}
	if top.Subject != "Add second file" {
		t.Errorf("subject = %q, want %q", top.Subject, "Add second file")
	}
	if !strings.Contains(top.Body, "Body line one") || !strings.Contains(top.Body, "Body line two") {
		t.Errorf("body = %q, want both body lines", top.Body)
	}
	if len(top.Parents) != 1 {
		t.Errorf("expected 1 parent on the second commit, got %v", top.Parents)
	}
	if taskLog.CurrentBranch == "" {
		t.Error("expected current_branch to be resolved in the worktree")
	}

	// The project endpoint reads the same repo and must agree exactly.
	rr2 := doRequest(t, mux, "GET", "/api/projects/tasklogfull-proj/git/log?full=1&limit=1")
	if rr2.Code != http.StatusOK {
		t.Fatalf("project full log: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var projLog types.GitLogResponse
	if err := json.Unmarshal(rr2.Body.Bytes(), &projLog); err != nil {
		t.Fatalf("decode project full log: %v (%s)", err, rr2.Body.String())
	}
	if len(projLog.Commits) != 1 || projLog.Commits[0].Hash != top.Hash {
		t.Errorf("project log top commit = %+v, want same hash as task log (%s)", projLog.Commits, top.Hash)
	}
	if projLog.HasMore != taskLog.HasMore {
		t.Errorf("project has_more=%v, task has_more=%v — must agree", projLog.HasMore, taskLog.HasMore)
	}
}

func TestHandleTaskDiff_ValidRefs(t *testing.T) {
	h, s, wm, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "taskdiff-proj-v", Name: "taskdiff-proj-v", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	task := &types.Task{
		ID:        "taskdiff-task-v",
		ProjectID: proj.ID,
		Title:     "diff task valid",
		Status:    "in_progress",
		Branch:    "main",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(worktreesDir, proj.ID, task.ID)
	createWorktreeAt(t, projPath, wtPath, "main")

	mux := newTestGitMux(h)
	_ = wm

	// Valid refs (HEAD..HEAD → empty diff) → 200
	rr := doRequest(t, mux, "GET", "/api/projects/taskdiff-proj-v/tasks/taskdiff-task-v/diff?base=HEAD&head=HEAD")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid refs, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- Character validation tests ---
// These tests verify that characters not in the allowed set (letters, digits,
// -, _, ., /, +) are rejected with 400 Bad Request.

var invalidBranchChars = []string{"~", "^", ":", "?", "*", "[", "\\", " ", "feat..branch"}

// --- Dash-prefixed path staging tests ---
// These tests verify that file paths starting with '-' are safely staged
// (treated as filenames, not git flags) thanks to the '--' separator
// before the path argument in `git add`.

// TestHandleProjectStageFile_PathStartingWithDash verifies that a path
// starting with '-' is safely staged (treated as a filename, not a git flag)
// thanks to the '--' separator before the path argument in `git add`.
//
// Without the '--', `git add -dash-file.txt` would be interpreted as
// `git add -d -a -s -h ...` (short options) and fail with an "unknown switch"
// error, leaving the file unstaged.
func TestHandleProjectStageFile_PathStartingWithDash(t *testing.T) {
	repoPath := initGitRepoTemp(t)
	s := gitTestStore(t)

	proj := &types.Project{ID: "test-proj", Name: "test", Path: repoPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	h := &GitHandler{store: s}

	filename := "-dash-file.txt"
	createFile(t, repoPath, filename, "content\n")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/git/stage", h.HandleProjectStageFile)

	req := newStageRequest(t, "/api/projects/test-proj/git/stage", filename, true)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	// The file must actually be in the staged index.
	files := stagedFiles(t, repoPath)
	for _, f := range files {
		if f == filename {
			return
		}
	}
	t.Fatalf("expected %q to be staged, got: %v", filename, files)
}

// TestHandleStageFile_PathStartingWithDash verifies the same '--' separator
// protection for the task-level staging handler, which operates against a
// git worktree rather than the project repo.
func TestHandleStageFile_PathStartingWithDash(t *testing.T) {
	worktreesDir := t.TempDir()

	repoPath := initGitRepoTemp(t)
	s := gitTestStore(t)

	projID := "test-proj"
	taskID := "test-task"
	proj := &types.Project{ID: projID, Name: "test", Path: repoPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create the task. Status must not be "backlog" or "done".
	task := &types.Task{ID: taskID, ProjectID: projID, Title: "test", Status: "in_progress"}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	cfgStore := config.NewConfigStore(s, filepath.Join(t.TempDir(), "test.db"), "test")
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	wm := git.NewWorktreeManager(s, cfgStore)
	h := &GitHandler{store: s, worktreeMgr: wm}

	// Create the worktree directory (mirrors what WorktreePath computes).
	worktreePath := filepath.Join(worktreesDir, projID, taskID)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if out, err := exec.Command("git", "init", worktreePath).CombinedOutput(); err != nil {
		t.Fatalf("git init worktree: %v: %s", err, out)
	}

	filename := "-dash-file.txt"
	createFile(t, worktreePath, filename, "content\n")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/diff/stage", h.HandleStageFile)

	req := newStageRequest(t, "/api/projects/test-proj/tasks/test-task/diff/stage", filename, true)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	files := stagedFiles(t, worktreePath)
	for _, f := range files {
		if f == filename {
			return
		}
	}
	t.Fatalf("expected %q to be staged, got: %v", filename, files)
}

func TestHandleTaskLog_BranchDefaultsToTaskBranch(t *testing.T) {
	h, s, wm, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "tasklog-default-proj", Name: "tasklog-default-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	task := &types.Task{
		ID:        "tasklog-default-task",
		ProjectID: proj.ID,
		Title:     "default log task",
		Status:    "in_progress",
		Branch:    "main",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(worktreesDir, proj.ID, task.ID)
	createWorktreeAt(t, projPath, wtPath, "main")

	mux := newTestGitMux(h)
	_ = wm

	// No branch param → defaults to task.Branch ("main") → 200
	rr := doRequest(t, mux, "GET", "/api/projects/tasklog-default-proj/tasks/tasklog-default-task/git/log")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 when branch param is absent (defaults to task branch), got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- HandleCheckout tests ---

func TestHandleCheckout_DashPrefix(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "checkout-proj", Name: "checkout-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/git/checkout", h.HandleCheckout)

	// Dash-prefixed branch → 400
	body := strings.NewReader(`{"branch": "-foo"}`)
	req := httptest.NewRequest("POST", "/api/projects/checkout-proj/git/checkout", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for dash-prefixed checkout branch, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCheckout_ValidBranch(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	// Create a second branch to checkout (use git branch, not checkout -b)
	mustRun(t, projPath, "git", "branch", "feature-branch")
	proj := &types.Project{ID: "checkout-valid-proj", Name: "checkout-valid-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/git/checkout", h.HandleCheckout)

	// Valid branch → 200
	body := strings.NewReader(`{"branch": "feature-branch"}`)
	req := httptest.NewRequest("POST", "/api/projects/checkout-valid-proj/git/checkout", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid checkout branch, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- HandleTaskCheckout tests ---

func TestHandleTaskCheckout_DashPrefix(t *testing.T) {
	h, s, wm, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "taskcheckout-proj", Name: "taskcheckout-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	task := &types.Task{
		ID:        "taskcheckout-task",
		ProjectID: proj.ID,
		Title:     "checkout task",
		Status:    "in_progress",
		Branch:    "main",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(worktreesDir, proj.ID, task.ID)
	createWorktreeAt(t, projPath, wtPath, "main")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/git/checkout", h.HandleTaskCheckout)
	_ = wm

	// Dash-prefixed branch → 400
	body := strings.NewReader(`{"branch": "-foo"}`)
	req := httptest.NewRequest("POST", "/api/projects/taskcheckout-proj/tasks/taskcheckout-task/git/checkout", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for dash-prefixed task checkout branch, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleTaskCheckout_ValidBranch(t *testing.T) {
	h, s, wm, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	// Create a branch in the project repo (not checked out by any worktree)
	mustRun(t, projPath, "git", "branch", "feature-task")
	proj := &types.Project{ID: "taskcheckout-valid-proj", Name: "taskcheckout-valid-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	task := &types.Task{
		ID:        "taskcheckout-valid-task",
		ProjectID: proj.ID,
		Title:     "valid checkout task",
		Status:    "in_progress",
		Branch:    "main",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(worktreesDir, proj.ID, task.ID)
	createWorktreeAt(t, projPath, wtPath, "main")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/git/checkout", h.HandleTaskCheckout)
	_ = wm

	// Valid branch → 200
	body := strings.NewReader(`{"branch": "feature-task"}`)
	req := httptest.NewRequest("POST", "/api/projects/taskcheckout-valid-proj/tasks/taskcheckout-valid-task/git/checkout", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid task checkout branch, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- HandlePull / HandlePush tests ---

// addBareRemote creates a bare repo, registers it as "origin" in projPath,
// and pushes the current branch with upstream tracking. Returns the bare path.
func addBareRemote(t *testing.T, projPath string) string {
	t.Helper()
	bare := t.TempDir()
	mustRun(t, "", "git", "init", "--bare", bare)
	mustRun(t, projPath, "git", "remote", "add", "origin", bare)
	mustRun(t, projPath, "git", "push", "-u", "origin", "HEAD")
	return bare
}

// decodeGitError parses a JSON error response body and returns the message.
func decodeGitError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Error string }
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v: %s", err, rec.Body.String())
	}
	return body.Error
}

// newPullPushMux wires the pull/push routes exactly like the server does.
func newPullPushMux(h *GitHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/git/pull", h.HandlePull)
	mux.HandleFunc("POST /api/projects/{projectID}/git/push", h.HandlePush)
	return mux
}

func TestHandlePull_RequiresPOST(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "pull-method-proj", Name: "pull-method-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newPullPushMux(h)

	// GET is not allowed for the pull route.
	req := httptest.NewRequest("GET", "/api/projects/pull-method-proj/git/pull", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET pull, got %d: %s", rec.Code, rec.Body.String())
	}

	// GET is not allowed for the push route.
	req = httptest.NewRequest("GET", "/api/projects/pull-method-proj/git/push", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET push, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePush_Success(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	addBareRemote(t, projPath)
	proj := &types.Project{ID: "push-success-proj", Name: "push-success-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	// Create a new local commit to push.
	createFile(t, projPath, "pushed-file.txt", "pushed\n")
	mustRun(t, projPath, "git", "add", "pushed-file.txt")
	mustRun(t, projPath, "git", "commit", "-m", "commit to push")
	localHead, err := exec.Command("git", "-C", projPath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	mux := newPullPushMux(h)
	req := httptest.NewRequest("POST", "/api/projects/push-success-proj/git/push", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for push, got %d: %s", rec.Code, rec.Body.String())
	}

	// The bare remote must now contain the local commit.
	bare, err := exec.Command("git", "-C", projPath, "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatalf("remote get-url: %v", err)
	}
	remoteHead, err := exec.Command("git", "-C", strings.TrimSpace(string(bare)), "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse remote HEAD: %v", err)
	}
	if strings.TrimSpace(string(remoteHead)) != strings.TrimSpace(string(localHead)) {
		t.Fatalf("remote HEAD %q != local HEAD %q", remoteHead, localHead)
	}
}

func TestHandlePull_Success(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	bare := addBareRemote(t, projPath)
	proj := &types.Project{ID: "pull-success-proj", Name: "pull-success-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	// Advance the remote via a second clone so the local repo is behind.
	clone := t.TempDir()
	mustRun(t, "", "git", "clone", bare, clone)
	createFile(t, clone, "remote-file.txt", "from remote\n")
	mustRun(t, clone, "git", "add", "remote-file.txt")
	mustRun(t, clone, "git", "commit", "-m", "remote commit")
	mustRun(t, clone, "git", "push")

	mux := newPullPushMux(h)
	req := httptest.NewRequest("POST", "/api/projects/pull-success-proj/git/pull", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for pull, got %d: %s", rec.Code, rec.Body.String())
	}

	// The pulled commit must now be in the local repo.
	if _, err := os.Stat(filepath.Join(projPath, "remote-file.txt")); err != nil {
		t.Fatalf("expected remote-file.txt after pull: %v", err)
	}
}

func TestHandlePull_NotAGitRepo_ErrorIncludesGitOutput(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	// A plain directory (not a git repo) gives a deterministic git error
	// whose wording is stable across git versions.
	projPath := t.TempDir()
	proj := &types.Project{ID: "pull-err-proj", Name: "pull-err-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newPullPushMux(h)
	req := httptest.NewRequest("POST", "/api/projects/pull-err-proj/git/pull", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for pull on non-git dir, got %d: %s", rec.Code, rec.Body.String())
	}
	msg := decodeGitError(t, rec)
	if !strings.Contains(msg, "not a git repository") {
		t.Fatalf("error message should include git output, got %q", msg)
	}
}

func TestHandlePush_NotAGitRepo_ErrorIncludesGitOutput(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	proj := &types.Project{ID: "push-err-proj", Name: "push-err-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newPullPushMux(h)
	req := httptest.NewRequest("POST", "/api/projects/push-err-proj/git/push", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for push on non-git dir, got %d: %s", rec.Code, rec.Body.String())
	}
	msg := decodeGitError(t, rec)
	if !strings.Contains(msg, "not a git repository") {
		t.Fatalf("error message should include git output, got %q", msg)
	}
}

// --- HandleProjectStageFile_DashFPathNotForceAdd ---
// TestHandleProjectStageFile_DashFPathNotForceAdd verifies that a path of
// exactly "-f" is treated as a filename (via '--'), not as the --force flag.
// Since no file named "-f" exists, git add -- -f fails (500) rather than
// silently force-adding unrelated tracked files.
func TestHandleProjectStageFile_DashFPathNotForceAdd(t *testing.T) {
	repoPath := initGitRepoTemp(t)
	s := gitTestStore(t)

	proj := &types.Project{ID: "test-proj", Name: "test", Path: repoPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a tracked file so there is content, but do NOT create a file named "-f".
	createFile(t, repoPath, "real-file.txt", "tracked\n")
	if out, err := exec.Command("git", "-C", repoPath, "add", "--", "real-file.txt").CombinedOutput(); err != nil {
		t.Fatalf("initial git add: %v: %s", err, out)
	}

	h := &GitHandler{store: s}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/git/stage", h.HandleProjectStageFile)

	// With "--" before the path, "-f" is treated as a filename, not --force.
	req := newStageRequest(t, "/api/projects/test-proj/git/stage", "-f", true)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// git add -- -f fails because no file named "-f" exists, so we expect 500.
	// Critically, this is NOT 200 (which would indicate -f was treated as --force).
	if rec.Code == http.StatusOK {
		t.Fatalf("path '-f' should be treated as a filename and fail, not as --force (got 200)")
	}
}
