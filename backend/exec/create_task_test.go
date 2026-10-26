package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"mike/backend/store"
	"mike/backend/types"
)

// projectSeeder is what a fixture needs to put a project in the database: a real store or
// an executor's narrower store field both satisfy it.
type projectSeeder interface {
	CreateProject(p *types.Project) error
}

// createProject creates a project in the store for testing.
func createProject(t *testing.T, s projectSeeder, id, name string) *types.Project {
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
func createParentTask(t *testing.T, s *store.Store, id, projectID string, status types.Status) *types.Task {
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
		ExecutionState: types.ExecStateIdle,
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
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-1", "proj-1", "backlog")

	result, err := te.tools.handleCreateTask(map[string]any{
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
	child, err := te.store.GetTaskByID(childID)
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
	if child.Status != types.StatusBacklog {
		t.Errorf("expected status 'backlog', got %q", child.Status)
	}
	if !child.TitleLLM {
		t.Errorf("a title the model wrote carries no mark: title=%q title_llm=false", child.Title)
	}
}

func TestHandleCreateTask_InheritsSettings(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-2", "proj-1", "backlog")

	_, err := te.tools.handleCreateTask(map[string]any{
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
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-3", "proj-1", "backlog")

	// Create first child under parent
	result1, err := te.tools.handleCreateTask(map[string]any{
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
	result2, err := te.tools.handleCreateTask(map[string]any{
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
	level2, err := te.store.GetTaskByID(level2ID)
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
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-4", "proj-1", "backlog")

	result, err := te.tools.handleCreateTask(map[string]any{
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
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-5", "proj-1", "backlog")

	result, err := te.tools.handleCreateTask(map[string]any{
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
	task, _ := te.store.GetTaskByID(childID)
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
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-6", "proj-1", "backlog")

	_, err := te.tools.handleCreateTask(map[string]any{
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
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-7", "proj-1", "backlog")

	_, err := te.tools.handleCreateTask(map[string]any{
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
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-8", "proj-1", "backlog")

	_, err := te.tools.handleCreateTask(map[string]any{
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

// TestHandleCreateTask_ParentStatusPlacesChild: any parent may have a subtask, and its
// status decides which column the child lands in.
func TestHandleCreateTask_ParentStatusPlacesChild(t *testing.T) {
	t.Parallel()
	cases := []struct {
		parent string
		want   types.Status
	}{
		{"backlog", types.StatusBacklog},
		{"in_progress", types.StatusInProgress},
		{"review", types.StatusBacklog},
		{"done", types.StatusBacklog},
	}
	for n, tc := range cases {
		t.Run(tc.parent, func(t *testing.T) {
			te := newTestExecutor(t)
			parent := createParentTask(t, te.store, fmt.Sprintf("parent-place-%d", n), "proj-1", types.Status(tc.parent))

			result, err := te.tools.handleCreateTask(map[string]any{
				"title":          "Placed Child",
				"prompt":         "Test prompt",
				"parent_task_id": parent.ID,
			}, parent, nil)
			if err != nil {
				t.Fatalf("a parent in %s must accept a subtask: %v", tc.parent, err)
			}
			var resp map[string]string
			if err := json.Unmarshal([]byte(result), &resp); err != nil {
				t.Fatalf("expected JSON response, got: %s", result)
			}
			child, err := te.store.GetTaskByID(resp["task_id"])
			if err != nil {
				t.Fatalf("created child is missing: %v", err)
			}
			if child.Status != tc.want {
				t.Errorf("child status = %s, want %s", child.Status, tc.want)
			}
			if child.Status == types.StatusInProgress && child.ExecutionState != types.ExecStateIdle {
				t.Errorf("a child waiting beneath a started parent must read Pending, got %s", child.ExecutionState)
			}
			if child.ParentTaskID != parent.ID {
				t.Errorf("child parent_task_id = %q, want %s", child.ParentTaskID, parent.ID)
			}
		})
	}
}

func TestHandleCreateTask_AllowsInProgressParent(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-10", "proj-1", "in_progress")

	result, err := te.tools.handleCreateTask(map[string]any{
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
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-11", "proj-1", "backlog")

	// Create multiple subtasks under parent
	for i := 1; i <= 3; i++ {
		result, err := te.tools.handleCreateTask(map[string]any{
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
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-12", "proj-1", "backlog")

	result, err := te.tools.handleCreateTask(map[string]any{
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
	t.Parallel()
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
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "plan-parent-1", "proj-1", "backlog")

	// Create a subtask via handleCreateTask
	state := &executionState{}
	result, err := te.tools.handleCreateTask(map[string]any{
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
		Status:         types.StatusBacklog,
		ExecutionState: types.ExecStateIdle,
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

	ids := te.tools.planCreatedTaskIDs(plan, state)
	if len(ids) != 2 || ids[0] != "child-a" || ids[1] != "child-b" {
		t.Errorf("expected [child-a child-b] (deduped), got %v", ids)
	}
}

func TestHandleCreateTask_UsesConfigDefaultsForAutoMerge(t *testing.T) {
	t.Parallel()
	autoMerge := true
	subtasksAutoMerge := true
	te := newTestExecutorWithConfig(t, autoMerge, subtasksAutoMerge)
	parent := createParentTask(t, te.store, "parent-auto-merge", "proj-am", "backlog")

	// Create an independent task (no parent_task_id)
	result, err := te.tools.handleCreateTask(map[string]any{
		"title":  "Independent Task",
		"prompt": "Test prompt",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp map[string]string
	json.Unmarshal([]byte(result), &resp)
	task, err := te.store.GetTaskByID(resp["task_id"])
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
	t.Parallel()
	// Config defaults: autoMerge=false, subtasksAutoMerge=true
	te := newTestExecutorWithConfig(t, false, true)
	// Parent has subtasks_auto_merge=true (from config or explicit)
	parent := createParentTask(t, te.store, "parent-inherit-am", "proj-inh", "backlog")
	parent.SubtasksAutoMerge = true
	_ = te.store.UpdateTask(parent)

	// Create a subtask under this parent
	result, err := te.tools.handleCreateTask(map[string]any{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "parent-inherit-am",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp map[string]string
	json.Unmarshal([]byte(result), &resp)
	child, err := te.store.GetTaskByID(resp["task_id"])
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
	t.Parallel()
	// Config with defaults disabled
	te := newTestExecutorWithConfig(t, false, false)
	parent := createParentTask(t, te.store, "parent-no-defaults", "proj-nd", "backlog")

	result, err := te.tools.handleCreateTask(map[string]any{
		"title":  "Task",
		"prompt": "Test prompt",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp map[string]string
	json.Unmarshal([]byte(result), &resp)
	task, err := te.store.GetTaskByID(resp["task_id"])
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
	t.Parallel()
	te := newTestExecutor(t)
	createProject(t, te.store, "proj-1", "proj-1")
	now := time.Now().UTC()
	task := &types.Task{
		ID:             "standard-task-1",
		ProjectID:      "proj-1",
		Title:          "Standard Task",
		Prompt:         "Standard prompt",
		Status:         types.StatusInProgress,
		ExecutionState: types.ExecStateRunning,
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
	t.Parallel()
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

	// A done parent is accepted and the child lands in the backlog beneath it.
	doneParent := createParentTask(t, te.store, "done-parent", "proj-fb-done", "done")
	result, ok = te.executeTool(context.Background(), "create_task", map[string]any{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "done-parent",
	}, "", doneParent, &executionState{}, nil)
	if !ok {
		t.Fatalf("a done parent must accept a subtask, got: %s", result)
	}
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(result), &created); err != nil || created.TaskID == "" {
		t.Fatalf("expected a task_id in %s (%v)", result, err)
	}
	child, err := te.store.GetTaskByID(created.TaskID)
	if err != nil {
		t.Fatalf("created child is missing: %v", err)
	}
	if child.Status != types.StatusBacklog {
		t.Errorf("child under a done parent = %s, want backlog", child.Status)
	}
}

// createOriginTask seeds the task a create_task call runs in.
func createOriginTask(t *testing.T, s *store.Store, id, projectID, parentID, baseRef string) *types.Task {
	t.Helper()
	createProject(t, s, projectID, projectID)
	if parentID != "" {
		createTaskInProject(t, s, projectID, parentID)
	}
	now := time.Now().UTC()
	task := &types.Task{
		ID:             id,
		ProjectID:      projectID,
		Title:          "Origin Task",
		Prompt:         "Origin prompt",
		Status:         types.StatusInProgress,
		ExecutionState: types.ExecStateRunning,
		LlamaServerURL: "http://test:8080",
		ModelName:      "test-model",
		OperatingMode:  types.ModePlan,
		ParentTaskID:   parentID,
		BaseRef:        baseRef,
		BaseCommit:     "0123456789abcdef0123456789abcdef01234567",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create origin task: %v", err)
	}
	return task
}

// createdTask reads back the task a create_task result reports.
func createdTask(t *testing.T, s *store.Store, result string) *types.Task {
	t.Helper()
	var resp map[string]string
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("expected a JSON response, got: %s", result)
	}
	task, err := s.GetTaskByID(resp["task_id"])
	if err != nil {
		t.Fatalf("get created task %q: %v", resp["task_id"], err)
	}
	return task
}

// TestHandleCreateTask_RootInheritsOriginBaseBranch: a created root task lands on the base branch
// the person set on the originating task.
func TestHandleCreateTask_RootInheritsOriginBaseBranch(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)
	origin := createOriginTask(t, te.store, "origin-branch-1", "proj-branch-1", "", "test")

	result, err := te.tools.handleCreateTask(map[string]any{
		"title":  "Independent Task",
		"prompt": "Work on the test branch",
	}, origin, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	created := createdTask(t, te.store, result)
	if created.ParentTaskID != "" {
		t.Errorf("expected a root task, got parent %q", created.ParentTaskID)
	}
	if created.BaseRef != "test" {
		t.Errorf("base_ref = %q, want the originating task's %q", created.BaseRef, "test")
	}
	if created.BaseCommit != "" {
		t.Errorf("base_commit = %q, want empty until the task starts", created.BaseCommit)
	}
}

// TestHandleCreateTask_SubtaskGetsNoBaseBranch: a nested task merges into its parent's branch, so it
// carries no base branch of its own.
func TestHandleCreateTask_SubtaskGetsNoBaseBranch(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)
	origin := createOriginTask(t, te.store, "origin-branch-2", "proj-branch-2", "", "test")

	result, err := te.tools.handleCreateTask(map[string]any{
		"title":          "Subtask",
		"prompt":         "Part of the plan",
		"parent_task_id": "origin-branch-2",
	}, origin, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	created := createdTask(t, te.store, result)
	if created.ParentTaskID != "origin-branch-2" {
		t.Errorf("expected parent origin-branch-2, got %q", created.ParentTaskID)
	}
	if created.BaseRef != "" {
		t.Errorf("base_ref = %q on a subtask, want empty: its work lands on the parent's branch", created.BaseRef)
	}
}

// TestHandleCreateTask_RootFromSubtaskOriginGetsNoBaseBranch: a subtask's base_ref is its parent's
// branch, not a configured one, so it passes no base branch on.
func TestHandleCreateTask_RootFromSubtaskOriginGetsNoBaseBranch(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)
	origin := createOriginTask(t, te.store, "origin-branch-3", "proj-branch-3", "origin-branch-3-parent", "task/origin-branch-3-x7q2")

	result, err := te.tools.handleCreateTask(map[string]any{
		"title":  "Independent Task",
		"prompt": "Work outside the subtree",
	}, origin, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if created := createdTask(t, te.store, result); created.BaseRef != "" {
		t.Errorf("base_ref = %q, want empty: a subtask configures no base branch", created.BaseRef)
	}
}

// TestHandleCreateTask_SkipsOriginBaseBranchThatIsNotABranchName: mergeInto creates a branch named by
// base_ref when it is missing, so a name git would refuse is not copied.
func TestHandleCreateTask_SkipsOriginBaseBranchThatIsNotABranchName(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)
	origin := createOriginTask(t, te.store, "origin-branch-4", "proj-branch-4", "", "release..next")

	result, err := te.tools.handleCreateTask(map[string]any{
		"title":  "Independent Task",
		"prompt": "Work on a branch git accepts",
	}, origin, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if created := createdTask(t, te.store, result); created.BaseRef != "" {
		t.Errorf("base_ref = %q, want empty for a name git would refuse", created.BaseRef)
	}
}
