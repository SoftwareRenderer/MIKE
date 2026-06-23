package exec

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/stream"
	"kanban/backend/store"
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
	boardStreamMgr := stream.NewBoardStreamManager(s, nil)
	return &TaskExecutor{
		store:          s,
		boardStreamMgr: boardStreamMgr,
		configStore:    &config.ConfigStore{},
	}
}

// createProject creates a project in the store for testing.
func createProject(t *testing.T, s *store.Store, id, name string) *types.Project {
	t.Helper()
	now := time.Now().UTC()
	p := &types.Project{
		ID:        id,
		Name:      name,
		Path:      "/tmp/test-project",
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
		StatusState:    "idle",
		LlamaServerURL: "http://test:8080",
		ModelName:      "test-model",
		AutoCommit:     true,
		PlanMode:       true,
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

	result := te.handleCreateTask(map[string]interface{}{
		"title":          "Child Task",
		"prompt":         "Implement the child feature",
		"parent_task_id": "parent-task-1",
	}, parent, nil)

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

	te.handleCreateTask(map[string]interface{}{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "parent-task-2",
	}, parent, nil)

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
	if child.PlanMode != parent.PlanMode {
		t.Errorf("expected inherited plan_mode %v, got %v", parent.PlanMode, child.PlanMode)
	}
}

func TestHandleCreateTask_NestedSubtask(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-3", "proj-1", "backlog")

	// Create first child under parent
	result1 := te.handleCreateTask(map[string]interface{}{
		"title":          "Level 1 Task",
		"prompt":         "Level 1 prompt",
		"parent_task_id": "parent-task-3",
	}, parent, nil)

	var resp1 map[string]string
	json.Unmarshal([]byte(result1), &resp1)
	level1ID := resp1["task_id"]

	// Ensure parent can accept nested subtasks (parent must still be in backlog)
	// Create a second child under the first child
	result2 := te.handleCreateTask(map[string]interface{}{
		"title":         "Level 2 Task",
		"prompt":        "Level 2 prompt",
		"parent_task_id": level1ID,
	}, parent, nil)

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

	result := te.handleCreateTask(map[string]interface{}{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "parent-task-4",
	}, parent, nil)

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

	result := te.handleCreateTask(map[string]interface{}{
		"title":          "Child Task",
		"prompt":         "Implement feature X",
		"parent_task_id": "parent-task-5",
	}, parent, nil)

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

	result := te.handleCreateTask(map[string]interface{}{
		"prompt": "Test prompt",
	}, parent, nil)

	if result != "Error: title and prompt are required" {
		t.Errorf("expected error for missing title, got: %s", result)
	}
}

func TestHandleCreateTask_MissingPrompt(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-7", "proj-1", "backlog")

	result := te.handleCreateTask(map[string]interface{}{
		"title": "Task without prompt",
	}, parent, nil)

	if result != "Error: title and prompt are required" {
		t.Errorf("expected error for missing prompt, got: %s", result)
	}
}

func TestHandleCreateTask_ParentNotFound(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-8", "proj-1", "backlog")

	result := te.handleCreateTask(map[string]interface{}{
		"title":         "Child Task",
		"prompt":        "Test prompt",
		"parent_task_id": "nonexistent-task",
	}, parent, nil)

	if !strings.Contains(result, "not found") {
		t.Errorf("expected 'not found' error, got: %s", result)
	}
}

func TestHandleCreateTask_ParentNotInBacklog(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-9", "proj-1", "done")

	result := te.handleCreateTask(map[string]interface{}{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "parent-task-9",
	}, parent, nil)

	if !strings.Contains(result, "not in backlog") {
		t.Errorf("expected 'not in backlog' error, got: %s", result)
	}
}

func TestHandleCreateTask_AllowsInProgressParent(t *testing.T) {
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-10", "proj-1", "in_progress")

	result := te.handleCreateTask(map[string]interface{}{
		"title":          "Child Task",
		"prompt":         "Test prompt",
		"parent_task_id": "parent-task-10",
	}, parent, nil)

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
		result := te.handleCreateTask(map[string]interface{}{
			"title":          "Task " + string(rune('A'+i-1)),
			"prompt":         "Prompt for task " + string(rune('A'+i-1)),
			"parent_task_id": "parent-task-11",
		}, parent, nil)

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

	result := te.handleCreateTask(map[string]interface{}{
		"title":          `Task with "quotes" and \backslash`,
		"prompt":         "Test prompt",
		"parent_task_id": "parent-task-12",
	}, parent, nil)

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
	result, ok := te.executeTool("task_complete", map[string]interface{}{
		"summary": "Plan complete",
	}, "", parent, state, nil, nil, nil)

	if ok {
		t.Error("expected task_complete to fail in plan mode without subtasks")
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
	result := te.handleCreateTask(map[string]interface{}{
		"title":          "Child Task",
		"prompt":         "Implement the child feature",
		"parent_task_id": "plan-parent-1",
	}, parent, state)

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
	result2, ok := te.executeTool("task_complete", map[string]interface{}{
		"summary": "Plan complete",
	}, "", parent, state, nil, nil, nil)

	if !ok {
		t.Errorf("expected task_complete to succeed with subtasks, got: %s", result2)
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
		StatusState:    "running",
		LlamaServerURL: "http://test:8080",
		ModelName:      "test-model",
		PlanMode:       false,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Try to complete without subtasks (should succeed)
	result, ok := te.executeTool("task_complete", map[string]interface{}{
		"summary": "Task complete",
	}, "", task, nil, nil, nil, nil)

	if !ok {
		t.Errorf("expected task_complete to succeed in standard mode, got: %s", result)
	}
}
