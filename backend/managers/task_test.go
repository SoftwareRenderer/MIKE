package managers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/exec"
	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"
)

// --- Test helpers for task handler validation tests ---

// newTestTaskHandler creates a minimal TaskHandler backed by a temporary store.
func newTestTaskHandler(t *testing.T) (*TaskHandler, *store.Store, string) {
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
	boardMgr := stream.NewAppStreamManager(s, wm)
	h := NewTaskHandler(s, boardMgr, cfgStore, nil, nil, wm, nil, nil)

	return h, s, worktreesDir
}

// newTaskMux creates a ServeMux wired with task handler routes.
func newTaskMux(h *TaskHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{projectID}/tasks", h.HandleList)
	mux.HandleFunc("POST /api/projects/{projectID}/tasks", h.HandleList)
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}", h.HandleByID)
	mux.HandleFunc("PUT /api/projects/{projectID}/tasks/{taskID}", h.HandleByID)
	mux.HandleFunc("DELETE /api/projects/{projectID}/tasks/{taskID}", h.HandleByID)
	return mux
}

// doTaskRequest sends a request through the task mux and returns the response.
func doTaskRequest(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// setupTaskValidationProject creates a project and task for PUT validation tests.
// Uses hardcoded IDs "task-val-proj" and "task-val-task".
func setupTaskValidationProject(t *testing.T, h *TaskHandler, s *store.Store) {
	t.Helper()
	projPath := t.TempDir()
	proj := &types.Project{ID: "task-val-proj", Name: "task-val-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{
		ID:        "task-val-task",
		ProjectID: proj.ID,
		Title:     "validation task",
		Status:    "backlog",
		Branch:    "main",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
}

// --- PUT handler branch validation tests ---
// These tests verify that the PUT /api/projects/{projectID}/tasks/{taskID} handler
// properly validates branch, base_ref, and branch_tip fields using ValidateBranchName
// to prevent git argument injection.

func TestTaskHandler_PUT_BranchValidation_DashPrefix(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	// Dash-prefixed branch → 400
	rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
		"branch": "-f",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for dash-prefixed branch, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTaskHandler_PUT_BranchValidation_DoubleDash(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	// Double-dash branch → 400
	rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
		"branch": "--format=json",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for double-dash branch, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTaskHandler_PUT_BranchValidation_Empty(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	// Empty branch → 400
	rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
		"branch": "",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty branch, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTaskHandler_PUT_BranchValidation_Valid(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	// Valid branch → 200
	rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
		"branch": "feature/my-branch",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid branch, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify the branch was actually updated
	task, err := s.GetTask("task-val-proj", "task-val-task")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Branch != "feature/my-branch" {
		t.Fatalf("expected branch 'feature/my-branch', got %q", task.Branch)
	}
}

func TestTaskHandler_PUT_BranchValidation_InvalidChars(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	invalidBranches := []string{"feat~1", "feat^head", "feat:branch", "feat branch", "feat..branch"}
	for _, branch := range invalidBranches {
		t.Run(strings.ReplaceAll(branch, " ", "_"), func(t *testing.T) {
			rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
				"branch": branch,
			})
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for branch %q, got %d: %s", branch, rr.Code, rr.Body.String())
			}
		})
	}
}

// --- PUT handler base_ref validation tests ---

func TestTaskHandler_PUT_BaseRefValidation_DashPrefix(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	// Dash-prefixed base_ref → 400
	rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
		"base_ref": "-C/tmp/evil",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for dash-prefixed base_ref, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTaskHandler_PUT_BaseRefValidation_Valid(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	// Valid base_ref → 200
	rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
		"base_ref": "main",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid base_ref, got %d: %s", rr.Code, rr.Body.String())
	}

	task, err := s.GetTask("task-val-proj", "task-val-task")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.BaseRef != "main" {
		t.Fatalf("expected base_ref 'main', got %q", task.BaseRef)
	}
}

func TestTaskHandler_PUT_BaseRefValidation_InvalidChars(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	invalidRefs := []string{"feat~1", "feat..branch", "feat branch"}
	for _, ref := range invalidRefs {
		t.Run(strings.ReplaceAll(ref, " ", "_"), func(t *testing.T) {
			rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
				"base_ref": ref,
			})
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for base_ref %q, got %d: %s", ref, rr.Code, rr.Body.String())
			}
		})
	}
}

// --- PUT handler branch_tip validation tests ---

func TestTaskHandler_PUT_BranchTipValidation_DashPrefix(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	// Dash-prefixed branch_tip → 400
	rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
		"branch_tip": "--dry-run",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for dash-prefixed branch_tip, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTaskHandler_PUT_BranchTipValidation_Valid(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	// Valid branch_tip (commit hash) → 200
	rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
		"branch_tip": "abc123def456",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid branch_tip, got %d: %s", rr.Code, rr.Body.String())
	}

	task, err := s.GetTask("task-val-proj", "task-val-task")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.BranchTip != "abc123def456" {
		t.Fatalf("expected branch_tip 'abc123def456', got %q", task.BranchTip)
	}
}

func TestTaskHandler_PUT_BranchTipValidation_InvalidChars(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	invalidTips := []string{"abc~1", "abc..def", "abc def"}
	for _, tip := range invalidTips {
		t.Run(strings.ReplaceAll(tip, " ", "_"), func(t *testing.T) {
			rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
				"branch_tip": tip,
			})
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for branch_tip %q, got %d: %s", tip, rr.Code, rr.Body.String())
			}
		})
	}
}

// --- Combined validation: multiple fields at once ---

func TestTaskHandler_PUT_MultipleFields_Valid(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	// All three git ref fields valid → 200
	rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
		"branch":     "feature/test",
		"base_ref":   "main",
		"branch_tip": "abc123",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for all valid fields, got %d: %s", rr.Code, rr.Body.String())
	}

	task, err := s.GetTask("task-val-proj", "task-val-task")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Branch != "feature/test" {
		t.Fatalf("expected branch 'feature/test', got %q", task.Branch)
	}
	if task.BaseRef != "main" {
		t.Fatalf("expected base_ref 'main', got %q", task.BaseRef)
	}
	if task.BranchTip != "abc123" {
		t.Fatalf("expected branch_tip 'abc123', got %q", task.BranchTip)
	}
}

func TestTaskHandler_PUT_MultipleFields_OneInvalid(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := newTaskMux(h)

	// Valid branch + valid base_ref + INVALID branch_tip → 400
	rr := doTaskRequest(t, mux, "PUT", "/api/projects/task-val-proj/tasks/task-val-task", map[string]string{
		"branch":     "feature/test",
		"base_ref":   "main",
		"branch_tip": "-f",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when branch_tip is invalid, got %d: %s", rr.Code, rr.Body.String())
	}
}

// --- Existing tests (preserved below) ---

func TestEnrichWorktreeExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Create worktree directories: workspaceDir/projectID/taskID
	projectID := "test-project"
	taskID1 := "task-1"
	taskID2 := "task-2"

	// task-1 has a worktree, task-2 does not
	os.MkdirAll(filepath.Join(tmpDir, projectID, taskID1), 0o755)

	tasks := []types.Task{
		{ID: taskID1, ProjectID: projectID},
		{ID: taskID2, ProjectID: projectID},
	}

	EnrichWorktreeExists(tasks, tmpDir)

	if !tasks[0].WorktreeExists {
		t.Error("task-1 should have worktree_exists=true")
	}
	if tasks[1].WorktreeExists {
		t.Error("task-2 should have worktree_exists=false")
	}
}

func TestEnrichWorktreeExists_UsesProjectID(t *testing.T) {
	tmpDir := t.TempDir()

	// Create worktree at the CORRECT path: workspaceDir/projectID/taskID
	projectID := "my-project"
	taskID := "my-task"
	os.MkdirAll(filepath.Join(tmpDir, projectID, taskID), 0o755)

	// Also create a directory at the WRONG path: workspaceDir/taskID (no projectID)
	// This should NOT match
	os.MkdirAll(filepath.Join(tmpDir, taskID), 0o755)

	tasks := []types.Task{
		{ID: taskID, ProjectID: projectID},
	}

	EnrichWorktreeExists(tasks, tmpDir)

	// Should find the worktree at workspaceDir/projectID/taskID
	if !tasks[0].WorktreeExists {
		t.Error("task should have worktree_exists=true (found at workspaceDir/projectID/taskID)")
	}
}

func TestEnrichTaskWorktree(t *testing.T) {
	tmpDir := t.TempDir()
	projectID := "proj"
	taskID := "task"
	os.MkdirAll(filepath.Join(tmpDir, projectID, taskID), 0o755)

	task := &types.Task{ID: taskID, ProjectID: projectID}
	enrichTaskWorktree(task, tmpDir)

	if !task.WorktreeExists {
		t.Error("task should have worktree_exists=true")
	}
}

// --- Auto-answer provenance (autostarted) ---
//
// Starting a parent task runs its subtasks autonomously (no human present to
// answer ask_question). The selected subtask must therefore be marked
// autostarted=true, so the auto-answer system answers its questions from parent
// context instead of stopping to wait for a user.

// gitRun executes a git command in dir.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := osexec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// gitOutput runs a git command in dir and returns its stdout.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := osexec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// TestHandleCommitAndDone_ExcludesUnstagedFiles: untracked worktree files stay local.
func TestHandleCommitAndDone_ExcludesUnstagedFiles(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	h.commitQueue = exec.NewCommitQueue()

	proj, err := s.GetProject(projID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}

	task := &types.Task{
		ID:              "cdn-unstaged",
		ProjectID:       projID,
		Title:           "Commit and done unstaged",
		Prompt:          "make changes",
		Status:          "review",
		Branch:          "task/cdn-unstaged",
		CurrentThreadID: "thread-cdn",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	wtPath, _, err := h.worktreeMgr.CreateWorktree(projID, task.ID, task.Branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Staged change (should be committed) and untracked file (must not be).
	if err := os.WriteFile(filepath.Join(wtPath, "staged.txt"), []byte("staged content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wtPath, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(wtPath, "scratch.txt"), []byte("scratch content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/commit", h.HandleCommitAndDone)
	body, _ := json.Marshal(map[string]string{"commit_message": "test commit"})
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projID+"/tasks/"+task.ID+"/commit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("commit-and-done status = %d, body=%s", rr.Code, rr.Body.String())
	}

	updated, err := s.GetTask(projID, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updated.Status != "done" {
		t.Fatalf("task status = %q, want done", updated.Status)
	}

	// Untracked file must remain in the worktree...
	if _, err := os.Stat(filepath.Join(wtPath, "scratch.txt")); err != nil {
		t.Errorf("scratch.txt should remain in the worktree: %v", err)
	}
	// ...and must not exist in the project's main folder.
	if _, err := os.Stat(filepath.Join(proj.Path, "scratch.txt")); !os.IsNotExist(err) {
		t.Errorf("scratch.txt must not be copied to the project dir (stat err=%v)", err)
	}
	// It must not be part of the merged branch either.
	if out := gitOutput(t, proj.Path, "log", "--all", "--oneline", "--", "scratch.txt"); strings.TrimSpace(out) != "" {
		t.Errorf("scratch.txt appears in project branch history: %s", out)
	}
	// And it must not show up as untracked content in the project status.
	if status := gitOutput(t, proj.Path, "status", "--porcelain"); strings.Contains(status, "scratch.txt") {
		t.Errorf("scratch.txt appears in project git status: %s", status)
	}
	// The staged change must still be committed and merged into the project.
	if out := gitOutput(t, proj.Path, "show", "HEAD:staged.txt"); !strings.Contains(out, "staged content") {
		t.Errorf("staged.txt not merged into project HEAD, got: %q", out)
	}
}

// newTaskHandlerWithExecutor builds a TaskHandler wired to a real executor,
// backed by a temp git repo (needed for subtask worktree/branch setup).
func newTaskHandlerWithExecutor(t *testing.T) (*TaskHandler, *store.Store, string) {
	t.Helper()
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	gitRun(t, repoDir, "init")
	gitRun(t, repoDir, "config", "user.email", "test@test.com")
	gitRun(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoDir, "add", "README.md")
	gitRun(t, repoDir, "commit", "-m", "Initial")

	dbPath := filepath.Join(tmpDir, "test.db")
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

	projID := "proj-auto-start"
	proj := &types.Project{ID: projID, Name: "auto-start", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	worktreeMgr := git.NewWorktreeManager(s, cfgStore)
	boardMgr := stream.NewAppStreamManager(s, worktreeMgr)

	executor := exec.NewTaskExecutor(s, cfgStore, boardMgr, worktreeMgr, nil, nil, nil, nil)
	h := NewTaskHandler(s, boardMgr, cfgStore, nil, executor, worktreeMgr, nil, nil)
	return h, s, projID
}

// createParentWithSubtask creates an in_progress parent with one subtask whose
// thread already ends in an assistant message. This lets StartTask persist the
// autostarted flag and return early without invoking the LLM.
func createParentWithSubtask(t *testing.T, s *store.Store, projectID string) (string, string) {
	t.Helper()
	now := time.Now().UTC()

	parent := &types.Task{
		ID:              "parent-auto-start",
		ProjectID:       projectID,
		Title:           "Parent Auto Start",
		Prompt:          "Parent prompt",
		Status:          "backlog",
		ExecutionState:  "idle",
		LlamaServerURL:  "http://localhost:8080",
		ModelName:       "test-model",
		OperatingMode:   "act",
		CurrentThreadID: "thread-parent",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.CreateTask(parent); err != nil {
		t.Fatal(err)
	}

	child := &types.Task{
		ID:              "child-auto-start",
		ProjectID:       projectID,
		Title:           "Child Auto Start",
		Prompt:          "Child prompt",
		Status:          "backlog",
		ExecutionState:  "idle",
		ParentTaskID:    parent.ID,
		LlamaServerURL:  "http://localhost:8080",
		ModelName:       "test-model",
		OperatingMode:   "act",
		CurrentThreadID: "thread-child",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.CreateTask(child); err != nil {
		t.Fatal(err)
	}

	// Seed the child thread so StartTask returns early (no LLM) after persisting
	// the autostarted flag: a user prompt message followed by an assistant reply.
	for _, m := range []*types.TaskMessage{
		{ID: "u1", TaskID: child.ID, ThreadID: child.CurrentThreadID, Role: "user", Content: child.Prompt, CreatedAt: now},
		{ID: "a1", TaskID: child.ID, ThreadID: child.CurrentThreadID, Role: "assistant", Content: "Done.", CreatedAt: now},
	} {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	return parent.ID, child.ID
}

// TestHandleStart_MarksSubtaskAutostarted verifies that starting a
// parent task with subtasks marks the selected subtask autostarted=true, so its
// ask_question calls are auto-answered from parent context.
func TestHandleStart_MarksSubtaskAutostarted(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	parentID, childID := createParentWithSubtask(t, s, projID)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/start", h.HandleStart)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projID+"/tasks/"+parentID+"/start", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("HandleStart status = %d, body=%s", rr.Code, rr.Body.String())
	}

	child, err := s.GetTaskByID(childID)
	if err != nil {
		t.Fatalf("get child: %v", err)
	}
	if !child.Autostarted {
		t.Errorf("child task should be marked autostarted=true when started via its parent (got %v)", child.Autostarted)
	}
}

// --- Autostart chain: start paths (chain = started-but-pending, deepest first) ---

// createChainTask creates a task for chain tests. When seed is true, the
// task's thread is seeded with a user + assistant message pair so StartTask
// persists state and returns early without invoking the LLM.
func createChainTask(t *testing.T, s *store.Store, projectID, id, title, status, execState, parentID, threadID string, seed bool) {
	t.Helper()
	now := time.Now().UTC()
	task := &types.Task{
		ID: id, ProjectID: projectID, Title: title, Prompt: title + " prompt",
		Status: status, ExecutionState: execState, ParentTaskID: parentID,
		LlamaServerURL: "http://localhost:8080", ModelName: "test-model",
		OperatingMode: "act", CurrentThreadID: threadID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	if seed && threadID != "" {
		for _, m := range []*types.TaskMessage{
			{ID: id + "-u", TaskID: id, ThreadID: threadID, Role: "user", Content: task.Prompt, CreatedAt: now},
			{ID: id + "-a", TaskID: id, ThreadID: threadID, Role: "assistant", Content: "Done.", CreatedAt: now},
		} {
			if err := s.CreateTaskMessage(m); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func postToMux(t *testing.T, mux *http.ServeMux, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// TestHandleStart_MovesParentToInProgress verifies that starting a
// backlog parent via POST /start moves the parent itself to In Progress
// (started-but-pending) while the deepest subtask starts running.
func TestHandleStart_MovesParentToInProgress(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "par-mip", "Parent", "backlog", "idle", "", "thread-par-mip", false)
	createChainTask(t, s, projID, "child-mip", "Child", "backlog", "idle", "par-mip", "thread-child-mip", true)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/start", h.HandleStart)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/par-mip/start", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("HandleStart status = %d, body=%s", rr.Code, rr.Body.String())
	}

	parent, _ := s.GetTaskByID("par-mip")
	if parent.Status != "in_progress" {
		t.Errorf("parent should move to In Progress (started-but-pending), got %q", parent.Status)
	}
	if parent.ExecutionState != "idle" {
		t.Errorf("parent should stay pending (not running), got %q", parent.ExecutionState)
	}

	child, _ := s.GetTaskByID("child-mip")
	// The seeded child's conversation already ends in an assistant message, so
	// StartTask skips the LLM call and settles into idle (not a stale running).
	if child.Status != "in_progress" || child.ExecutionState != "idle" {
		t.Errorf("deepest subtask should be started-but-idle: status=%q state=%q", child.Status, child.ExecutionState)
	}
	if !child.Autostarted {
		t.Error("subtask started via its parent should be marked autostarted")
	}
}

// TestHandleStart_AllChildrenDoneStartsOwnExecution verifies that
// starting a parent whose subtasks are all done starts the parent's own
// execution.
func TestHandleStart_AllChildrenDoneStartsOwnExecution(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "par-ad", "Parent", "backlog", "idle", "", "thread-par-ad", true)
	createChainTask(t, s, projID, "child-ad", "Child", "done", "completed", "par-ad", "", false)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/start", h.HandleStart)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/par-ad/start", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("HandleStart status = %d, body=%s", rr.Code, rr.Body.String())
	}

	parent, _ := s.GetTaskByID("par-ad")
	// The seeded parent's conversation already ends in an assistant message, so
	// StartTask skips the LLM call and settles into idle (not a stale running).
	if parent.Status != "in_progress" || parent.ExecutionState != "idle" {
		t.Errorf("parent should be started-but-idle once all children are done: status=%q state=%q", parent.Status, parent.ExecutionState)
	}
	if parent.Autostarted {
		t.Error("user-initiated start should not mark the task autostarted")
	}
}

// TestHandleStart_BlockedChildrenStayPending verifies that starting
// a parent whose subtask is in Review does not start anything — the parent
// stays started-but-pending until the child is moved to Done.
func TestHandleStart_BlockedChildrenStayPending(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "par-blk", "Parent", "backlog", "idle", "", "thread-par-blk", false)
	createChainTask(t, s, projID, "child-blk", "Child", "review", "idle", "par-blk", "", false)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/start", h.HandleStart)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/par-blk/start", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("HandleStart status = %d, body=%s", rr.Code, rr.Body.String())
	}

	parent, _ := s.GetTaskByID("par-blk")
	if parent.Status != "in_progress" || parent.ExecutionState != "idle" {
		t.Errorf("parent should be started-but-pending: status=%q state=%q", parent.Status, parent.ExecutionState)
	}
	child, _ := s.GetTaskByID("child-blk")
	if child.Status != "review" {
		t.Errorf("child should stay in review, got %q", child.Status)
	}
}

// TestHandleStartSubtask_WithChildren_StartsDeepestSubtask verifies that
// starting a subtask that has children of its own starts the deepest
// descendant — the subtask's own execution must wait for its children.
func TestHandleStartSubtask_WithChildren_StartsDeepestSubtask(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "root-nsp", "Root", "backlog", "idle", "", "", false)
	createChainTask(t, s, projID, "mid-nsp", "Mid", "backlog", "idle", "root-nsp", "thread-mid-nsp", false)
	createChainTask(t, s, projID, "leaf-nsp", "Leaf", "backlog", "idle", "mid-nsp", "thread-leaf-nsp", true)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/start-subtask", h.HandleStartSubtask)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/mid-nsp/start-subtask", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("HandleStartSubtask status = %d, body=%s", rr.Code, rr.Body.String())
	}

	mid, _ := s.GetTaskByID("mid-nsp")
	if mid.Status != "in_progress" || mid.ExecutionState != "idle" {
		t.Errorf("mid subtask should be started-but-pending: status=%q state=%q", mid.Status, mid.ExecutionState)
	}
	leaf, _ := s.GetTaskByID("leaf-nsp")
	// The seeded leaf's conversation already ends in an assistant message, so
	// StartTask skips the LLM call and settles into idle (not a stale running).
	if leaf.Status != "in_progress" || leaf.ExecutionState != "idle" {
		t.Errorf("deepest descendant should be started-but-idle: status=%q state=%q", leaf.Status, leaf.ExecutionState)
	}
	if !leaf.Autostarted {
		t.Error("deepest descendant should be marked autostarted")
	}
}

// TestHandleMove_SubtaskWithChildren_StartsDeepestSubtask verifies that moving
// a subtask with its own children to In Progress starts the deepest
// descendant, not the subtask's own execution.
func TestHandleMove_SubtaskWithChildren_StartsDeepestSubtask(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "root-msc", "Root", "backlog", "idle", "", "", false)
	createChainTask(t, s, projID, "mid-msc", "Mid", "backlog", "idle", "root-msc", "thread-mid-msc", false)
	createChainTask(t, s, projID, "leaf-msc", "Leaf", "backlog", "idle", "mid-msc", "thread-leaf-msc", true)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/move", h.HandleMove)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/mid-msc/move", map[string]any{"status": "in_progress"})
	if rr.Code != http.StatusOK {
		t.Fatalf("HandleMove status = %d, body=%s", rr.Code, rr.Body.String())
	}

	mid, _ := s.GetTaskByID("mid-msc")
	if mid.Status != "in_progress" || mid.ExecutionState != "idle" {
		t.Errorf("mid subtask should be started-but-pending: status=%q state=%q", mid.Status, mid.ExecutionState)
	}
	leaf, _ := s.GetTaskByID("leaf-msc")
	// The seeded leaf's conversation already ends in an assistant message, so
	// StartTask skips the LLM call and settles into idle (not a stale running).
	if leaf.Status != "in_progress" || leaf.ExecutionState != "idle" {
		t.Errorf("deepest descendant should be started-but-idle: status=%q state=%q", leaf.Status, leaf.ExecutionState)
	}
}

// TestHandleMove_ParentAllChildrenDone_StartsOwnExecution verifies that moving
// a parent with all-done subtasks to In Progress starts the parent's own
// execution.
func TestHandleMove_ParentAllChildrenDone_StartsOwnExecution(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "par-mad", "Parent", "backlog", "idle", "", "thread-par-mad", true)
	createChainTask(t, s, projID, "child-mad", "Child", "done", "completed", "par-mad", "", false)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/move", h.HandleMove)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/par-mad/move", map[string]any{"status": "in_progress"})
	if rr.Code != http.StatusOK {
		t.Fatalf("HandleMove status = %d, body=%s", rr.Code, rr.Body.String())
	}

	parent, _ := s.GetTaskByID("par-mad")
	// The seeded parent's conversation already ends in an assistant message, so
	// StartTask skips the LLM call and settles into idle (not a stale running).
	if parent.Status != "in_progress" || parent.ExecutionState != "idle" {
		t.Errorf("parent should be started-but-idle once all children are done: status=%q state=%q", parent.Status, parent.ExecutionState)
	}
}

// TestHandleMove_ParentWithGatedChildren_KeepsThemInPlace verifies that moving
// a parent to In Progress (a start) moves backlog descendants along but does
// not drag them out of review (human gate) or done.
func TestHandleMove_ParentWithGatedChildren_KeepsThemInPlace(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "par-gate", "Parent", "backlog", "idle", "", "thread-par-gate", true)
	createChainTask(t, s, projID, "child-gate-review", "InReview", "review", "idle", "par-gate", "", false)
	createChainTask(t, s, projID, "child-gate-done", "Done", "done", "completed", "par-gate", "", false)
	createChainTask(t, s, projID, "child-gate-backlog", "Backlog", "backlog", "idle", "par-gate", "thread-child-gate-backlog", true)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/move", h.HandleMove)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/par-gate/move", map[string]any{"status": "in_progress"})
	if rr.Code != http.StatusOK {
		t.Fatalf("HandleMove status = %d, body=%s", rr.Code, rr.Body.String())
	}

	parent, _ := s.GetTaskByID("par-gate")
	if parent.Status != "in_progress" || parent.ExecutionState != "idle" {
		t.Errorf("parent should stay started-but-pending: status=%q state=%q", parent.Status, parent.ExecutionState)
	}
	backlog, _ := s.GetTaskByID("child-gate-backlog")
	if backlog.Status != "in_progress" {
		t.Errorf("backlog child should follow parent to In Progress: status=%q", backlog.Status)
	}
	review, _ := s.GetTaskByID("child-gate-review")
	if review.Status != "review" {
		t.Errorf("review child must not be dragged into In Progress: status=%q", review.Status)
	}
	done, _ := s.GetTaskByID("child-gate-done")
	if done.Status != "done" {
		t.Errorf("done child must not be dragged into In Progress: status=%q", done.Status)
	}
}

// heldTitleLLM is an httptest LLM server that holds every chat request open
// until Release() is called, simulating a slow (or provider-gate-queued)
// title generation call. After release it answers with a JSON completion.
type heldTitleLLM struct {
	arrived chan struct{} // closed when the chat request arrives
	release chan struct{} // closed to release the held request
}

func newHeldTitleLLM() *heldTitleLLM {
	return &heldTitleLLM{arrived: make(chan struct{}), release: make(chan struct{})}
}

func (m *heldTitleLLM) Release() { close(m.release) }

func (m *heldTitleLLM) Server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(m.arrived)
		<-m.release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Generated Title"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
}

// TestTitleGenerationDoesNotClobberTaskStatus pins the stale full-row write
// bug: while the title-generation LLM call is in flight (e.g. queued on the
// provider concurrency gate behind another task's stream), the task gets
// started (backlog → in_progress). The title update must then write ONLY the
// title — writing back the row as read at creation time (status backlog)
// silently un-starts the task.
func TestTitleGenerationDoesNotClobberTaskStatus(t *testing.T) {
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

	mock := newHeldTitleLLM()
	server := mock.Server()
	defer server.Close()

	providers := `[{"name":"held","display_name":"Held","url":"` + server.URL + `","models":[{"name":"m1","reasoning_effort":"off"}],"concurrency":1}]`
	s.SetConfigValue("providers", providers)
	s.SetConfigValue("worker_provider", "")
	s.SetConfigValue("worker_model", "")
	s.SetConfigValue("utility_provider", "held")
	s.SetConfigValue("utility_model", "m1")

	cfgStore := config.NewConfigStore(s, dbPath, "test")
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	wm := git.NewWorktreeManager(s, cfgStore)
	boardMgr := stream.NewAppStreamManager(s, wm)
	te := exec.NewTaskExecutor(s, cfgStore, boardMgr, wm, nil, nil, nil, nil)
	h := NewTaskHandler(s, boardMgr, cfgStore, nil, te, wm, nil, nil)
	mux := newTaskMux(h)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "title-clobber-proj", Name: "Title Clobber", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	// Create the task (7-word prompt → title generation runs). The
	// title-generation goroutine reads the row, then its LLM call is held
	// open by the mock.
	rr := doTaskRequest(t, mux, http.MethodPost, "/api/projects/title-clobber-proj/tasks", map[string]any{
		"prompt":           "please generate a title for this task",
		"llama_server_url": server.URL,
		"model_name":       "m1",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("parse created task: %v", err)
	}
	taskID, _ := created["id"].(string)

	// Wait for the title call to reach the mock (the row was already read).
	select {
	case <-mock.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("title call never reached the mock LLM")
	}

	// Start the task while the title call is in flight.
	task, err := s.GetTask("title-clobber-proj", taskID)
	if err != nil {
		t.Fatal(err)
	}
	task.Status = "in_progress"
	task.ExecutionState = "running"
	task.Branch = "task/" + taskID
	if err := s.UpdateTask(task); err != nil {
		t.Fatal(err)
	}

	// Release the title call → the title update runs.
	mock.Release()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		done, _ := s.GetTask("title-clobber-proj", taskID)
		if done != nil && !done.TitleGenerating && done.Title == "Generated Title" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	task, err = s.GetTask("title-clobber-proj", taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "Generated Title" {
		t.Fatalf("title not updated: %q (title_generating=%v)", task.Title, task.TitleGenerating)
	}
	if task.Status != "in_progress" || task.ExecutionState != "running" || task.Branch != "task/"+taskID {
		t.Fatalf("title update clobbered task state: status=%s exec=%s branch=%q",
			task.Status, task.ExecutionState, task.Branch)
	}
}
