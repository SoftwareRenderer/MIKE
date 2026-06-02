package backend

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore creates an in-memory store for testing.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	tmpDir := t.TempDir()
	s, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustUUID(t *testing.T) string {
	t.Helper()
	return generateUUID()
}

// -- Config Tests --

func TestConfigDefaults(t *testing.T) {
	s := newTestStore(t)

	url, err := s.GetConfigValue("llama_server_url")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "http://localhost:8081/v1" {
		t.Errorf("expected default llama_server_url, got %q", url)
	}

	limits, err := s.GetConfigValue("endpoint_limits")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limits != `{"http://localhost:8081/v1":2}` {
		t.Errorf("unexpected endpoint_limits: %q", limits)
	}
}

func TestSetConfigValue(t *testing.T) {
	s := newTestStore(t)

	err := s.SetConfigValue("llama_server_url", "http://example.com/v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	val, err := s.GetConfigValue("llama_server_url")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "http://example.com/v1" {
		t.Errorf("expected updated value, got %q", val)
	}
}

func TestGetConfigValueNotFound(t *testing.T) {
	s := newTestStore(t)

	val, err := s.GetConfigValue("nonexistent_key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty string, got %q", val)
	}
}

// -- Project Tests --

func TestCreateAndGetProject(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	p := &Project{
		ID:        mustUUID(t),
		Name:      "test-project",
		Path:      "/tmp/test-project",
		CreatedAt: now,
		UpdatedAt: now,
	}

	err := s.CreateProject(p)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	got, err := s.GetProject(p.ID)
	if err != nil {
		t.Fatalf("failed to get project: %v", err)
	}
	if got.ID != p.ID || got.Name != p.Name || got.Path != p.Path {
		t.Errorf("project mismatch: got %+v", got)
	}
}

func TestGetProjectNotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetProject("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent project")
	}
}

func TestListProjects(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		p := &Project{
			ID:        mustUUID(t),
			Name:      "project-" + string(rune('A'+i)),
			Path:      "/tmp/project-" + string(rune('A'+i)),
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := s.CreateProject(p); err != nil {
			t.Fatalf("failed to create project: %v", err)
		}
	}

	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("failed to list projects: %v", err)
	}
	if len(projects) != 3 {
		t.Errorf("expected 3 projects, got %d", len(projects))
	}
}

func TestUpdateProject(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	p := &Project{
		ID:        mustUUID(t),
		Name:      "old-name",
		Path:      "/tmp/test",
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.CreateProject(p)

	updated, err := s.UpdateProject(p.ID, "new-name")
	if err != nil {
		t.Fatalf("failed to update project: %v", err)
	}
	if updated.Name != "new-name" {
		t.Errorf("expected name 'new-name', got %q", updated.Name)
	}
}

func TestDeleteProject(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	p := &Project{
		ID:        mustUUID(t),
		Name:      "to-delete",
		Path:      "/tmp/to-delete",
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.CreateProject(p)

	err := s.DeleteProject(p.ID)
	if err != nil {
		t.Fatalf("failed to delete project: %v", err)
	}

	_, err = s.GetProject(p.ID)
	if err == nil {
		t.Fatal("expected error after deletion")
	}
}

func TestCreateProjectDuplicatePath(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	path := "/tmp/unique-path"

	p1 := &Project{ID: mustUUID(t), Name: "p1", Path: path, CreatedAt: now, UpdatedAt: now}
	p2 := &Project{ID: mustUUID(t), Name: "p2", Path: path, CreatedAt: now, UpdatedAt: now}

	s.CreateProject(p1)
	err := s.CreateProject(p2)
	if err == nil {
		t.Fatal("expected error for duplicate path")
	}
}

// -- Task Tests --

func createTestProject(t *testing.T, s *Store) *Project {
	t.Helper()
	now := time.Now().UTC()
	p := &Project{
		ID:        mustUUID(t),
		Name:      "test-project",
		Path:      filepath.Join(os.TempDir(), "test-"+mustUUID(t)),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateProject(p); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	return p
}

func createTestTask(t *testing.T, s *Store, projectID string) *Task {
	t.Helper()
	now := time.Now().UTC()
	task := &Task{
		ID:                       mustUUID(t),
		ProjectID:                projectID,
		Title:                    "Test Task",
		Prompt:                   "Do something",
		Status:                   "backlog",
		ColumnOrder:              0,
		StatusState:              "idle",
		MaxConsecutiveFailedTurns: 3,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	return task
}

func TestCreateAndGetTask(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)

	got, err := s.GetTask(p.ID, task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if got.Title != task.Title || got.Status != task.Status {
		t.Errorf("task mismatch: got %+v", got)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)

	_, err := s.GetTask(p.ID, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestGetTasksByProject(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)

	for i := 0; i < 3; i++ {
		now := time.Now().UTC()
		task := &Task{
			ID:          mustUUID(t),
			ProjectID:   p.ID,
			Title:       "Task " + string(rune('A'+i)),
			Prompt:      "Prompt",
			Status:      "backlog",
			ColumnOrder: i,
			StatusState: "idle",
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		s.CreateTask(task)
	}

	tasks, err := s.GetTasksByProject(p.ID)
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(tasks))
	}
	// Verify ordering
	for i := 0; i < len(tasks); i++ {
		if tasks[i].ColumnOrder != i {
			t.Errorf("task at index %d has order %d, expected %d", i, tasks[i].ColumnOrder, i)
		}
	}
}

func TestUpdateTask(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)

	task.Title = "Updated Title"
	task.Status = "in_progress"
	err := s.UpdateTask(task)
	if err != nil {
		t.Fatalf("failed to update task: %v", err)
	}

	got, _ := s.GetTask(p.ID, task.ID)
	if got.Title != "Updated Title" || got.Status != "in_progress" {
		t.Errorf("task not updated: got %+v", got)
	}
}

func TestUpdateTaskStatusState(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)

	err := s.UpdateTaskStatusState(task.ID, p.ID, "running")
	if err != nil {
		t.Fatalf("failed to update status state: %v", err)
	}

	got, _ := s.GetTask(p.ID, task.ID)
	if got.StatusState != "running" {
		t.Errorf("expected 'running', got %q", got.StatusState)
	}
}

func TestUpdateTaskStatusAndState(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)

	err := s.UpdateTaskStatusAndState(task.ID, p.ID, "review", "awaiting_review")
	if err != nil {
		t.Fatalf("failed to update: %v", err)
	}

	got, _ := s.GetTask(p.ID, task.ID)
	if got.Status != "review" || got.StatusState != "awaiting_review" {
		t.Errorf("expected review/awaiting_review, got %s/%s", got.Status, got.StatusState)
	}
}

func TestDeleteTask(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)

	err := s.DeleteTask(p.ID, task.ID)
	if err != nil {
		t.Fatalf("failed to delete task: %v", err)
	}

	_, err = s.GetTask(p.ID, task.ID)
	if err == nil {
		t.Fatal("expected error after deletion")
	}
}

func TestReorderTasks(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)

	// Create tasks with distinct column orders
	tasks := make([]*Task, 3)
	for i := 0; i < 3; i++ {
		now := time.Now().UTC()
		tasks[i] = &Task{
			ID:          mustUUID(t),
			ProjectID:   p.ID,
			Title:       "Task " + string(rune('A'+i)),
			Prompt:      "Prompt",
			Status:      "backlog",
			ColumnOrder: i,
			StatusState: "idle",
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		s.CreateTask(tasks[i])
	}

	// Reorder: swap first and last
	reorders := []ReorderRequest{
		{ID: tasks[0].ID, ColumnOrder: 2},
		{ID: tasks[2].ID, ColumnOrder: 0},
	}
	err := s.ReorderTasks(p.ID, reorders)
	if err != nil {
		t.Fatalf("failed to reorder: %v", err)
	}

	updated, _ := s.GetTasksByProject(p.ID)
	if updated[0].ID != tasks[2].ID || updated[1].ID != tasks[1].ID || updated[2].ID != tasks[0].ID {
		t.Errorf("reorder failed: expected [t2, t1, t0], got [%s, %s, %s]", updated[0].ID[:8], updated[1].ID[:8], updated[2].ID[:8])
	}
}

func TestTaskCascadeOnProjectDelete(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)

	err := s.DeleteProject(p.ID)
	if err != nil {
		t.Fatalf("failed to delete project: %v", err)
	}

	_, err = s.GetTask(p.ID, task.ID)
	if err == nil {
		t.Fatal("task should have been cascade deleted")
	}
}

// -- Conversation History Tests --

func TestSaveAndGetConversationHistory(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)

	entries := []ConversationHistory{
		{TaskID: task.ID, TurnIndex: 0, Role: "user", Content: "Do the task"},
		{TaskID: task.ID, TurnIndex: 1, Role: "assistant", Content: "Working on it"},
	}
	for i := range entries {
		entries[i].CreatedAt = time.Now().UTC()
		if err := s.SaveConversationHistory(&entries[i]); err != nil {
			t.Fatalf("failed to save history: %v", err)
		}
	}

	history, err := s.GetConversationHistory(task.ID)
	if err != nil {
		t.Fatalf("failed to get history: %v", err)
	}
	if len(history) != 2 {
		t.Errorf("expected 2 entries, got %d", len(history))
	}
	if history[0].Role != "user" {
		t.Errorf("first entry should be user, got %s", history[0].Role)
	}
}

func TestClearConversationHistory(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)

	s.SaveConversationHistory(&ConversationHistory{TaskID: task.ID, TurnIndex: 0, Role: "user", Content: "test", CreatedAt: time.Now().UTC()})

	err := s.ClearConversationHistory(task.ID)
	if err != nil {
		t.Fatalf("failed to clear history: %v", err)
	}

	history, _ := s.GetConversationHistory(task.ID)
	if len(history) != 0 {
		t.Errorf("expected 0 entries after clear, got %d", len(history))
	}
}

func TestConversationHistoryCascadeOnTaskDelete(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)

	s.SaveConversationHistory(&ConversationHistory{TaskID: task.ID, TurnIndex: 0, Role: "user", Content: "test", CreatedAt: time.Now().UTC()})

	s.DeleteTask(p.ID, task.ID)

	history, _ := s.GetConversationHistory(task.ID)
	if len(history) != 0 {
		t.Errorf("history should be cascade deleted, got %d entries", len(history))
	}
}

// -- Task Dependency Tests --

func TestCreateAndGetDependency(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	parent := createTestTask(t, s, p.ID)
	child := createTestTask(t, s, p.ID)

	dep := &TaskDependency{
		ID:           mustUUID(t),
		ParentTaskID: parent.ID,
		ChildTaskID:  child.ID,
		AutoStart:    true,
		CreatedAt:    time.Now().UTC(),
	}
	err := s.CreateDependency(dep)
	if err != nil {
		t.Fatalf("failed to create dependency: %v", err)
	}

	deps, err := s.GetDependencies(child.ID)
	if err != nil {
		t.Fatalf("failed to get dependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].ParentTaskID != parent.ID {
		t.Errorf("dependency mismatch: got %+v", deps)
	}

	dependents, err := s.GetDependents(parent.ID)
	if err != nil {
		t.Fatalf("failed to get dependents: %v", err)
	}
	if len(dependents) != 1 || dependents[0].ChildTaskID != child.ID {
		t.Errorf("dependent mismatch: got %+v", dependents)
	}
}

func TestDependencyExists(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	parent := createTestTask(t, s, p.ID)
	child := createTestTask(t, s, p.ID)

	s.CreateDependency(&TaskDependency{
		ID: mustUUID(t), ParentTaskID: parent.ID, ChildTaskID: child.ID, CreatedAt: time.Now().UTC(),
	})

	exists, err := s.DependencyExists(parent.ID, child.ID)
	if err != nil || !exists {
		t.Errorf("expected dependency to exist")
	}

	exists, err = s.DependencyExists(parent.ID, "nonexistent")
	if err != nil || exists {
		t.Errorf("expected dependency to not exist")
	}
}

func TestDeleteDependency(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	parent := createTestTask(t, s, p.ID)
	child := createTestTask(t, s, p.ID)

	s.CreateDependency(&TaskDependency{
		ID: mustUUID(t), ParentTaskID: parent.ID, ChildTaskID: child.ID, CreatedAt: time.Now().UTC(),
	})

	err := s.DeleteDependency(parent.ID, child.ID)
	if err != nil {
		t.Fatalf("failed to delete dependency: %v", err)
	}

	deps, _ := s.GetDependencies(child.ID)
	if len(deps) != 0 {
		t.Errorf("expected 0 dependencies after delete, got %d", len(deps))
	}
}

func TestDuplicateDependencyRejected(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	parent := createTestTask(t, s, p.ID)
	child := createTestTask(t, s, p.ID)

	dep := &TaskDependency{
		ID: mustUUID(t), ParentTaskID: parent.ID, ChildTaskID: child.ID, CreatedAt: time.Now().UTC(),
	}
	s.CreateDependency(dep)

	// Try to create duplicate
	dep2 := &TaskDependency{
		ID: mustUUID(t), ParentTaskID: parent.ID, ChildTaskID: child.ID, CreatedAt: time.Now().UTC(),
	}
	err := s.CreateDependency(dep2)
	if err == nil {
		t.Fatal("expected error for duplicate dependency")
	}
}

// -- Review Comment Tests --

func TestCreateAndGetReviewComment(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)

	now := time.Now().UTC()
	c := &ReviewComment{
		ID:        mustUUID(t),
		TaskID:    task.ID,
		FilePath:  "src/main.go",
		LineNum:   42,
		Comment:   "Fix this bug",
		Resolved:  false,
		CreatedAt: now,
		UpdatedAt: now,
	}
	err := s.CreateReviewComment(c)
	if err != nil {
		t.Fatalf("failed to create review comment: %v", err)
	}

	comments, err := s.GetReviewComments(task.ID)
	if err != nil {
		t.Fatalf("failed to get comments: %v", err)
	}
	if len(comments) != 1 || comments[0].LineNum != 42 {
		t.Errorf("comment mismatch: got %+v", comments)
	}
}

func TestGetUnresolvedReviewComments(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)
	now := time.Now().UTC()

	// Create resolved comment
	s.CreateReviewComment(&ReviewComment{
		ID: mustUUID(t), TaskID: task.ID, FilePath: "a.go", LineNum: 1,
		Comment: "resolved", Resolved: true, CreatedAt: now, UpdatedAt: now,
	})
	// Create unresolved comment
	s.CreateReviewComment(&ReviewComment{
		ID: mustUUID(t), TaskID: task.ID, FilePath: "b.go", LineNum: 2,
		Comment: "unresolved", Resolved: false, CreatedAt: now, UpdatedAt: now,
	})

	unresolved, err := s.GetUnresolvedReviewComments(task.ID)
	if err != nil {
		t.Fatalf("failed to get unresolved: %v", err)
	}
	if len(unresolved) != 1 || unresolved[0].Comment != "unresolved" {
		t.Errorf("expected 1 unresolved, got %+v", unresolved)
	}
}

func TestUpdateReviewComment(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)
	now := time.Now().UTC()

	c := &ReviewComment{
		ID: mustUUID(t), TaskID: task.ID, FilePath: "a.go", LineNum: 1,
		Comment: "original", Resolved: false, CreatedAt: now, UpdatedAt: now,
	}
	s.CreateReviewComment(c)

	resolved := true
	updated, err := s.UpdateReviewComment(c.ID, nil, &resolved)
	if err != nil {
		t.Fatalf("failed to update comment: %v", err)
	}
	if !updated.Resolved {
		t.Error("expected comment to be resolved")
	}
}

func TestDeleteReviewComment(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	task := createTestTask(t, s, p.ID)
	now := time.Now().UTC()

	c := &ReviewComment{
		ID: mustUUID(t), TaskID: task.ID, FilePath: "a.go", LineNum: 1,
		Comment: "delete me", Resolved: false, CreatedAt: now, UpdatedAt: now,
	}
	s.CreateReviewComment(c)

	err := s.DeleteReviewComment(c.ID)
	if err != nil {
		t.Fatalf("failed to delete comment: %v", err)
	}

	comments, _ := s.GetReviewComments(task.ID)
	if len(comments) != 0 {
		t.Errorf("expected 0 comments after delete, got %d", len(comments))
	}
}

// -- Home Agent Message Tests --

func TestCreateAndGetHomeAgentMessages(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)

	msgs := []*HomeAgentMessage{
		{ID: mustUUID(t), ProjectID: p.ID, Role: "user", Content: "Hello", CreatedAt: time.Now().UTC()},
		{ID: mustUUID(t), ProjectID: p.ID, Role: "assistant", Content: "Hi there", CreatedAt: time.Now().UTC()},
	}
	for _, m := range msgs {
		if err := s.CreateHomeAgentMessage(m); err != nil {
			t.Fatalf("failed to create message: %v", err)
		}
	}

	got, err := s.GetHomeAgentMessages(p.ID)
	if err != nil {
		t.Fatalf("failed to get messages: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 messages, got %d", len(got))
	}
	if got[0].Role != "user" {
		t.Errorf("first message should be user, got %s", got[0].Role)
	}
}

func TestHomeAgentMessagesCascadeOnProjectDelete(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	s.CreateHomeAgentMessage(&HomeAgentMessage{
		ID: mustUUID(t), ProjectID: p.ID, Role: "user", Content: "test", CreatedAt: time.Now().UTC(),
	})

	s.DeleteProject(p.ID)

	msgs, _ := s.GetHomeAgentMessages(p.ID)
	if len(msgs) != 0 {
		t.Errorf("messages should be cascade deleted, got %d", len(msgs))
	}
}

// -- Helper Function Tests --

func TestB2I(t *testing.T) {
	if b2i(true) != 1 {
		t.Error("b2i(true) should be 1")
	}
	if b2i(false) != 0 {
		t.Error("b2i(false) should be 0")
	}
}

func TestI2B(t *testing.T) {
	if i2b(1) != true {
		t.Error("i2b(1) should be true")
	}
	if i2b(0) != false {
		t.Error("i2b(0) should be false")
	}
	if i2b(42) != true {
		t.Error("i2b(42) should be true")
	}
}

// -- AutoCommit/AutoPR Tests --

func TestTaskAutoCommitPersistence(t *testing.T) {
	s := newTestStore(t)
	p := createTestProject(t, s)
	now := time.Now().UTC()

	task := &Task{
		ID:          mustUUID(t),
		ProjectID:   p.ID,
		Title:       "Auto-commit task",
		Prompt:      "Do something",
		Status:      "backlog",
		StatusState: "idle",
		AutoCommit:  true,
		AutoPR:      true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.CreateTask(task)

	got, _ := s.GetTask(p.ID, task.ID)
	if !got.AutoCommit {
		t.Error("AutoCommit should be true")
	}
	if !got.AutoPR {
		t.Error("AutoPR should be true")
	}
}