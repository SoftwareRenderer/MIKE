package managers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/stream"
	"kanban/backend/store"
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

	s.SetConfigValue("workspace_dir", worktreesDir)
	s.SetConfigValue("providers", "[]")
	s.SetConfigValue("worker_provider", "")
	s.SetConfigValue("worker_model", "")
	s.SetConfigValue("utility_provider", "")
	s.SetConfigValue("utility_model", "")

	cfgStore := config.NewConfigStore(s, dbPath, "test")
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
