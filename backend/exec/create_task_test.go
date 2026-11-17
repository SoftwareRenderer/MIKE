package exec

import (
	"context"
	"encoding/json/v2"
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

// TestHandleCreateTask_CreatedTaskIsRunnable: a created task arrives ready to run — its own active
// "Main" thread, and its prompt stored as the first user message of that thread.
func TestHandleCreateTask_CreatedTaskIsRunnable(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)
	parent := createParentTask(t, te.store, "parent-task-4", "proj-1", "backlog")

	result, err := te.tools.handleCreateTask(map[string]any{
		"title":          "Child Task",
		"prompt":         "Implement feature X",
		"parent_task_id": "parent-task-4",
	}, parent, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var resp map[string]string
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		t.Fatalf("expected JSON response, got: %s", result)
	}
	childID := resp["task_id"]

	threads, err := te.store.GetThreadsByTask(childID)
	if err != nil {
		t.Fatalf("get threads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected 1 thread, got %d", len(threads))
	}
	if threads[0].Name != "Main" || !threads[0].IsActive {
		t.Errorf("thread = %q active=%v, want the active Main thread", threads[0].Name, threads[0].IsActive)
	}

	child, err := te.store.GetTaskByID(childID)
	if err != nil {
		t.Fatalf("get created task: %v", err)
	}
	if child.CurrentThreadID != threads[0].ID {
		t.Errorf("the task points at thread %q, not the thread it was given (%q)", child.CurrentThreadID, threads[0].ID)
	}
	messages, err := te.store.GetTaskMessages(childID, child.CurrentThreadID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Content != "Implement feature X" {
		t.Errorf("first row = role %q content %q, want the prompt as a user message", messages[0].Role, messages[0].Content)
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

// planGateState returns the execution state a plan-mode review is judged against.
type planGateSeed struct {
	// createInRun calls create_task during this execution, recording it in the state.
	createInRun bool
	// historyCall seeds a create_task tool call from an earlier execution; nil means none.
	historyCall *bool
}

func (s planGateSeed) run(t *testing.T, te *TaskExecutor, planID string) *executionState {
	t.Helper()
	state := &executionState{}
	if s.createInRun {
		plan, err := te.store.GetTaskByID(planID)
		if err != nil {
			t.Fatalf("reload plan: %v", err)
		}
		if _, err := te.tools.handleCreateTask(map[string]any{
			"title":          "Child Task",
			"prompt":         "Implement the child feature",
			"parent_task_id": planID,
		}, plan, state); err != nil {
			t.Fatalf("create_task: %v", err)
		}
	}
	if s.historyCall != nil {
		thread := &types.TaskThread{ID: "thread-gate", TaskID: planID, Name: "Main", IsActive: true, CreatedAt: time.Now().UTC()}
		if err := te.store.CreateThread(thread); err != nil {
			t.Fatalf("create thread: %v", err)
		}
		seedCreateTaskHistory(t, te.store, planID, "thread-gate", "call-gate", "child-gate", "Seeded Child", *s.historyCall)
	}
	return state
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

// TestRequestReview_PlanModeNeedsACreatedSubtask: the plan gate opens only for a plan that produced
// children — this execution's or an earlier successful one's — and asks nothing of a turn that is
// not planning.
func TestRequestReview_PlanModeNeedsACreatedSubtask(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mode   types.OperatingMode
		seed   planGateSeed
		wantOK bool
	}{
		{name: "no child at all", mode: types.ModePlan, seed: planGateSeed{}, wantOK: false},
		{name: "a child created in this execution", mode: types.ModePlan, seed: planGateSeed{createInRun: true}, wantOK: true},
		{name: "a successful create_task in history", mode: types.ModePlan, seed: planGateSeed{historyCall: yes()}, wantOK: true},
		{name: "a failed create_task in history", mode: types.ModePlan, seed: planGateSeed{historyCall: no()}, wantOK: false},
		{name: "an act-mode turn is not gated", mode: types.ModeAct, seed: planGateSeed{}, wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := newTestExecutor(t)
			plan := createParentTask(t, te.store, "plan-task", "proj-1", types.StatusBacklog)
			plan.OperatingMode = tc.mode
			if err := te.store.UpdateTask(plan); err != nil {
				t.Fatalf("set mode: %v", err)
			}
			state := tc.seed.run(t, te, plan.ID)
			plan, err := te.store.GetTask(plan.ProjectID, plan.ID)
			if err != nil {
				t.Fatalf("reload plan: %v", err)
			}

			result, ok := te.executeTool(context.Background(), "request_review", map[string]any{
				"summary": "Plan complete",
			}, "", plan, state, nil)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (result: %s)", ok, tc.wantOK, result)
			}
			if !tc.wantOK && !strings.Contains(result, "Cannot complete planning without creating") {
				t.Errorf("unexpected refusal: %s", result)
			}
		})
	}
}

// TestHandleCreateTask_AutoMergeFollowsConfigAndParent: a created task takes auto-merge from the
// parent's subtasks_auto_merge when it has a parent, from the config otherwise, and auto-commit
// always follows auto-merge.
func TestHandleCreateTask_AutoMergeFollowsConfigAndParent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		cfgAutoMerge   bool
		cfgSubtasksAM  bool
		parentSAM      bool
		asSubtask      bool
		wantAutoMerge  bool
		wantSubtasksAM bool
	}{
		{name: "config defaults reach a root task", cfgAutoMerge: true, cfgSubtasksAM: true, wantAutoMerge: true, wantSubtasksAM: true},
		{name: "config defaults off leave them off", wantAutoMerge: false, wantSubtasksAM: false},
		{name: "a subtask inherits the parent's subtasks_auto_merge", cfgSubtasksAM: true, parentSAM: true, asSubtask: true, wantAutoMerge: true, wantSubtasksAM: true},
		{name: "a subtask of a parent that will not merge stays unmerged", cfgAutoMerge: true, cfgSubtasksAM: true, asSubtask: true, wantAutoMerge: false, wantSubtasksAM: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := newTestExecutorWithConfig(t, tc.cfgAutoMerge, tc.cfgSubtasksAM)
			parent := createParentTask(t, te.store, "am-parent", "proj-am", types.StatusBacklog)
			parent.SubtasksAutoMerge = tc.parentSAM
			if err := te.store.UpdateTask(parent); err != nil {
				t.Fatalf("set parent subtasks_auto_merge: %v", err)
			}

			args := map[string]any{"title": "Created Task", "prompt": "Test prompt"}
			if tc.asSubtask {
				args["parent_task_id"] = parent.ID
			}
			result, err := te.tools.handleCreateTask(args, parent, nil)
			if err != nil {
				t.Fatalf("create_task: %v", err)
			}

			created := createdTask(t, te.store, result)
			if created.AutoMerge != tc.wantAutoMerge {
				t.Errorf("auto_merge = %v, want %v", created.AutoMerge, tc.wantAutoMerge)
			}
			if created.SubtasksAutoMerge != tc.wantSubtasksAM {
				t.Errorf("subtasks_auto_merge = %v, want %v", created.SubtasksAutoMerge, tc.wantSubtasksAM)
			}
			if created.AutoMerge && !created.AutoCommit {
				t.Errorf("auto_commit = false while auto_merge = true: auto-commit is a prerequisite of auto-merge")
			}
			if tc.asSubtask && created.ParentTaskID != parent.ID {
				t.Errorf("parent_task_id = %q, want %q", created.ParentTaskID, parent.ID)
			}
		})
	}
}

// TestHandleCreateTask_BaseBranchInheritance: a created root task lands on the base branch the person
// set on the originating task; a subtask, a subtask's own creation and a name git would refuse all
// carry none, because mergeInto creates a branch named by base_ref when it is missing.
func TestHandleCreateTask_BaseBranchInheritance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		originParent  string // non-empty makes the originating task itself a subtask
		originBaseRef string
		asSubtask     bool
		wantBaseRef   string
	}{
		{name: "a root inherits the origin's base branch", originBaseRef: "test", wantBaseRef: "test"},
		{name: "a subtask carries none", originBaseRef: "test", asSubtask: true, wantBaseRef: ""},
		{name: "a root from a subtask origin carries none", originParent: "origin-parent", originBaseRef: "task/origin-x7q2", wantBaseRef: ""},
		{name: "a name git would refuse is not copied", originBaseRef: "release..next", wantBaseRef: ""},
		{name: "an origin with no base branch gives none", originBaseRef: "", wantBaseRef: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			te := newTestExecutor(t)
			origin := createOriginTask(t, te.store, "origin-task", "proj-origin", tc.originParent, tc.originBaseRef)

			args := map[string]any{"title": "Created Task", "prompt": "Test prompt"}
			if tc.asSubtask {
				args["parent_task_id"] = origin.ID
			}
			result, err := te.tools.handleCreateTask(args, origin, nil)
			if err != nil {
				t.Fatalf("create_task: %v", err)
			}

			created := createdTask(t, te.store, result)
			if created.BaseRef != tc.wantBaseRef {
				t.Errorf("base_ref = %q, want %q", created.BaseRef, tc.wantBaseRef)
			}
			if got := created.ParentTaskID != ""; got != tc.asSubtask {
				t.Errorf("parent_task_id = %q, want a subtask=%v", created.ParentTaskID, tc.asSubtask)
			}
			if tc.wantBaseRef != "" && created.BaseCommit != "" {
				t.Errorf("base_commit = %q, want empty until the task starts", created.BaseCommit)
			}
		})
	}
}
