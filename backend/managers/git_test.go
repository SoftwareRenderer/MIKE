package managers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/types"
)

// --- Test helpers ---

// mustRun executes a command in dir, failing the test on error.
func mustRun(t *testing.T, dir, cmd string, args ...string) {
	t.Helper()
	c := exec.Command(cmd, args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", cmd, args, err, out)
	}
}

// initGitRepo creates a git repo with one committed file.
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

// createWorktreeAt creates a git worktree at worktreePath with the given branch.
func createWorktreeAt(t *testing.T, projectPath, worktreePath, branch string) {
	t.Helper()
	mustRun(t, projectPath, "git", "worktree", "add", "-B", branch, worktreePath)
}

// newTestGitHandler creates a GitHandler backed by a temporary store, config, and
// worktree manager. Returns the handler, store, worktree manager, and the worktrees
// directory path.
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

	// Set required config values so that ConfigStore.GetConfig() succeeds.
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

// doRequest sends a GET request through the mux and returns the recorded response.
func doRequest(t *testing.T, mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// --- HandleLog tests ---

func TestHandleLog_BranchValidation_DashPrefix(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	// Create a project with a git repo
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

	// Create a project git repo with a commit
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "tasklog-proj", Name: "tasklog-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	// Create a task
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

	// Create the worktree at the expected path so WorktreePath resolves.
	wtPath := filepath.Join(worktreesDir, proj.ID, task.ID)
	createWorktreeAt(t, projPath, wtPath, "main")

	mux := newTestGitMux(h)
	_ = wm // worktree manager is non-nil via handler

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

	// Create the worktree at the expected path so git commands inside the worktree succeed.
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
