package store

import (
	"encoding/json"
	"testing"

	"kanban/backend/types"
)

func TestGetSetConfigValue(t *testing.T) {
	s := testStore(t)

	// Set a value
	if err := s.SetConfigValue("test_key", "test_value"); err != nil {
		t.Fatalf("set config: %v", err)
	}

	// Get it back
	val, err := s.GetConfigValue("test_key")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if val != "test_value" {
		t.Fatalf("unexpected value: %s", val)
	}
}

func TestGetConfigValueNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.GetConfigValue("nonexistent_key")
	if err == nil {
		t.Fatal("expected error for missing config key")
	}
}

func TestConfigOverwrite(t *testing.T) {
	s := testStore(t)

	if err := s.SetConfigValue("key", "value1"); err != nil {
		t.Fatalf("set config: %v", err)
	}
	if err := s.SetConfigValue("key", "value2"); err != nil {
		t.Fatalf("set config: %v", err)
	}

	val, err := s.GetConfigValue("key")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if val != "value2" {
		t.Fatalf("expected value2, got %s", val)
	}
}

func TestConfigWorkspaceDirDefault(t *testing.T) {
	s := testStore(t)

	val, err := s.GetConfigValue("workspace_dir")
	if err != nil {
		t.Fatalf("get workspace_dir: %v", err)
	}
	if val != "./.worktrees" {
		t.Fatalf("unexpected default workspace_dir: %s", val)
	}
}

func TestResetData(t *testing.T) {
	s := testStore(t)

	// Create project and tasks
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	msg := newMessage("msg-1", "task-1", "user", "hello")
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Reset data
	if err := s.ResetData(); err != nil {
		t.Fatalf("reset data: %v", err)
	}

	// Tasks and messages should be gone
	tasks, err := s.GetTasksByProject("proj-1")
	if err != nil {
		t.Fatalf("get tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks after reset, got %d", len(tasks))
	}

	// Projects should be preserved
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != "proj-1" {
		t.Fatalf("projects should be preserved: %+v", projects)
	}
}

func TestConfigProvidersJSON(t *testing.T) {
	s := testStore(t)

	providers := []types.Provider{
		{
			Name:        "neptune",
			DisplayName: "Neptune",
			URL:         "http://neptune:8010/v1",
			Models: []types.ModelConfig{
				{Name: "model-a", ReasoningEffort: "low"},
				{Name: "model-b", ReasoningEffort: "high"},
			},
			Concurrency: 2,
		},
	}
	jsonData, _ := json.Marshal(providers)

	if err := s.SetConfigValue("providers", string(jsonData)); err != nil {
		t.Fatalf("set providers: %v", err)
	}

	val, err := s.GetConfigValue("providers")
	if err != nil {
		t.Fatalf("get providers: %v", err)
	}

	var got []types.Provider
	if err := json.Unmarshal([]byte(val), &got); err != nil {
		t.Fatalf("unmarshal providers: %v", err)
	}
	if len(got) != 1 || got[0].Name != "neptune" {
		t.Fatalf("unexpected providers: %+v", got)
	}
	if len(got[0].Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(got[0].Models))
	}
}
