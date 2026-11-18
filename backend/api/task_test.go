package api

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/backend/exec"
	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/testutil/testgit"
	"mike/backend/testutil/teststore"
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

	s := teststore.StoreIn(t, dbPath)

	s.SetConfigValue("providers", "[]")
	s.SetConfigValue("worker_provider", "")
	s.SetConfigValue("worker_model", "")
	s.SetConfigValue("utility_provider", "")
	s.SetConfigValue("utility_model", "")

	cfgStore := teststore.ConfigStore(t, s, dbPath)
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
// string. The accepted rows close the loop twice: the value reads back off the stored task,
// which is what separates "validated" from "silently dropped", and it comes back in the
// response body under its wire name, which is what the panel re-renders.
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
		{"base_ref", "-f", http.StatusBadRequest},
		{"base_ref", "-C/tmp/evil", http.StatusBadRequest},
		{"base_ref", "--dry-run", http.StatusBadRequest},
		{"base_ref", "feat~1", http.StatusBadRequest},
		{"base_ref", "feat..branch", http.StatusBadRequest},
		{"base_ref", "feat branch", http.StatusBadRequest},
		{"base_ref", "main", http.StatusOK},
		{"base_ref", "origin/feature-branch", http.StatusOK},

		// branch_tip: a commit, still validated for the same reason.
		{"branch_tip", "-f", http.StatusBadRequest},
		{"branch_tip", "-C/tmp/evil", http.StatusBadRequest},
		{"branch_tip", "--dry-run", http.StatusBadRequest},
		{"branch_tip", "abc~1", http.StatusBadRequest},
		{"branch_tip", "abc..def", http.StatusBadRequest},
		{"branch_tip", "abc def", http.StatusBadRequest},
		{"branch_tip", "abc123def456", http.StatusOK},
		{"branch_tip", "feature-branch", http.StatusOK},
		{"branch_tip", "task/frost-amber-quartz", http.StatusOK},
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
			// The body is what the panel re-renders, so the accepted value has to come back in it
			// under the field's wire name, not only in the row.
			var echoed map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &echoed); err != nil {
				t.Fatalf("decode the answer: %v\n%s", err, rr.Body.String())
			}
			if echoed[tc.field] != tc.value {
				t.Errorf("the response echoes %s = %v, want %q", tc.field, echoed[tc.field], tc.value)
			}
		})
	}
}

// --- Combined validation: multiple fields at once ---
// --- Every field of one body is validated before any of it is written ---

// TestTaskHandler_PUT_EveryFieldIsValidatedBeforeAnyIsWritten covers the batch shape the panel sends:
// the accepted rows prove three git-facing refs land together, and the refused row proves a body is
// all-or-nothing — the two valid refs beside an invalid one reach neither the response nor the row,
// so a card never ends up half-moved by a request that failed.
func TestTaskHandler_PUT_EveryFieldIsValidatedBeforeAnyIsWritten(t *testing.T) {
	for _, tt := range []struct {
		name   string
		fields map[string]string
		want   int
		// stored is what each named field must hold afterwards.
		stored map[string]string
	}{
		{
			name:   "three valid refs land together",
			fields: map[string]string{"branch": "feature/test", "base_ref": "main", "branch_tip": "abc123"},
			want:   http.StatusOK,
			stored: map[string]string{"branch": "feature/test", "base_ref": "main", "branch_tip": "abc123"},
		},
		{
			name:   "one invalid ref costs the whole body",
			fields: map[string]string{"branch": "feature/test", "base_ref": "main", "branch_tip": "-f"},
			want:   http.StatusBadRequest,
			// The task as setupTaskValidationProject wrote it: none of the body reached the row.
			stored: map[string]string{"branch": "main", "base_ref": "", "branch_tip": ""},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, s, _ := newTestTaskHandler(t)
			setupTaskValidationProject(t, h, s)

			rr := doTaskRequest(t, newTaskMux(h), "PUT",
				"/api/projects/task-val-proj/tasks/task-val-task", tt.fields)
			if rr.Code != tt.want {
				t.Fatalf("body %v: status = %d, want %d; body: %s", tt.fields, rr.Code, tt.want, rr.Body.String())
			}

			task, err := s.GetTask("task-val-proj", "task-val-task")
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			stored := map[string]string{
				"branch": task.Branch, "base_ref": task.BaseRef, "branch_tip": task.BranchTip,
			}
			for field, want := range tt.stored {
				if stored[field] != want {
					t.Errorf("%s = %q, want %q", field, stored[field], want)
				}
			}
		})
	}
}

// --- Existing tests (preserved below) ---
// TestWorktreeExistsReflectsTheWorktreeDirectory: the board's list and one task's own answer both
// report whether the task's worktree directory is on disk, from the same path, through two entry
// points. Both are asserted in both directions: a row that claims a worktree the editor cannot open
// is as broken as one that hides a worktree that is there.
func TestWorktreeExistsReflectsTheWorktreeDirectory(t *testing.T) {
	for _, tt := range []struct {
		name string
		fill func(tasks []types.Task, workspaceDir string)
	}{
		{"the board's list", func(tasks []types.Task, workspaceDir string) {
			EnrichWorktreeExists(tasks, workspaceDir)
		}},
		{"one task's answer", func(tasks []types.Task, workspaceDir string) {
			for i := range tasks {
				enrichTaskWorktree(&tasks[i], workspaceDir)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const projectID = "test-project"
			workspace := t.TempDir()
			// Only task-1 has a worktree: workspaceDir/projectID/taskID, the one layout
			// backend/git owns.
			if err := os.MkdirAll(filepath.Join(workspace, projectID, "task-1"), 0o755); err != nil {
				t.Fatal(err)
			}

			tasks := []types.Task{
				{ID: "task-1", ProjectID: projectID},
				{ID: "task-2", ProjectID: projectID},
			}
			tt.fill(tasks, workspace)

			if !tasks[0].WorktreeExists {
				t.Error("the task with a worktree directory reported worktree_exists=false")
			}
			if tasks[1].WorktreeExists {
				t.Error("the task with no worktree directory reported worktree_exists=true")
			}
		})
	}
}

// --- Auto-answer provenance (autostarted) ---
//
// Starting a parent task runs its subtasks autonomously (no human present to
// answer ask_question). The selected subtask must therefore be marked
// autostarted=true, so the auto-answer system answers its questions from parent
// context instead of stopping to wait for a user.

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
	testgit.Git(t, wtPath, "add", "staged.txt")
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
	if out := testgit.GitOutput(t, proj.Path, "log", "--all", "--oneline", "--", "scratch.txt"); strings.TrimSpace(out) != "" {
		t.Errorf("scratch.txt appears in project branch history: %s", out)
	}
	// And it must not show up as untracked content in the project status.
	if status := testgit.GitOutput(t, proj.Path, "status", "--porcelain"); strings.Contains(status, "scratch.txt") {
		t.Errorf("scratch.txt appears in project git status: %s", status)
	}
	// The staged change must still be committed and merged into the project.
	if out := testgit.GitOutput(t, proj.Path, "show", "HEAD:staged.txt"); !strings.Contains(out, "staged content") {
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
	target := strings.TrimSpace(testgit.GitOutput(t, proj.Path, "rev-parse", "--abbrev-ref", "HEAD"))

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
	testgit.Git(t, wtPath, "add", "shared.txt")
	if err := os.WriteFile(filepath.Join(proj.Path, "shared.txt"), []byte("project change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, proj.Path, "add", "shared.txt")
	testgit.Git(t, proj.Path, "commit", "-m", "project change")

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

	testgit.Git(t, repoDir, "init")
	testgit.Git(t, repoDir, "config", "user.email", "test@test.com")
	testgit.Git(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, repoDir, "add", "README.md")
	testgit.Git(t, repoDir, "commit", "-m", "Initial")

	dbPath := filepath.Join(tmpDir, "test.db")
	s := teststore.StoreIn(t, dbPath)

	s.SetConfigValue("providers", "[]")
	s.SetConfigValue("worker_provider", "")
	s.SetConfigValue("worker_model", "")
	s.SetConfigValue("utility_provider", "")
	s.SetConfigValue("utility_model", "")

	cfgStore := teststore.ConfigStore(t, s, dbPath)
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
		if err := json.MarshalWrite(&buf, body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// --- One schedule decision, whichever route the click arrived through ---

// postClick sends the click that asks a row to run. `route` is the path suffix and the handler the
// server registers for it; a drag to In Progress is a move with a status, the two start routes take
// no body.
func postClick(t *testing.T, h *TaskHandler, projID, route, taskID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	var body any
	switch route {
	case "start":
		mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/start", h.HandleStart)
	case "start-subtask":
		mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/start-subtask", h.HandleStartSubtask)
	case "move":
		mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/move", h.HandleMove)
		body = map[string]any{"status": "in_progress"}
	default:
		t.Fatalf("unknown click route %q", route)
	}
	return postToMux(t, mux, "/api/projects/"+projID+"/tasks/"+taskID+"/"+route, body)
}

// TestClickStartsTheDeepestReadyDescendant: starting a row, starting a subtask and dragging a row to
// In Progress all ask startUnderSchedule the same question, so the answer is asserted once per entry
// point: the clicked row goes to In Progress and waits, and the deepest descendant with nothing left
// to run below it is the one that runs. It is marked autostarted because nobody clicked it — that is
// what makes the auto-answer system answer its questions from the parent's context.
func TestClickStartsTheDeepestReadyDescendant(t *testing.T) {
	type chainRow struct {
		id     string
		parent string
	}
	for _, tt := range []struct {
		name  string
		route string
		chain []chainRow
		click string
	}{
		{
			name:  "start on a parent starts its child",
			route: "start",
			chain: []chainRow{{id: "par-drd", parent: ""}, {id: "child-drd", parent: "par-drd"}},
			click: "par-drd",
		},
		{
			name:  "start-subtask on a middle row starts the leaf",
			route: "start-subtask",
			chain: []chainRow{{id: "root-drd2", parent: ""}, {id: "mid-drd2", parent: "root-drd2"}, {id: "leaf-drd2", parent: "mid-drd2"}},
			click: "mid-drd2",
		},
		{
			name:  "dragging a middle row to In Progress starts the leaf",
			route: "move",
			chain: []chainRow{{id: "root-drd3", parent: ""}, {id: "mid-drd3", parent: "root-drd3"}, {id: "leaf-drd3", parent: "mid-drd3"}},
			click: "mid-drd3",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, s, projID := newTaskHandlerWithExecutor(t)
			deepest := tt.chain[len(tt.chain)-1].id
			for _, row := range tt.chain {
				// The deepest row is the one with a conversation, so its start gets as far as
				// deciding there is nothing left to run and waits in review without an LLM.
				createChainTask(t, s, projID, row.id, row.id, types.StatusBacklog, types.ExecStateIdle,
					row.parent, "thread-"+row.id, row.id == deepest)
			}

			rr := postClick(t, h, projID, tt.route, tt.click)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s status = %d, body=%s", tt.route, rr.Code, rr.Body.String())
			}

			clicked, err := s.GetTaskByID(tt.click)
			if err != nil {
				t.Fatal(err)
			}
			if clicked.Status != types.StatusInProgress || clicked.ExecutionState != types.ExecStateIdle {
				t.Errorf("the clicked row should be started-but-pending: status=%q state=%q", clicked.Status, clicked.ExecutionState)
			}

			leaf, err := s.GetTaskByID(deepest)
			if err != nil {
				t.Fatal(err)
			}
			if leaf.Status != types.StatusReview || leaf.ExecutionState != types.ExecStateIdle {
				t.Errorf("the deepest descendant with nothing to run should wait in review: status=%q state=%q", leaf.Status, leaf.ExecutionState)
			}
			if !leaf.Autostarted {
				t.Error("a row the scheduler started should be marked autostarted, whatever route the click came through")
			}
		})
	}
}

// TestClickRunsItsOwnTurnWhenNothingUnderItIsLeft: once every descendant is Done the row has work of
// its own to do, so its turn starts — and a row the person clicked is never marked autostarted,
// because a human is present to answer what it asks.
func TestClickRunsItsOwnTurnWhenNothingUnderItIsLeft(t *testing.T) {
	for _, route := range []string{"start", "move"} {
		t.Run(route, func(t *testing.T) {
			h, s, projID := newTaskHandlerWithExecutor(t)
			createChainTask(t, s, projID, "par-aod", "Parent", types.StatusBacklog, types.ExecStateIdle, "", "thread-par-aod", true)
			createChainTask(t, s, projID, "child-aod", "Child", types.StatusDone, types.ExecStateCompleted, "par-aod", "", false)

			rr := postClick(t, h, projID, route, "par-aod")
			if rr.Code != http.StatusOK {
				t.Fatalf("%s status = %d, body=%s", route, rr.Code, rr.Body.String())
			}

			parent, err := s.GetTaskByID("par-aod")
			if err != nil {
				t.Fatal(err)
			}
			if parent.Status != types.StatusReview || parent.ExecutionState != types.ExecStateIdle {
				t.Errorf("a parent with nothing left to run should wait in review: status=%q state=%q", parent.Status, parent.ExecutionState)
			}
			if parent.Autostarted {
				t.Error("a row the person clicked is not one the scheduler started")
			}
		})
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

// createChildWithoutModelConfig writes a child the way plan-mode decomposition writes it: no model
// config of its own, and a thread whose last word is the assistant's, so a start that gets as far as
// preparing the run ends in review without reaching an LLM.
func createChildWithoutModelConfig(t *testing.T, s *store.Store, projectID, id, parentID string) {
	t.Helper()
	createChainTask(t, s, projectID, id, id, "backlog", "idle", parentID, "thread-"+id, true)
	child, err := s.GetTaskByID(id)
	if err != nil {
		t.Fatal(err)
	}
	child.LlamaServerURL = ""
	child.ModelName = ""
	if err := s.UpdateTask(child); err != nil {
		t.Fatal(err)
	}
}

// requireStartedOnParentsConfig: a click that starts a child reaches the scheduler, and a child with no
// model config runs on its parent's. exec's TestStartSubtask_InheritsModelConfig pins the inheritance.
func requireStartedOnParentsConfig(t *testing.T, route string, rr *httptest.ResponseRecorder, s *store.Store, id string) {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("%s status = %d, body=%s", route, rr.Code, rr.Body.String())
	}
	child, err := s.GetTaskByID(id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	if child.LlamaServerURL != "http://localhost:8080" || child.ModelName != "test-model" {
		t.Errorf("start left the child without the parent's model config: server=%q model=%q",
			child.LlamaServerURL, child.ModelName)
	}
	if child.Status != types.StatusReview {
		t.Errorf("started child with nothing left to run should wait in review: status=%q", child.Status)
	}
}

func TestHandleMove_ChildWithoutModelConfigRunsOnTheParents(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "root-cfg-mv", "Root", "backlog", "idle", "", "", false)
	createChildWithoutModelConfig(t, s, projID, "child-cfg-mv", "root-cfg-mv")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/move", h.HandleMove)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/child-cfg-mv/move", map[string]any{"status": "in_progress"})

	requireStartedOnParentsConfig(t, "HandleMove", rr, s, "child-cfg-mv")
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
	s := teststore.StoreIn(t, dbPath)

	mock := newHeldTitleLLM()
	server := mock.Server()
	defer server.Close()

	providers := `[{"name":"held","display_name":"Held","url":"` + server.URL + `","models":[{"name":"m1","reasoning_effort":"off"}],"concurrency":1}]`
	s.SetConfigValue("providers", providers)
	s.SetConfigValue("worker_provider", "")
	s.SetConfigValue("worker_model", "")
	s.SetConfigValue("utility_provider", "held")
	s.SetConfigValue("utility_model", "m1")

	cfgStore := teststore.ConfigStore(t, s, dbPath)
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
	if !task.TitleLLM {
		t.Errorf("the generated title reached the row without its mark")
	}
	if task.Status != types.StatusInProgress || task.ExecutionState != types.ExecStateRunning || task.Branch != "task/"+taskID {
		t.Fatalf("title update clobbered task state: status=%s exec=%s branch=%q",
			task.Status, task.ExecutionState, task.Branch)
	}
}

// --- A created row takes no field the product owns ---

// TestTaskHandler_POST_CannotClaimAFieldTheProductOwns: the model's mark on a title is set by the
// title generator, and the memory peak is a reading the sandbox layer takes from a container as it is
// removed. Neither is a caller's to send, so a created row carries neither however the body spells
// them — a body that could claim them could make a typed title read as the model's, or a task that
// never ran read as one that filled its container.
func TestTaskHandler_POST_CannotClaimAFieldTheProductOwns(t *testing.T) {
	for _, tt := range []struct {
		name string
		body map[string]any
		// check reads the stored row and fails on anything the body claimed.
		check func(*testing.T, *types.Task)
	}{
		{
			name: "the model's mark on a title",
			body: map[string]any{"title": "typed by hand", "prompt": "typed by hand", "title_llm": true},
			check: func(t *testing.T, stored *types.Task) {
				if stored.TitleLLM {
					t.Errorf("a created row took the model's mark from the body")
				}
			},
		},
		{
			name: "a measured memory peak",
			body: map[string]any{"title": "seeded peak", "sandbox_memory_peak_mb": 99999},
			check: func(t *testing.T, stored *types.Task) {
				if stored.SandboxMemoryPeakMB != 0 {
					t.Errorf("created task carries peak %d MB, want 0", stored.SandboxMemoryPeakMB)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, s, worktreesDir := newTestTaskHandler(t)
			proj := &types.Project{ID: "claim-proj", Name: "claim", Path: filepath.Join(worktreesDir, "claim")}
			if err := s.CreateProject(proj); err != nil {
				t.Fatal(err)
			}

			rr := doTaskRequest(t, newTaskMux(h), http.MethodPost, "/api/projects/claim-proj/tasks", tt.body)
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
			tt.check(t, stored)
		})
	}
}

// parentWithStagedWork builds the ordinary Review state: work staged but not committed.
func parentWithStagedWork(t *testing.T, h *TaskHandler, projID, repoDir, id string) (*types.Task, string) {
	t.Helper()
	base := strings.TrimSpace(testgit.GitOutput(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD"))
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
	testgit.Git(t, wtPath, "add", "README.md", "parent_note.md")
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
	testgit.Git(t, childWT, "add", file)
	testgit.Git(t, childWT, "commit", "-m", "subtask work")
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

	readme := testgit.GitOutput(t, proj.Path, "show", base+":README.md")
	if !strings.Contains(readme, "the parent's edit") {
		t.Errorf("the parent's own edit never reached %s; it reads %q", base, readme)
	}
	if out := testgit.GitOutput(t, proj.Path, "ls-tree", "--name-only", base); !strings.Contains(out, "child_work.txt") {
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
	testgit.Git(t, childWT, "add", "README.md")
	testgit.Git(t, childWT, "commit", "-m", "subtask edit to the same file")
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
	onBranch := testgit.GitOutput(t, proj.Path, "show", parent.Branch+":README.md")
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
	wtName := filepath.Base(strings.TrimSpace(testgit.GitOutput(t, wtPath, "rev-parse", "--git-dir")))
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
	if out := testgit.GitOutput(t, proj.Path, "ls-tree", "--name-only", base); strings.Contains(out, "parent_note.md") {
		t.Error("the uncommitted work was merged to the target branch anyway")
	}
}

// TestHandleCommitAndDone_DoneWhenOnlyTheProjectOwnsTheConflict: the task's branch reached the target
// branch and the only thing that came back conflicted was the project checkout's own uncommitted edit,
// which the merge bracket had parked to run. The click asked for commit + merge + done and got all
// three, so the task finishes and the leftover conflict is reported by name.
func TestHandleCommitAndDone_DoneWhenOnlyTheProjectOwnsTheConflict(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	h.commitQueue = exec.NewCommitQueue()

	proj, err := s.GetProject(projID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	target := strings.TrimSpace(testgit.GitOutput(t, proj.Path, "rev-parse", "--abbrev-ref", "HEAD"))

	// One line both sides will reword, committed so the merge and the parked edit share a base.
	if err := os.WriteFile(filepath.Join(proj.Path, "shared.txt"), []byte("the original sentence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, proj.Path, "add", "shared.txt")
	testgit.Git(t, proj.Path, "commit", "-m", "shared line")

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
	testgit.Git(t, wtPath, "add", "shared.txt")
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
	if out := testgit.GitOutput(t, proj.Path, "show", "HEAD:shared.txt"); !strings.Contains(out, "the task wording") {
		t.Errorf("the merge did not deliver the task's work: HEAD:shared.txt = %q", out)
	}

	// What is left over is the project's working copy, and it has to be reported: the file, and where
	// the parked version still is.
	if status := testgit.GitOutput(t, proj.Path, "status", "--porcelain"); !strings.Contains(status, "UU shared.txt") {
		t.Errorf("the project checkout was not left holding the conflict: %q", status)
	}
	label := git.StashMessageForAutoMerge(task.ID)
	if out := testgit.GitOutput(t, proj.Path, "stash", "list"); !strings.Contains(out, label) {
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
// --- base_ref is a root task's own: nesting a task drops it ---

// TestNestingATaskDropsItsBaseBranch: a subtask's baseline is its parent's branch, decided when it
// runs, so a base_ref on a nested task is a baseline that no longer exists. Both ways a task becomes
// nested — created under a parent, and re-parented by an update — clear the field.
func TestNestingATaskDropsItsBaseBranch(t *testing.T) {
	for _, tt := range []struct {
		name string
		// nest makes the row a subtask and returns how it was nested, for the failure message.
		nest func(t *testing.T, h *TaskHandler, s *store.Store, parentID string) string
	}{
		{
			name: "created under a parent",
			nest: func(t *testing.T, h *TaskHandler, s *store.Store, parentID string) string {
				rr := doTaskRequest(t, newTaskMux(h), http.MethodPost, "/api/projects/task-val-proj/tasks",
					map[string]any{"prompt": "Part of the plan", "parent_task_id": parentID, "base_ref": "test"})
				if rr.Code != http.StatusCreated {
					t.Fatalf("create: status = %d, want 201: %s", rr.Code, rr.Body.String())
				}
				var created types.Task
				if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
					t.Fatal(err)
				}
				if created.ParentTaskID != parentID {
					t.Fatalf("parent_task_id = %q, want %q", created.ParentTaskID, parentID)
				}
				return created.ID
			},
		},
		{
			name: "re-parented by an update",
			nest: func(t *testing.T, h *TaskHandler, s *store.Store, parentID string) string {
				mux := newTaskMux(h)
				const path = "/api/projects/task-val-proj/tasks/task-val-task"
				if rr := doTaskRequest(t, mux, http.MethodPut, path,
					map[string]string{"base_ref": "test"}); rr.Code != http.StatusOK {
					t.Fatalf("configure base branch: status = %d: %s", rr.Code, rr.Body.String())
				}
				if rr := doTaskRequest(t, mux, http.MethodPut, path,
					map[string]string{"parent_task_id": parentID}); rr.Code != http.StatusOK {
					t.Fatalf("nest task: status = %d: %s", rr.Code, rr.Body.String())
				}
				return "task-val-task"
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, s, _ := newTestTaskHandler(t)
			setupTaskValidationProject(t, h, s)
			if err := s.CreateTask(&types.Task{
				ID: "nest-parent", ProjectID: "task-val-proj", Title: "parent", Status: types.StatusBacklog,
			}); err != nil {
				t.Fatalf("create parent: %v", err)
			}

			id := tt.nest(t, h, s, "nest-parent")

			stored, err := s.GetTask("task-val-proj", id)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			if stored.BaseRef != "" {
				t.Errorf("base_ref = %q on a nested task, want empty", stored.BaseRef)
			}
		})
	}
}

// TestGitAuthorOfReadsTheProjectsOwnRepository: the config read is scoped to the repository at this
// row's Path. The layering of the three sources is held by
// TestProjectAuthorIdentityAnswersTheProjectFirst.
func TestGitAuthorOfReadsTheProjectsOwnRepository(t *testing.T) {
	origName, origEmail := git.AuthorIdentity()
	defer git.SetAuthorIdentity(origName, origEmail)
	// An empty HOME keeps this person's own global git config out of the answer.
	t.Setenv("HOME", t.TempDir())

	mine := testgit.Repo(t)
	testgit.Git(t, mine, "config", "user.name", "Repo Person")
	testgit.Git(t, mine, "config", "user.email", "repo@example.com")
	theirs := testgit.Repo(t)
	testgit.Git(t, theirs, "config", "user.name", "Someone Else")
	testgit.Git(t, theirs, "config", "user.email", "other@example.com")

	git.SetAuthorIdentity("Boot Person", "boot@example.com")
	if name, email := gitAuthorOf(&types.Project{ID: "p1", Path: mine, GitAuthorName: "Ada Lovelace"}); name != "Ada Lovelace" || email != "boot@example.com" {
		t.Errorf("the row's half: got %q <%q>, want the row's name over the stamped email", name, email)
	}

	// Unstamped, each half the row does not name comes from its own repository.
	git.SetAuthorIdentity("", "")
	if name, email := gitAuthorOf(&types.Project{ID: "p1", Path: mine, GitAuthorEmail: "ada@example.com"}); name != "Repo Person" || email != "ada@example.com" {
		t.Errorf("an unnamed half: got %q <%q>, want this project's repository to name the person", name, email)
	}
	if name, email := gitAuthorOf(&types.Project{ID: "p2", Path: theirs}); name != "Someone Else" || email != "other@example.com" {
		t.Errorf("a second project: got %q <%q>, want its own repository's identity, not the first project's", name, email)
	}

	// Nothing names an identity anywhere: the pair is empty.
	if name, email := gitAuthorOf(&types.Project{ID: "p3", Path: testgit.EmptyRepo(t)}); name != "" || email != "" {
		t.Errorf("nothing configured: got %q <%q>, want the empty pair", name, email)
	}
}
