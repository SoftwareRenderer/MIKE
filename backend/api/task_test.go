package api

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

	"mike/backend/config"
	"mike/backend/exec"
	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/types"
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

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	wm := git.NewWorktreeManager(s, cfgStore)
	boardMgr := stream.NewAppStreamManager(s, wm)
	h := NewTaskHandler(TaskDeps{Store: s, BoardStream: boardMgr, ConfigStore: cfgStore, WorktreeMgr: wm})

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
		Status:    types.StatusBacklog,
		Branch:    "main",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
}

// --- POST create: operating_mode is the only spelling of the mode ---
//
// The refusal is what an old client sees; converting the alias instead would let it keep winning
// over an explicit operating_mode in the same body.
func TestTaskHandler_POST_RefusesTheRetiredPlanMode(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	if err := s.CreateProject(&types.Project{ID: "pm-proj", Name: "pm-proj", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	mux := newTaskMux(h)

	for _, body := range []map[string]any{
		{"prompt": "Fix the CLI flag", "plan_mode": true},
		{"prompt": "Fix the CLI flag", "plan_mode": false},
		{"prompt": "Fix the CLI flag", "plan_mode": true, "operating_mode": "act"},
		{"prompt": "Fix the CLI flag", "plan_mode": "yes"},
	} {
		rr := doTaskRequest(t, mux, http.MethodPost, "/api/projects/pm-proj/tasks", body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("plan_mode=%v: status = %d, want 400: %s", body["plan_mode"], rr.Code, rr.Body.String())
			continue
		}
		if !strings.Contains(rr.Body.String(), "operating_mode") {
			t.Errorf("refusal %q does not name the field to send instead", rr.Body.String())
		}
	}

	// The retired field is refused before anything is written: the four bodies above created no rows.
	tasks, err := s.GetTasksByProject("pm-proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Errorf("%d tasks were created by refused requests, want none", len(tasks))
	}

	rr := doTaskRequest(t, mux, http.MethodPost, "/api/projects/pm-proj/tasks",
		map[string]any{"prompt": "Fix the CLI flag", "operating_mode": "plan"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("operating_mode=plan: status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	var created types.Task
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.OperatingMode != types.ModePlan {
		t.Errorf("operating_mode = %q, want %q", created.OperatingMode, types.ModePlan)
	}
}

// --- PUT handler git-ref validation ---
//
// TestTaskHandler_PUT_RefValidation is the whole validation matrix for the three
// git-facing fields of PUT /api/projects/{projectID}/tasks/{taskID}. All three reach a
// git command line, so each must be rejected before it becomes an argument: dash-prefixed
// values (option injection), range/ref operators and spaces, and — for branch — the empty
// string. The accepted rows close the loop by reading the value back off the stored task,
// which is what separates "validated" from "silently dropped".
func TestTaskHandler_PUT_RefValidation(t *testing.T) {
	// stored reads a field back off the task so an accepted row can prove it landed.
	stored := map[string]func(task *types.Task) string{
		"branch":     func(task *types.Task) string { return task.Branch },
		"base_ref":   func(task *types.Task) string { return task.BaseRef },
		"branch_tip": func(task *types.Task) string { return task.BranchTip },
	}

	cases := []struct {
		field string
		value string
		want  int
	}{
		// branch: ValidateBranchName is the tighter of the two validators.
		{"branch", "-f", http.StatusBadRequest},
		{"branch", "--format=json", http.StatusBadRequest},
		{"branch", "", http.StatusBadRequest},
		{"branch", "feat~1", http.StatusBadRequest},
		{"branch", "feat^head", http.StatusBadRequest},
		{"branch", "feat:branch", http.StatusBadRequest},
		{"branch", "feat branch", http.StatusBadRequest},
		{"branch", "feat..branch", http.StatusBadRequest},
		{"branch", "feature/my-branch", http.StatusOK},

		// base_ref: a rev, so ValidateGitRef applies.
		{"base_ref", "-C/tmp/evil", http.StatusBadRequest},
		{"base_ref", "feat~1", http.StatusBadRequest},
		{"base_ref", "feat..branch", http.StatusBadRequest},
		{"base_ref", "feat branch", http.StatusBadRequest},
		{"base_ref", "main", http.StatusOK},

		// branch_tip: a commit, still validated for the same reason.
		{"branch_tip", "--dry-run", http.StatusBadRequest},
		{"branch_tip", "abc~1", http.StatusBadRequest},
		{"branch_tip", "abc..def", http.StatusBadRequest},
		{"branch_tip", "abc def", http.StatusBadRequest},
		{"branch_tip", "abc123def456", http.StatusOK},
	}

	slug := strings.NewReplacer("/", "_", " ", "_", "~", "_", "^", "_", ":", "_", "-", "dash-")

	for _, tc := range cases {
		t.Run(tc.field+"/"+slug.Replace(tc.value), func(t *testing.T) {
			// A fresh store per row: rows stay independent, and the accepted write in a
			// row cannot be masked by the row before it.
			h, s, _ := newTestTaskHandler(t)
			setupTaskValidationProject(t, h, s)

			rr := doTaskRequest(t, newTaskMux(h), "PUT",
				"/api/projects/task-val-proj/tasks/task-val-task",
				map[string]string{tc.field: tc.value})
			if rr.Code != tc.want {
				t.Fatalf("%s=%q: status = %d, want %d; body: %s", tc.field, tc.value, rr.Code, tc.want, rr.Body.String())
			}
			if tc.want != http.StatusOK {
				return
			}

			task, err := s.GetTask("task-val-proj", "task-val-task")
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			if got := stored[tc.field](task); got != tc.value {
				t.Errorf("%s = %q after the accepted update, want %q", tc.field, got, tc.value)
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
		Status:          types.StatusReview,
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
	if updated.Status != types.StatusDone {
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
	// A click that merged cleanly must leave no intent behind for a later turn to act on.
	if h.executor.CommitAndDonePending(task.ID) {
		t.Error("a clean commit-and-done left a click armed")
	}
}

// TestHandleCommitAndDone_ArmsClickForConflictResolution: when the click's merge conflicts, the turn
// that resolves it has to know the conflict came from this click, or it can only stop in Review.
func TestHandleCommitAndDone_ArmsClickForConflictResolution(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	h.commitQueue = exec.NewCommitQueue()

	proj, err := s.GetProject(projID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	target := strings.TrimSpace(gitOutput(t, proj.Path, "rev-parse", "--abbrev-ref", "HEAD"))

	// ExecutionState=running makes the handler re-arm and return without starting an executor, so no
	// LLM is involved.
	task := &types.Task{
		ID:              "arm-on-click",
		ProjectID:       projID,
		Title:           "Arm on click",
		Prompt:          "make changes",
		Status:          types.StatusInProgress,
		ExecutionState:  types.ExecStateRunning,
		Branch:          "task/arm-on-click",
		BaseRef:         target,
		CurrentThreadID: "thread-arm",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	wtPath, _, err := h.worktreeMgr.CreateWorktree(projID, task.ID, task.Branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Both sides change the same line: committing is fine, merging conflicts.
	if err := os.WriteFile(filepath.Join(wtPath, "shared.txt"), []byte("task change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wtPath, "add", "shared.txt")
	if err := os.WriteFile(filepath.Join(proj.Path, "shared.txt"), []byte("project change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, proj.Path, "add", "shared.txt")
	gitRun(t, proj.Path, "commit", "-m", "project change")

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
	if !updated.HasMergeConflict {
		t.Fatal("fixture: the merge should have conflicted")
	}
	if updated.Status == types.StatusDone {
		t.Fatal("a conflicted task must not be marked done")
	}
	if !h.executor.CommitAndDonePending(task.ID) {
		t.Error("the click that hit the conflict was not recorded for the resolution to finish")
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

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)

	projID := "proj-auto-start"
	proj := &types.Project{ID: projID, Name: "auto-start", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	worktreeMgr := git.NewWorktreeManager(s, cfgStore)
	boardMgr := stream.NewAppStreamManager(s, worktreeMgr)

	executor := exec.NewTaskExecutor(exec.Deps{
		Store:        s,
		ConfigStore:  cfgStore,
		AppStreamMgr: boardMgr,
		WorktreeMgr:  worktreeMgr,
	})
	h := NewTaskHandler(TaskDeps{Store: s, BoardStream: boardMgr, ConfigStore: cfgStore,
		Executor: executor, WorktreeMgr: worktreeMgr})
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
		Status:          types.StatusBacklog,
		ExecutionState:  types.ExecStateIdle,
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
		Status:          types.StatusBacklog,
		ExecutionState:  types.ExecStateIdle,
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
func createChainTask(t *testing.T, s *store.Store, projectID, id, title string, status types.Status, execState types.ExecutionState, parentID, threadID string, seed bool) {
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
	if parent.Status != types.StatusInProgress {
		t.Errorf("parent should move to In Progress (started-but-pending), got %q", parent.Status)
	}
	if parent.ExecutionState != types.ExecStateIdle {
		t.Errorf("parent should stay pending (not running), got %q", parent.ExecutionState)
	}

	child, _ := s.GetTaskByID("child-mip")
	if child.Status != types.StatusReview || child.ExecutionState != types.ExecStateIdle {
		t.Errorf("deepest subtask with nothing to run should wait in review: status=%q state=%q", child.Status, child.ExecutionState)
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
	if parent.Status != types.StatusReview || parent.ExecutionState != types.ExecStateIdle {
		t.Errorf("parent with nothing to run should wait in review once all children are done: status=%q state=%q", parent.Status, parent.ExecutionState)
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
	if parent.Status != types.StatusInProgress || parent.ExecutionState != types.ExecStateIdle {
		t.Errorf("parent should be started-but-pending: status=%q state=%q", parent.Status, parent.ExecutionState)
	}
	child, _ := s.GetTaskByID("child-blk")
	if child.Status != types.StatusReview {
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
	if mid.Status != types.StatusInProgress || mid.ExecutionState != types.ExecStateIdle {
		t.Errorf("mid subtask should be started-but-pending: status=%q state=%q", mid.Status, mid.ExecutionState)
	}
	leaf, _ := s.GetTaskByID("leaf-nsp")
	if leaf.Status != types.StatusReview || leaf.ExecutionState != types.ExecStateIdle {
		t.Errorf("deepest descendant with nothing to run should wait in review: status=%q state=%q", leaf.Status, leaf.ExecutionState)
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
	if mid.Status != types.StatusInProgress || mid.ExecutionState != types.ExecStateIdle {
		t.Errorf("mid subtask should be started-but-pending: status=%q state=%q", mid.Status, mid.ExecutionState)
	}
	leaf, _ := s.GetTaskByID("leaf-msc")
	if leaf.Status != types.StatusReview || leaf.ExecutionState != types.ExecStateIdle {
		t.Errorf("deepest descendant with nothing to run should wait in review: status=%q state=%q", leaf.Status, leaf.ExecutionState)
	}
	if !leaf.Autostarted {
		t.Error("deepest descendant started via its parent should be marked autostarted")
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
	if parent.Status != types.StatusReview || parent.ExecutionState != types.ExecStateIdle {
		t.Errorf("parent with nothing to run should wait in review once all children are done: status=%q state=%q", parent.Status, parent.ExecutionState)
	}
}

// TestHandleMove_AwaitingUserLeafReturnsToReview: a task sitting in review because the
// agent asked something, dragged into In Progress, has nothing to run — so the move must
// land it back in review, not park it as "Pending".
func TestHandleMove_AwaitingUserLeafReturnsToReview(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "leaf-await", "Leaf", "review", "idle", "", "thread-leaf-await", true)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/move", h.HandleMove)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/leaf-await/move", map[string]any{"status": "in_progress"})
	if rr.Code != http.StatusOK {
		t.Fatalf("HandleMove status = %d, body=%s", rr.Code, rr.Body.String())
	}

	leaf, _ := s.GetTaskByID("leaf-await")
	if leaf.Status != types.StatusReview || leaf.ExecutionState != types.ExecStateIdle {
		t.Errorf("task awaiting the user must return to review, not sit Pending in In Progress: status=%q state=%q", leaf.Status, leaf.ExecutionState)
	}
	// the column really changed, so the card lands on top of review
	if leaf.MovedAt.IsZero() {
		t.Errorf("moved_at did not advance — the row never changed column: moved_at=%v", leaf.MovedAt)
	}
	// the board moves the card from the response body
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "review" {
		t.Errorf("response status = %v, want review", body["status"])
	}
}

// TestHandleMove_AwaitingUserParentKeepsDoneChildren: same for a parent whose children are
// all Done — it lands in review, and its Done children stay Done.
func TestHandleMove_AwaitingUserParentKeepsDoneChildren(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "par-await", "Parent", "review", "idle", "", "thread-par-await", true)
	createChainTask(t, s, projID, "child-await-done1", "D1", "done", "completed", "par-await", "", false)
	createChainTask(t, s, projID, "child-await-done2", "D2", "done", "completed", "par-await", "", false)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/move", h.HandleMove)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/par-await/move", map[string]any{"status": "in_progress"})
	if rr.Code != http.StatusOK {
		t.Fatalf("HandleMove status = %d, body=%s", rr.Code, rr.Body.String())
	}

	parent, _ := s.GetTaskByID("par-await")
	if parent.Status != types.StatusReview || parent.ExecutionState != types.ExecStateIdle {
		t.Errorf("parent with nothing to run should wait in review: status=%q state=%q", parent.Status, parent.ExecutionState)
	}
	for _, id := range []string{"child-await-done1", "child-await-done2"} {
		child, err := s.GetTaskByID(id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if child.Status != types.StatusDone || child.ExecutionState != types.ExecStateCompleted {
			t.Errorf("%s must stay done/completed: status=%q state=%q", id, child.Status, child.ExecutionState)
		}
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
	if parent.Status != types.StatusInProgress || parent.ExecutionState != types.ExecStateIdle {
		t.Errorf("parent should stay started-but-pending: status=%q state=%q", parent.Status, parent.ExecutionState)
	}
	backlog, _ := s.GetTaskByID("child-gate-backlog")
	// it followed its parent out of Backlog, then waited in review (nothing to run)
	if backlog.Status != types.StatusReview {
		t.Errorf("backlog child should leave Backlog and wait in review (nothing to run): status=%q", backlog.Status)
	}
	review, _ := s.GetTaskByID("child-gate-review")
	if review.Status != types.StatusReview {
		t.Errorf("review child must not be dragged into In Progress: status=%q", review.Status)
	}
	done, _ := s.GetTaskByID("child-gate-done")
	if done.Status != types.StatusDone {
		t.Errorf("done child must not be dragged into In Progress: status=%q", done.Status)
	}
}

// TestHandleMove_PausedToBacklogRestsIdle: a task paused mid-turn and moved to Backlog
// leaves no turn behind — it rests like a row nobody started, the state a running task
// lands in when it is dragged out of In Progress.
func TestHandleMove_PausedToBacklogRestsIdle(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "move-paused", "Paused", "in_progress", "paused", "", "thread-move-paused", false)
	task, err := s.GetTaskByID("move-paused")
	if err != nil {
		t.Fatal(err)
	}
	task.Output = "Paused by user"
	if err := s.UpdateTask(task); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/move", h.HandleMove)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/move-paused/move", map[string]any{"status": "backlog"})
	if rr.Code != http.StatusOK {
		t.Fatalf("HandleMove status = %d, body=%s", rr.Code, rr.Body.String())
	}

	moved, err := s.GetTaskByID("move-paused")
	if err != nil {
		t.Fatalf("get moved task: %v", err)
	}
	if moved.Status != types.StatusBacklog || moved.ExecutionState != types.ExecStateIdle {
		t.Errorf("a paused row moved to Backlog must rest idle: status=%q state=%q", moved.Status, moved.ExecutionState)
	}
	if moved.Output != "" {
		t.Errorf("the pause text outlived the turn it described: output=%q", moved.Output)
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

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	wm := git.NewWorktreeManager(s, cfgStore)
	boardMgr := stream.NewAppStreamManager(s, wm)
	te := exec.NewTaskExecutor(exec.Deps{
		Store:        s,
		ConfigStore:  cfgStore,
		AppStreamMgr: boardMgr,
		WorktreeMgr:  wm,
	})
	h := NewTaskHandler(TaskDeps{Store: s, BoardStream: boardMgr, ConfigStore: cfgStore,
		Executor: te, WorktreeMgr: wm})
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
	task.Status = types.StatusInProgress
	task.ExecutionState = types.ExecStateRunning
	task.Branch = "task/" + taskID
	// Stands in for a task start, which may claim the in-flight state.
	if err := s.UpdateTaskClaimingExecution(task); err != nil {
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
	if task.Status != types.StatusInProgress || task.ExecutionState != types.ExecStateRunning || task.Branch != "task/"+taskID {
		t.Fatalf("title update clobbered task state: status=%s exec=%s branch=%q",
			task.Status, task.ExecutionState, task.Branch)
	}
}

// The peak is a reading the sandbox layer takes from a container as it is removed, so a created
// task has none — whatever the body claimed.
func TestTaskHandler_POST_DoesNotSeedAMeasuredPeak(t *testing.T) {
	h, s, worktreesDir := newTestTaskHandler(t)
	proj := &types.Project{ID: "peak-seed-proj", Name: "peak-seed", Path: filepath.Join(worktreesDir, "peak-seed")}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}

	rr := doTaskRequest(t, newTaskMux(h), http.MethodPost, "/api/projects/peak-seed-proj/tasks",
		map[string]any{"title": "seeded peak", "sandbox_memory_peak_mb": 99999})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", rr.Code, rr.Body.String())
	}
	var created types.Task
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("parse created task: %v", err)
	}

	stored, err := s.GetTask(proj.ID, created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.SandboxMemoryPeakMB != 0 {
		t.Errorf("created task carries peak %d MB, want 0", stored.SandboxMemoryPeakMB)
	}
}

// parentWithStagedWork builds the ordinary Review state: work staged but not committed.
func parentWithStagedWork(t *testing.T, h *TaskHandler, projID, repoDir, id string) (*types.Task, string) {
	t.Helper()
	base := strings.TrimSpace(gitOutput(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD"))
	task := &types.Task{
		ID: id, ProjectID: projID, Title: "parent " + id, Prompt: "work",
		Status: types.StatusReview, Branch: "task/" + id, BaseRef: base, CurrentThreadID: "thread-" + id,
	}
	if err := h.store.CreateTask(task); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	wtPath, _, err := h.worktreeMgr.CreateWorktree(projID, task.ID, task.Branch, "")
	if err != nil {
		t.Fatalf("parent worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("# Test\nthe parent's edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "parent_note.md"), []byte("written by the parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wtPath, "add", "README.md", "parent_note.md")
	return task, base
}

// subtaskMergedInto commits one file on a branch cut from the parent, and merges it.
func subtaskMergedInto(t *testing.T, h *TaskHandler, projID string, parent *types.Task, childID, file string) error {
	t.Helper()
	child := &types.Task{
		ID: childID, ProjectID: projID, Title: "child " + childID, Prompt: "work",
		Status: types.StatusReview, Branch: "task/" + childID, ParentTaskID: parent.ID, CurrentThreadID: "thread-" + childID,
	}
	if err := h.store.CreateTask(child); err != nil {
		t.Fatalf("create child: %v", err)
	}
	childWT, _, err := h.worktreeMgr.CreateWorktree(projID, child.ID, child.Branch, parent.Branch)
	if err != nil {
		t.Fatalf("child worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(childWT, file), []byte("written by the subtask\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, childWT, "add", file)
	gitRun(t, childWT, "commit", "-m", "subtask work")
	return h.executor.MergeBranch(projID, child.ID, child)
}

func postCommitAndDone(t *testing.T, h *TaskHandler, projID, taskID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/commit", h.HandleCommitAndDone)
	body, _ := json.Marshal(map[string]string{"commit_message": "the parent's commit"})
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projID+"/tasks/"+taskID+"/commit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// TestHandleCommitAndDone_CommitsTheParentsOwnWorkAfterASubtaskMerge: a restore that comes back
// unstaged commits part of the task and deletes the rest with its worktree.
func TestHandleCommitAndDone_CommitsTheParentsOwnWorkAfterASubtaskMerge(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	h.commitQueue = exec.NewCommitQueue()
	proj, err := s.GetProject(projID)
	if err != nil {
		t.Fatal(err)
	}

	parent, base := parentWithStagedWork(t, h, projID, proj.Path, "parent-merge-click")
	if err := subtaskMergedInto(t, h, projID, parent, "child-disjoint", "child_work.txt"); err != nil {
		t.Fatalf("subtask merge: %v", err)
	}

	rr := postCommitAndDone(t, h, projID, parent.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("commit-and-done status = %d, body=%s", rr.Code, rr.Body.String())
	}

	readme := gitOutput(t, proj.Path, "show", base+":README.md")
	if !strings.Contains(readme, "the parent's edit") {
		t.Errorf("the parent's own edit never reached %s; it reads %q", base, readme)
	}
	if out := gitOutput(t, proj.Path, "ls-tree", "--name-only", base); !strings.Contains(out, "child_work.txt") {
		t.Errorf("the subtask's work never reached %s: %q", base, out)
	}
}

// TestHandleCommitAndDone_HandsWorktreeConflictToTheResolver: the click finds the worktree
// conflicted by a subtask merge, so the model resolves it and the armed click completes.
func TestHandleCommitAndDone_HandsWorktreeConflictToTheResolver(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	h.commitQueue = exec.NewCommitQueue()
	proj, err := s.GetProject(projID)
	if err != nil {
		t.Fatal(err)
	}

	parent, _ := parentWithStagedWork(t, h, projID, proj.Path, "parent-clash-click")
	// The same file as the parent's uncommitted edit: the merge lands, the restore cannot.
	child := &types.Task{
		ID: "child-clash", ProjectID: projID, Title: "clashing child", Prompt: "work",
		Status: types.StatusReview, Branch: "task/child-clash", ParentTaskID: parent.ID, CurrentThreadID: "thread-clash",
	}
	if err := h.store.CreateTask(child); err != nil {
		t.Fatal(err)
	}
	childWT, _, err := h.worktreeMgr.CreateWorktree(projID, child.ID, child.Branch, parent.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childWT, "README.md"), []byte("# Test\nthe subtask's edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, childWT, "add", "README.md")
	gitRun(t, childWT, "commit", "-m", "subtask edit to the same file")
	if err := h.executor.MergeBranch(projID, child.ID, child); err == nil {
		t.Fatal("the fixture must leave the parent's worktree with a conflict to resolve")
	}

	rr := postCommitAndDone(t, h, projID, parent.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("commit-and-done status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	updated, err := s.GetTask(projID, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.HasMergeConflict {
		t.Error("the conflict must be flagged for the resolving turn")
	}
	if updated.Status == types.StatusDone {
		t.Error("a task whose work could not be committed must not be Done")
	}
	if !h.executor.CommitAndDonePending(parent.ID) {
		t.Error("the click must stay armed so the resolving turn completes it")
	}
	var handedToModel bool
	msgs, err := h.store.GetTaskMessages(parent.ID, parent.CurrentThreadID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Content, "conflict") {
			handedToModel = true
		}
	}
	if !handedToModel {
		t.Error("the conflict was never handed to the model")
	}

	wtPath, err := h.worktreeMgr.WorktreePath(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(filepath.Join(wtPath, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "the parent's edit") {
		t.Errorf("the parent's own edit was discarded instead of left to resolve: %q", onDisk)
	}
	onBranch := gitOutput(t, proj.Path, "show", parent.Branch+":README.md")
	if strings.Contains(onBranch, "<<<<<<<") {
		t.Errorf("conflict markers were committed onto the branch: %q", onBranch)
	}
}

// TestHandleCommitAndDone_ReportsACommitThatGitRefused: a refusal must end the request, not
// merge, mark the task Done and delete the worktree that holds the work.
func TestHandleCommitAndDone_ReportsACommitThatGitRefused(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	h.commitQueue = exec.NewCommitQueue()
	proj, err := s.GetProject(projID)
	if err != nil {
		t.Fatal(err)
	}

	parent, base := parentWithStagedWork(t, h, projID, proj.Path, "parent-locked")
	wtPath, err := h.worktreeMgr.WorktreePath(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Another git process holding this worktree's index: the ordinary way a commit is refused.
	wtName := filepath.Base(strings.TrimSpace(gitOutput(t, wtPath, "rev-parse", "--git-dir")))
	lock := filepath.Join(proj.Path, ".git", "worktrees", wtName, "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(lock) })

	rr := postCommitAndDone(t, h, projID, parent.ID)
	if rr.Code == http.StatusOK {
		t.Fatal("a refused commit answered OK: the task would be merged, marked Done and its worktree deleted")
	}
	updated, err := s.GetTask(projID, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status == types.StatusDone {
		t.Error("the task reached Done on a commit that never happened")
	}
	if out := gitOutput(t, proj.Path, "ls-tree", "--name-only", base); strings.Contains(out, "parent_note.md") {
		t.Error("the uncommitted work was merged to the target branch anyway")
	}
}

// TestHandleCommitAndDone_DoneWhenOnlyTheProjectOwnsTheConflict: the task's branch reached the target
// branch and the only thing that came back conflicted was the project checkout's own uncommitted edit,
// which the merge bracket had parked to run. The click asked for commit + merge + done and got all three,
// so the task finishes and the leftover conflict is reported by name. It used to travel back as a merge
// failure, which left a merged task in Review with nothing anywhere saying why.
func TestHandleCommitAndDone_DoneWhenOnlyTheProjectOwnsTheConflict(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	h.commitQueue = exec.NewCommitQueue()

	proj, err := s.GetProject(projID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	target := strings.TrimSpace(gitOutput(t, proj.Path, "rev-parse", "--abbrev-ref", "HEAD"))

	// One line both sides will reword, committed so the merge and the parked edit share a base.
	if err := os.WriteFile(filepath.Join(proj.Path, "shared.txt"), []byte("the original sentence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, proj.Path, "add", "shared.txt")
	gitRun(t, proj.Path, "commit", "-m", "shared line")

	task := &types.Task{
		ID:              "merged-anyway",
		ProjectID:       projID,
		Title:           "Merged anyway",
		Prompt:          "make changes",
		Status:          types.StatusReview,
		Branch:          "task/merged-anyway",
		BaseRef:         target,
		CurrentThreadID: "thread-merged-anyway",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	wtPath, _, err := h.worktreeMgr.CreateWorktree(projID, task.ID, task.Branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// The task rewords the line...
	if err := os.WriteFile(filepath.Join(wtPath, "shared.txt"), []byte("the task wording\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wtPath, "add", "shared.txt")
	// ...and so does the person, in the project checkout, uncommitted.
	if err := os.WriteFile(filepath.Join(proj.Path, "shared.txt"), []byte("the project's own wording\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/commit", h.HandleCommitAndDone)
	body, _ := json.Marshal(map[string]string{"commit_message": "task work"})
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
	if updated.Status != types.StatusDone {
		t.Fatalf("status = %q, want done — the work reached %s (output=%q)", updated.Status, target, updated.Output)
	}
	if updated.ExecutionState != types.ExecStateCompleted {
		t.Errorf("execution_state = %q, want completed", updated.ExecutionState)
	}
	if updated.HasMergeConflict {
		t.Error("the task is flagged for the conflict resolver: that path has the model edit and stage the project's own words")
	}
	if out := gitOutput(t, proj.Path, "show", "HEAD:shared.txt"); !strings.Contains(out, "the task wording") {
		t.Errorf("the merge did not deliver the task's work: HEAD:shared.txt = %q", out)
	}

	// What is left over is the project's working copy, and it has to be reported: the file, and where
	// the parked version still is.
	if status := gitOutput(t, proj.Path, "status", "--porcelain"); !strings.Contains(status, "UU shared.txt") {
		t.Errorf("the project checkout was not left holding the conflict: %q", status)
	}
	label := git.StashMessageForAutoMerge(task.ID)
	if out := gitOutput(t, proj.Path, "stash", "list"); !strings.Contains(out, label) {
		t.Errorf("the entry holding the project's own wording was dropped: %q", out)
	}
	msgs, err := s.GetTaskMessages(task.ID, task.CurrentThreadID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	var reported bool
	for _, m := range msgs {
		if strings.Contains(m.Content, "Merge failed") {
			t.Errorf("a merged task is told its merge failed: %q", m.Content)
		}
		if strings.Contains(m.Content, "shared.txt") && strings.Contains(m.Content, label) {
			reported = true
		}
	}
	if !reported {
		t.Error("nothing tells the user which file is left conflicted and which stash entry holds their version")
	}
}

// --- base_ref is a root task's own: nesting a task drops it ---

func TestTaskHandler_POST_DropsBaseBranchSentWithAParent(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	if err := s.CreateProject(&types.Project{ID: "bb-proj", Name: "bb-proj", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(&types.Task{
		ID: "bb-parent", ProjectID: "bb-proj", Title: "parent", Status: types.StatusBacklog,
	}); err != nil {
		t.Fatal(err)
	}
	mux := newTaskMux(h)

	rr := doTaskRequest(t, mux, http.MethodPost, "/api/projects/bb-proj/tasks", map[string]any{
		"prompt":         "Part of the plan",
		"parent_task_id": "bb-parent",
		"base_ref":       "test",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	var created types.Task
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ParentTaskID != "bb-parent" {
		t.Fatalf("parent_task_id = %q, want bb-parent", created.ParentTaskID)
	}
	stored, err := s.GetTask("bb-proj", created.ID)
	if err != nil {
		t.Fatalf("get created task: %v", err)
	}
	if stored.BaseRef != "" {
		t.Errorf("base_ref = %q on a subtask, want empty", stored.BaseRef)
	}
}

func TestTaskHandler_PUT_NestingClearsBaseBranch(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	if err := s.CreateTask(&types.Task{
		ID: "bb-put-parent", ProjectID: "task-val-proj", Title: "parent", Status: types.StatusBacklog,
	}); err != nil {
		t.Fatal(err)
	}
	mux := newTaskMux(h)

	if rr := doTaskRequest(t, mux, http.MethodPut,
		"/api/projects/task-val-proj/tasks/task-val-task", map[string]string{"base_ref": "test"}); rr.Code != http.StatusOK {
		t.Fatalf("configure base branch: status = %d: %s", rr.Code, rr.Body.String())
	}

	if rr := doTaskRequest(t, mux, http.MethodPut,
		"/api/projects/task-val-proj/tasks/task-val-task", map[string]string{"parent_task_id": "bb-put-parent"}); rr.Code != http.StatusOK {
		t.Fatalf("nest task: status = %d: %s", rr.Code, rr.Body.String())
	}

	task, err := s.GetTask("task-val-proj", "task-val-task")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.BaseRef != "" {
		t.Errorf("base_ref = %q after nesting, want empty", task.BaseRef)
	}
}
