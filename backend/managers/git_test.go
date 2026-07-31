package managers

import (
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
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, path, "git", "init")
	mustRun(t, path, "git", "config", "user.email", "test@test.com")
	mustRun(t, path, "git", "config", "user.name", "Test")
	mustRun(t, path, "git", "add", "README.md")
	mustRun(t, path, "git", "commit", "-m", "Initial commit")
}

// initGitRepoTemp creates a temp directory, initializes a bare git repo there
// (no commits, no user config), and returns the repo path. Used by tests that
// only need a git working tree for staging.
func initGitRepoTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v: %s", err, out)
	}
	exec.Command("git", "-C", dir, "symbolic-ref", "HEAD", "refs/heads/main").Run()
	return dir
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

	s.SetConfigValue("workspace_dir", worktreesDir)
	s.SetConfigValue("providers", "[]")
	s.SetConfigValue("default_provider", "test-provider")

	cfgStore := config.NewConfigStore(s, dbPath, "test")
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

// --- HandleLog tests ---

func TestHandleLog_BranchValidation_DashPrefix(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "log-proj", Name: "log-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)

	// Dash-prefixed branch → 400
	rr := doRequest(t, mux, "GET", "/api/projects/log-proj/git/log?branch=-foo")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for dash-prefixed branch, got %d: %s", rr.Code, rr.Body.String())
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

// --- HandleDiff tests ---

func TestHandleDiff_BaseValidation_DashPrefix(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "diff-proj", Name: "diff-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)

	// Dash-prefixed base → 400
	rr := doRequest(t, mux, "GET", "/api/projects/diff-proj/git/diff?base=-foo&head=HEAD")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for dash-prefixed base, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleDiff_HeadValidation_DashPrefix(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "diff-proj-h", Name: "diff-proj-h", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)

	// Dash-prefixed head → 400
	rr := doRequest(t, mux, "GET", "/api/projects/diff-proj-h/git/diff?base=HEAD&head=-bar")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for dash-prefixed head, got %d: %s", rr.Code, rr.Body.String())
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

// --- HandleTaskLog tests ---

func TestHandleTaskLog_BranchValidation_DashPrefix(t *testing.T) {
	h, s, wm, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "tasklog-proj", Name: "tasklog-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	task := &types.Task{
		ID:        "tasklog-task",
		ProjectID: proj.ID,
		Title:     "log task",
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

	// Dash-prefixed branch → 400
	rr := doRequest(t, mux, "GET", "/api/projects/tasklog-proj/tasks/tasklog-task/git/log?branch=-foo")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for dash-prefixed branch, got %d: %s", rr.Code, rr.Body.String())
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

// --- HandleTaskDiff tests ---

func TestHandleTaskDiff_BaseValidation_DashPrefix(t *testing.T) {
	h, s, wm, _ := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "taskdiff-proj", Name: "taskdiff-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	task := &types.Task{
		ID:        "taskdiff-task",
		ProjectID: proj.ID,
		Title:     "diff task",
		Status:    "in_progress",
		Branch:    "main",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)
	_ = wm

	// Dash-prefixed base → 400
	rr := doRequest(t, mux, "GET", "/api/projects/taskdiff-proj/tasks/taskdiff-task/diff?base=-foo")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for dash-prefixed base, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleTaskDiff_HeadValidation_DashPrefix(t *testing.T) {
	h, s, wm, _ := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "taskdiff-proj-h", Name: "taskdiff-proj-h", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	task := &types.Task{
		ID:        "taskdiff-task-h",
		ProjectID: proj.ID,
		Title:     "diff task head",
		Status:    "in_progress",
		Branch:    "main",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)
	_ = wm

	// Dash-prefixed head → 400
	rr := doRequest(t, mux, "GET", "/api/projects/taskdiff-proj-h/tasks/taskdiff-task-h/diff?head=-bar")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for dash-prefixed head, got %d: %s", rr.Code, rr.Body.String())
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

func TestHandleLog_InvalidCharacters(t *testing.T) {
	_, mux := setupHandleLogProject(t)
	for _, ch := range invalidBranchChars {
		url := "/api/projects/log-char-proj/git/log?branch=" + ch
		if ch == " " {
			url = "/api/projects/log-char-proj/git/log?branch=feat%20branch"
		}
		t.Run(ch, func(t *testing.T) {
			rr := doRequest(t, mux, "GET", url)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for invalid character %q, got %d: %s", ch, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestHandleDiff_InvalidCharacters(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "diff-char-proj", Name: "diff-char-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	mux := newTestGitMux(h)

	for _, ch := range invalidBranchChars {
		val := "feat" + ch + "branch"
		if ch == " " {
			val = "feat%20branch"
		}
		t.Run("base_"+ch, func(t *testing.T) {
			url := "/api/projects/diff-char-proj/git/diff?base=" + val + "&head=HEAD"
			rr := doRequest(t, mux, "GET", url)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for invalid base char %q, got %d: %s", ch, rr.Code, rr.Body.String())
			}
		})
		t.Run("head_"+ch, func(t *testing.T) {
			url := "/api/projects/diff-char-proj/git/diff?base=HEAD&head=" + val
			rr := doRequest(t, mux, "GET", url)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for invalid head char %q, got %d: %s", ch, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestHandleTaskDiff_InvalidCharacters(t *testing.T) {
	h, s, wm, _ := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "taskdiff-char-proj", Name: "taskdiff-char-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{
		ID:        "taskdiff-char-task",
		ProjectID: proj.ID,
		Title:     "char diff task",
		Status:    "in_progress",
		Branch:    "main",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	mux := newTestGitMux(h)
	_ = wm

	for _, ch := range invalidBranchChars {
		val := "feat" + ch + "branch"
		if ch == " " {
			val = "feat%20branch"
		}
		t.Run("base_"+ch, func(t *testing.T) {
			url := "/api/projects/taskdiff-char-proj/tasks/taskdiff-char-task/diff?base=" + val
			rr := doRequest(t, mux, "GET", url)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for invalid base char %q, got %d: %s", ch, rr.Code, rr.Body.String())
			}
		})
		t.Run("head_"+ch, func(t *testing.T) {
			url := "/api/projects/taskdiff-char-proj/tasks/taskdiff-char-task/diff?head=" + val
			rr := doRequest(t, mux, "GET", url)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for invalid head char %q, got %d: %s", ch, rr.Code, rr.Body.String())
			}
		})
	}
}

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
	t.Setenv("MIKE_WORKTREES_DIR", worktreesDir)

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

// --- HandleTaskLog invalid character tests ---
// These mirror the HandleLog/HandleDiff invalid character tests for the
// task-level log endpoint.

func TestHandleTaskLog_InvalidCharacters(t *testing.T) {
	h, s, wm, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "tasklog-char-proj", Name: "tasklog-char-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	task := &types.Task{
		ID:        "tasklog-char-task",
		ProjectID: proj.ID,
		Title:     "char log task",
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

	for _, ch := range invalidBranchChars {
		url := "/api/projects/tasklog-char-proj/tasks/tasklog-char-task/git/log?branch=" + ch
		if ch == " " {
			url = "/api/projects/tasklog-char-proj/tasks/tasklog-char-task/git/log?branch=feat%20branch"
		}
		t.Run(ch, func(t *testing.T) {
			rr := doRequest(t, mux, "GET", url)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for invalid character %q in task log branch, got %d: %s", ch, rr.Code, rr.Body.String())
			}
		})
	}
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
