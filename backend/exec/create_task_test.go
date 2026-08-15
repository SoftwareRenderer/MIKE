package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"
)

// testStore creates a new Store with a temporary database for testing.
func testStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newTestExecutor creates a TaskExecutor with a test store for testing create_task.
func newTestExecutor(t *testing.T) *TaskExecutor {
	t.Helper()
	s := testStore(t)
	appStreamMgr := stream.NewAppStreamManager(s, nil)
	cs := config.NewConfigStore(s, "", "test")
	cs.SetWorkspaceDirOverride(t.TempDir())
	_ = s.SetConfigValue("providers", "[]")
	_ = s.SetConfigValue("worker_provider", "")
	_ = s.SetConfigValue("worker_model", "")
	_ = s.SetConfigValue("utility_provider", "")
	_ = s.SetConfigValue("utility_model", "")
	return &TaskExecutor{
		store:        s,
		appStreamMgr: appStreamMgr,
		configStore:  cs,
		tasks:        make(map[string]*executionState),
	}
}

// createProject creates a project in the store for testing.
func createProject(t *testing.T, s *store.Store, id, name string) *types.Project {
	t.Helper()
	now := time.Now().UTC()
	p := &types.Project{
		ID:        id,
		Name:      name,
		Path:      t.TempDir(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateProject(p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return p
}

// createParentTask creates a parent task in the store for testing.
func createParentTask(t *testing.T, s *store.Store, id, projectID string, status string) *types.Task {
	t.Helper()
	// Ensure project exists
	createProject(t, s, projectID, projectID)
	now := time.Now().UTC()
	task := &types.Task{
		ID:             id,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         status,
		ExecutionState: "idle",
		LlamaServerURL: "http://test:8080",
		ModelName:      "test-model",
		AutoCommit:     true,
		OperatingMode:  "plan",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create parent task: %v", err)
	}
	return task
}

func TestHandleCreateTask_Basic(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-1", "proj-1", "backlog")

	result, err := te.handleCreateTask(map[string]any{
		"title":          "Child Task",
		"prompt":         "Implement the child feature",
		"parent_task_id": "parent-task-1",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return JSON with task_id
	var resp map[string]string
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("expected JSON response, got: %s", result)
	}
	if resp["task_id"] == "" {
		t.Fatalf("expected task_id in response, got: %s", result)
	}
	if resp["title"] != "Child Task" {
		t.Errorf("expected title 'Child Task', got %q", resp["title"])
	}

	// Verify task was created in store
	childID := resp["task_id"]
	child, err := te.store.GetTaskByTaskID(childID)
	if err != nil {
		t.Fatalf("get created task: %v", err)
	}
	if child.Title != "Child Task" {
		t.Errorf("expected title 'Child Task', got %q", child.Title)
	}
	if child.Prompt != "Implement the child feature" {
		t.Errorf("expected prompt 'Implement the child feature', got %q", child.Prompt)
	}
	if child.ParentTaskID != "parent-task-1" {
		t.Errorf("expected parent_task_id 'parent-task-1', got %q", child.ParentTaskID)
	}
	if child.ProjectID != "proj-1" {
		t.Errorf("expected project_id 'proj-1', got %q", child.ProjectID)
	}
	if child.Status != "backlog" {
		t.Errorf("expected status 'backlog', got %q", child.Status)
	}
}

func TestHandleCreateTask_InheritsSettings(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-2", "proj-1", "backlog")

	_, err := te.handleCreateTask(map[string]any{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "parent-task-2",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Get the child task
	children, _ := te.store.GetChildren("parent-task-2")
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}
	child := children[0]

	// Verify inherited settings
	if child.LlamaServerURL != parent.LlamaServerURL {
		t.Errorf("expected inherited llama_server_url %q, got %q", parent.LlamaServerURL, child.LlamaServerURL)
	}
	if child.ModelName != parent.ModelName {
		t.Errorf("expected inherited model_name %q, got %q", parent.ModelName, child.ModelName)
	}
	if child.AutoCommit != parent.AutoCommit {
		t.Errorf("expected inherited auto_commit %v, got %v", parent.AutoCommit, child.AutoCommit)
	}
	if child.IsPlanMode() {
		t.Errorf("expected operating_mode 'act' for subtask, got %q", child.OperatingMode)
	}
}

func TestHandleCreateTask_NestedSubtask(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-3", "proj-1", "backlog")

	// Create first child under parent
	result1, err := te.handleCreateTask(map[string]any{
		"title":          "Level 1 Task",
		"prompt":         "Level 1 prompt",
		"parent_task_id": "parent-task-3",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp1 map[string]string
	json.Unmarshal([]byte(result1), &resp1)
	level1ID := resp1["task_id"]

	// Ensure parent can accept nested subtasks (parent must still be in backlog)
	// Create a second child under the first child
	result2, err := te.handleCreateTask(map[string]any{
		"title":          "Level 2 Task",
		"prompt":         "Level 2 prompt",
		"parent_task_id": level1ID,
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp2 map[string]string
	json.Unmarshal([]byte(result2), &resp2)
	level2ID := resp2["task_id"]

	// Verify nesting
	level2, err := te.store.GetTaskByTaskID(level2ID)
	if err != nil {
		t.Fatalf("get level 2 task: %v", err)
	}
	if level2.ParentTaskID != level1ID {
		t.Errorf("expected level 2 parent_task_id %q, got %q", level1ID, level2.ParentTaskID)
	}
	if level2.Title != "Level 2 Task" {
		t.Errorf("expected title 'Level 2 Task', got %q", level2.Title)
	}
}

func TestHandleCreateTask_CreatesThread(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-4", "proj-1", "backlog")

	result, err := te.handleCreateTask(map[string]any{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "parent-task-4",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp map[string]string
	json.Unmarshal([]byte(result), &resp)
	childID := resp["task_id"]

	// Verify thread was created
	threads, err := te.store.GetThreadsByTask(childID)
	if err != nil {
		t.Fatalf("get threads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected 1 thread, got %d", len(threads))
	}
	if threads[0].Name != "Main" {
		t.Errorf("expected thread name 'Main', got %q", threads[0].Name)
	}
	if !threads[0].IsActive {
		t.Error("expected thread to be active")
	}
}

func TestHandleCreateTask_CreatesPromptMessage(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-5", "proj-1", "backlog")

	result, err := te.handleCreateTask(map[string]any{
		"title":          "Child Task",
		"prompt":         "Implement feature X",
		"parent_task_id": "parent-task-5",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp map[string]string
	json.Unmarshal([]byte(result), &resp)
	childID := resp["task_id"]

	// Verify prompt message was created
	task, _ := te.store.GetTaskByTaskID(childID)
	messages, err := te.store.GetTaskMessages(childID, task.CurrentThreadID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", messages[0].Role)
	}
	if messages[0].Content != "Implement feature X" {
		t.Errorf("expected content 'Implement feature X', got %q", messages[0].Content)
	}
}

func TestHandleCreateTask_MissingTitle(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-6", "proj-1", "backlog")

	_, err := te.handleCreateTask(map[string]any{
		"prompt": "Test prompt",
	}, parent, nil)

	if err == nil {
		t.Error("expected error for missing title, got nil")
	}
	if !strings.Contains(err.Error(), "title and prompt are required") {
		t.Errorf("unexpected error message: %s", err)
	}
}

func TestHandleCreateTask_MissingPrompt(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-7", "proj-1", "backlog")

	_, err := te.handleCreateTask(map[string]any{
		"title": "Task without prompt",
	}, parent, nil)

	if err == nil {
		t.Error("expected error for missing prompt, got nil")
	}
	if !strings.Contains(err.Error(), "title and prompt are required") {
		t.Errorf("unexpected error message: %s", err)
	}
}

func TestHandleCreateTask_ParentNotFound(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-8", "proj-1", "backlog")

	_, err := te.handleCreateTask(map[string]any{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "nonexistent-task",
	}, parent, nil)

	if err == nil {
		t.Error("expected error for parent not found, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error message: %s", err)
	}
}

func TestHandleCreateTask_ParentNotInBacklog(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-9", "proj-1", "done")

	_, err := te.handleCreateTask(map[string]any{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "parent-task-9",
	}, parent, nil)

	if err == nil {
		t.Error("expected error for parent not in backlog, got nil")
	}
	if !strings.Contains(err.Error(), "not in backlog") {
		t.Errorf("unexpected error message: %s", err)
	}
}

func TestHandleCreateTask_AllowsInProgressParent(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-10", "proj-1", "in_progress")

	result, err := te.handleCreateTask(map[string]any{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "parent-task-10",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should succeed (in_progress parents can accept subtasks during planning)
	var resp map[string]string
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("expected JSON response, got: %s", result)
	}
	if resp["task_id"] == "" {
		t.Fatalf("expected task_id in response, got: %s", result)
	}
}

func TestHandleCreateTask_MultipleSubtasks(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-11", "proj-1", "backlog")

	// Create multiple subtasks under parent
	for i := 1; i <= 3; i++ {
		result, err := te.handleCreateTask(map[string]any{
			"title":          "Task " + string(rune('A'+i-1)),
			"prompt":         "Prompt for task " + string(rune('A'+i-1)),
			"parent_task_id": "parent-task-11",
		}, parent, nil)
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}

		var resp map[string]string
		if err := json.Unmarshal([]byte(result), &resp); err != nil {
			t.Fatalf("iteration %d: expected JSON response, got: %s", i, result)
		}
		if resp["task_id"] == "" {
			t.Fatalf("iteration %d: expected task_id in response", i)
		}
	}

	// Verify all 3 children exist
	children, err := te.store.GetChildren("parent-task-11")
	if err != nil {
		t.Fatalf("get children: %v", err)
	}
	if len(children) != 3 {
		t.Errorf("expected 3 children, got %d", len(children))
	}
}

func TestHandleCreateTask_TitleWithSpecialChars(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-12", "proj-1", "backlog")

	result, err := te.handleCreateTask(map[string]any{
		"title":          `Task with "quotes" and \backslash`,
		"prompt":         "Test prompt",
		"parent_task_id": "parent-task-12",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return valid JSON even with special characters
	var resp map[string]string
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("expected valid JSON with special chars, got: %s (error: %v)", result, err)
	}
	if resp["title"] != `Task with "quotes" and \backslash` {
		t.Errorf("expected title to be preserved, got %q", resp["title"])
	}
}

func TestTaskComplete_PlanMode_RequiresSubtasks(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "plan-task-1", "proj-1", "backlog")

	// Try to complete without subtasks (empty state)
	state := &executionState{createdIDs: nil}
	result, ok := te.executeTool(context.Background(), "request_review", map[string]any{
		"summary": "Plan complete",
	}, "", parent, state, nil)

	if ok {
		t.Error("expected request_review to fail in plan mode without subtasks")
	}
	if !strings.Contains(result, "Cannot complete planning without creating") {
		t.Errorf("unexpected error message: %s", result)
	}
}

func TestTaskComplete_PlanMode_AllowsWithSubtasks(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "plan-parent-1", "proj-1", "backlog")

	// Create a subtask via handleCreateTask
	state := &executionState{}
	result, err := te.handleCreateTask(map[string]any{
		"title":          "Child Task",
		"prompt":         "Implement the child feature",
		"parent_task_id": "plan-parent-1",
	}, parent, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify subtask was created
	var resp map[string]string
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("failed to parse create_task result: %v", err)
	}
	if resp["task_id"] == "" {
		t.Fatalf("expected task_id in result, got: %s", result)
	}

	// Reload parent
	parent, _ = te.store.GetTask("proj-1", "plan-parent-1")

	// Try to complete with subtask
	result2, ok := te.executeTool(context.Background(), "request_review", map[string]any{
		"summary": "Plan complete",
	}, "", parent, state, nil)

	if !ok {
		t.Errorf("expected request_review to succeed with subtasks, got: %s", result2)
	}
}

// seedCreateTaskHistory persists a create_task tool_call message (and its
// result) for a task, simulating a create_task that ran in an earlier
// execution. The referenced child task is created in the store so the plan
// summary count can find it.
func seedCreateTaskHistory(t *testing.T, s *store.Store, planTaskID, threadID, callID, childID, title string, success bool) {
	t.Helper()
	now := time.Now().UTC()
	child := &types.Task{
		ID:             childID,
		ProjectID:      "proj-1",
		Title:          title,
		Prompt:         "Seeded child prompt",
		Status:         "backlog",
		ExecutionState: "idle",
		ParentTaskID:   planTaskID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.CreateTask(child); err != nil {
		t.Fatalf("create seeded child: %v", err)
	}
	output, _ := json.Marshal(map[string]string{"task_id": childID, "title": title})
	result, _ := json.Marshal(map[string]any{
		"output":   string(output),
		"success":  success,
		"duration": 1,
	})
	tc, _ := json.Marshal(map[string]any{
		"tool_id":           callID,
		"tool_name":         "create_task",
		"tool_display_name": "Create Task",
		"args":              map[string]any{"title": title, "prompt": "Seeded child prompt"},
		"raw_args":          fmt.Sprintf(`{"title":%q,"prompt":"Seeded child prompt"}`, title),
		"sandbox":           false,
	})
	msg := &types.TaskMessage{
		ID:         "seeded-" + callID,
		TaskID:     planTaskID,
		ThreadID:   threadID,
		Role:       "tool_call",
		ToolCalls:  string(tc),
		ToolResult: string(result),
		CreatedAt:  now,
	}
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("seed tool_call message: %v", err)
	}
}

func TestTaskComplete_PlanMode_CountsHistoryCreateTask(t *testing.T) {
	te := newTestExecutor(t)
	plan := createParentTask(t, te.store, "plan-task-h1", "proj-1", "backlog")

	// Simulate a previous execution: a Main thread with a successful
	// create_task tool call, and the child task on the board.
	thread := &types.TaskThread{ID: "thread-h1", TaskID: plan.ID, Name: "Main", IsActive: true, CreatedAt: time.Now().UTC()}
	if err := te.store.CreateThread(thread); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	seedCreateTaskHistory(t, te.store, plan.ID, "thread-h1", "call-h1", "child-h1", "Seeded Child", true)

	// Fresh execution state — nothing created in this execution.
	state := &executionState{}
	result, ok := te.executeTool(context.Background(), "request_review", map[string]any{"summary": "Plan complete"}, "", plan, state, nil)
	if !ok {
		t.Errorf("expected request_review to succeed using history, got: %s", result)
	}
}

func TestTaskComplete_PlanMode_HistoryFailedCreateTaskDoesNotCount(t *testing.T) {
	te := newTestExecutor(t)
	plan := createParentTask(t, te.store, "plan-task-h2", "proj-1", "backlog")
	thread := &types.TaskThread{ID: "thread-h2", TaskID: plan.ID, Name: "Main", IsActive: true, CreatedAt: time.Now().UTC()}
	if err := te.store.CreateThread(thread); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	seedCreateTaskHistory(t, te.store, plan.ID, "thread-h2", "call-h2", "child-h2", "Seeded Child", false)

	state := &executionState{}
	result, ok := te.executeTool(context.Background(), "request_review", map[string]any{"summary": "Plan complete"}, "", plan, state, nil)
	if ok {
		t.Error("expected request_review to fail: a failed create_task in history should not count")
	}
	if !strings.Contains(result, "Cannot complete planning without creating") {
		t.Errorf("unexpected error message: %s", result)
	}
}

func TestPlanCreatedTaskIDs_MergesHistoryAndState(t *testing.T) {
	te := newTestExecutor(t)
	plan := createParentTask(t, te.store, "plan-task-h4", "proj-1", "backlog")
	thread := &types.TaskThread{ID: "thread-h4", TaskID: plan.ID, Name: "Main", IsActive: true, CreatedAt: time.Now().UTC()}
	if err := te.store.CreateThread(thread); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	seedCreateTaskHistory(t, te.store, plan.ID, "thread-h4", "call-h4a", "child-a", "Child A", true)
	seedCreateTaskHistory(t, te.store, plan.ID, "thread-h4", "call-h4b", "child-b", "Child B", true)
	// Non-create_task tool call in history — must be ignored.
	nonCreate, _ := json.Marshal(map[string]any{
		"tool_id": "call-h4c", "tool_name": "write_file", "tool_display_name": "Write",
		"args": map[string]any{"path": "x.go"}, "raw_args": `{"path":"x.go"}`, "sandbox": false,
	})
	res, _ := json.Marshal(map[string]any{"output": "ok", "success": true, "duration": 1})
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID: "seeded-call-h4c", TaskID: plan.ID, ThreadID: "thread-h4", Role: "tool_call",
		ToolCalls: string(nonCreate), ToolResult: string(res), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed non-create message: %v", err)
	}
	state := &executionState{createdIDs: []string{"child-a", "child-b"}}

	ids := te.planCreatedTaskIDs(plan, state)
	if len(ids) != 2 || ids[0] != "child-a" || ids[1] != "child-b" {
		t.Errorf("expected [child-a child-b] (deduped), got %v", ids)
	}
}

// newTestExecutorWithConfig creates a TaskExecutor with config defaults set.
func newTestExecutorWithConfig(t *testing.T, autoMerge, subtasksAutoMerge bool) *TaskExecutor {
	t.Helper()
	s := testStore(t)
	appStreamMgr := stream.NewAppStreamManager(s, nil)
	cs := config.NewConfigStore(s, "", "test")
	cs.SetWorkspaceDirOverride(t.TempDir())
	_ = s.SetConfigValue("providers", "[]")
	_ = s.SetConfigValue("worker_provider", "")
	_ = s.SetConfigValue("worker_model", "")
	_ = s.SetConfigValue("utility_provider", "")
	_ = s.SetConfigValue("utility_model", "")
	_ = s.SetConfigValue("default_auto_merge", fmt.Sprintf("%t", autoMerge))
	_ = s.SetConfigValue("default_subtasks_auto_merge", fmt.Sprintf("%t", subtasksAutoMerge))
	return &TaskExecutor{
		store:        s,
		appStreamMgr: appStreamMgr,
		configStore:  cs,
		tasks:        make(map[string]*executionState),
	}
}

func TestHandleCreateTask_UsesConfigDefaultsForAutoMerge(t *testing.T) {
	autoMerge := true
	subtasksAutoMerge := true
	te := newTestExecutorWithConfig(t, autoMerge, subtasksAutoMerge)
	parent := createParentTask(t, te.store, "parent-auto-merge", "proj-am", "backlog")

	// Create an independent task (no parent_task_id)
	result, err := te.handleCreateTask(map[string]any{
		"title":  "Independent Task",
		"prompt": "Test prompt",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp map[string]string
	json.Unmarshal([]byte(result), &resp)
	task, err := te.store.GetTaskByTaskID(resp["task_id"])
	if err != nil {
		t.Fatalf("get created task: %v", err)
	}

	// Independent task should get config defaults
	if !task.AutoMerge {
		t.Errorf("expected AutoMerge=true from config default, got %v", task.AutoMerge)
	}
	if !task.SubtasksAutoMerge {
		t.Errorf("expected SubtasksAutoMerge=true from config default, got %v", task.SubtasksAutoMerge)
	}
	// AutoCommit must be true when AutoMerge is true (prerequisite)
	if !task.AutoCommit {
		t.Errorf("expected AutoCommit=true (prerequisite for AutoMerge), got %v", task.AutoCommit)
	}
}

func TestHandleCreateTask_SubtaskInheritsSubtasksAutoMergeAsAutoMerge(t *testing.T) {
	// Config defaults: autoMerge=false, subtasksAutoMerge=true
	te := newTestExecutorWithConfig(t, false, true)
	// Parent has subtasks_auto_merge=true (from config or explicit)
	parent := createParentTask(t, te.store, "parent-inherit-am", "proj-inh", "backlog")
	parent.SubtasksAutoMerge = true
	_ = te.store.UpdateTask(parent)

	// Create a subtask under this parent
	result, err := te.handleCreateTask(map[string]any{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "parent-inherit-am",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp map[string]string
	json.Unmarshal([]byte(result), &resp)
	child, err := te.store.GetTaskByTaskID(resp["task_id"])
	if err != nil {
		t.Fatalf("get created task: %v", err)
	}

	// Child should inherit parent's SubtasksAutoMerge as its AutoMerge
	if !child.AutoMerge {
		t.Errorf("expected AutoMerge=true (inherited from parent SubtasksAutoMerge), got %v", child.AutoMerge)
	}
	// Child's SubtasksAutoMerge should come from config default
	if !child.SubtasksAutoMerge {
		t.Errorf("expected SubtasksAutoMerge=true from config default, got %v", child.SubtasksAutoMerge)
	}
	// AutoCommit must be true when AutoMerge is true (prerequisite)
	if !child.AutoCommit {
		t.Errorf("expected AutoCommit=true (prerequisite for AutoMerge), got %v", child.AutoCommit)
	}
}

func TestHandleCreateTask_NoConfigDefaults_UsesZeroValues(t *testing.T) {
	// Config with defaults disabled
	te := newTestExecutorWithConfig(t, false, false)
	parent := createParentTask(t, te.store, "parent-no-defaults", "proj-nd", "backlog")

	result, err := te.handleCreateTask(map[string]any{
		"title":  "Task",
		"prompt": "Test prompt",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp map[string]string
	json.Unmarshal([]byte(result), &resp)
	task, err := te.store.GetTaskByTaskID(resp["task_id"])
	if err != nil {
		t.Fatalf("get created task: %v", err)
	}

	// Should be false when config defaults are false
	if task.AutoMerge {
		t.Errorf("expected AutoMerge=false, got %v", task.AutoMerge)
	}
	if task.SubtasksAutoMerge {
		t.Errorf("expected SubtasksAutoMerge=false, got %v", task.SubtasksAutoMerge)
	}
}

func TestTaskComplete_StandardMode_NoValidation(t *testing.T) {
	te := newTestExecutor(t)
	createProject(t, te.store, "proj-1", "proj-1")
	now := time.Now().UTC()
	task := &types.Task{
		ID:             "standard-task-1",
		ProjectID:      "proj-1",
		Title:          "Standard Task",
		Prompt:         "Standard prompt",
		Status:         "in_progress",
		ExecutionState: "running",
		LlamaServerURL: "http://test:8080",
		ModelName:      "test-model",
		OperatingMode:  "act",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Try to complete without subtasks (should succeed)
	result, ok := te.executeTool(context.Background(), "request_review", map[string]any{
		"summary": "Task complete",
	}, "", task, &executionState{}, nil)

	if !ok {
		t.Errorf("expected request_review to succeed in standard mode, got: %s", result)
	}
}

// TestExecuteTool_CreateTask_ErrorFeedback verifies that create_task errors
// are included in the result string returned to the LLM (not silently dropped).
func TestExecuteTool_CreateTask_ErrorFeedback(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-feedback", "proj-fb", "backlog")

	// Missing title — error message must be in result string
	result, ok := te.executeTool(context.Background(), "create_task", map[string]any{
		"prompt": "Test prompt",
	}, "", parent, &executionState{}, nil)
	if ok {
		t.Error("expected create_task to fail with missing title")
	}
	if !strings.Contains(result, "title and prompt are required") {
		t.Errorf("expected error message in result, got: %q", result)
	}

	// Missing prompt — error message must be in result string
	result, ok = te.executeTool(context.Background(), "create_task", map[string]any{
		"title": "Task",
	}, "", parent, &executionState{}, nil)
	if ok {
		t.Error("expected create_task to fail with missing prompt")
	}
	if !strings.Contains(result, "title and prompt are required") {
		t.Errorf("expected error message in result, got: %q", result)
	}

	// Parent not found — error message must be in result string
	result, ok = te.executeTool(context.Background(), "create_task", map[string]any{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "nonexistent-task",
	}, "", parent, &executionState{}, nil)
	if ok {
		t.Error("expected create_task to fail with nonexistent parent")
	}
	if !strings.Contains(result, "not found") {
		t.Errorf("expected 'not found' in error message, got: %q", result)
	}

	// Parent not in backlog — error message must be in result string
	doneParent := createParentTask(t, te.store, "done-parent", "proj-fb-done", "done")
	result, ok = te.executeTool(context.Background(), "create_task", map[string]any{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "done-parent",
	}, "", doneParent, &executionState{}, nil)
	if ok {
		t.Error("expected create_task to fail with parent not in backlog")
	}
	if !strings.Contains(result, "not in backlog") {
		t.Errorf("expected 'not in backlog' in error message, got: %q", result)
	}
}
