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

// initGitRepo creates a temp directory, initializes a git repo there,
// and returns the repo path.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v: %s", err, out)
	}
	// Ensure a deterministic default branch.
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

// testStore creates a temp store and registers cleanup.
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

// TestHandleProjectStageFile_PathStartingWithDash verifies that a path
// starting with '-' is safely staged (treated as a filename, not a git flag)
// thanks to the '--' separator before the path argument in `git add`.
//
// Without the '--', `git add -dash-file.txt` would be interpreted as
// `git add -d -a -s -h ...` (short options) and fail with an "unknown switch"
// error, leaving the file unstaged.
func TestHandleProjectStageFile_PathStartingWithDash(t *testing.T) {
	repoPath := initGitRepo(t)
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

	repoPath := initGitRepo(t)
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

// TestHandleProjectStageFile_DashFPathNotForceAdd verifies that a path of
// exactly "-f" is treated as a filename (via '--'), not as the --force flag.
// Since no file named "-f" exists, git add -- -f fails (500) rather than
// silently force-adding unrelated tracked files.
func TestHandleProjectStageFile_DashFPathNotForceAdd(t *testing.T) {
	repoPath := initGitRepo(t)
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
