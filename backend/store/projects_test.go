package store

import (
	"kanban/backend/types"
	"testing"
)

func TestCreateAndGetProject(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "My Project", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	got, err := s.GetProject("proj-1")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.ID != "proj-1" || got.Name != "My Project" || got.Path != "/tmp/repo" {
		t.Fatalf("unexpected project: %+v", got)
	}
}

func TestGetProjectNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.GetProject("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestListProjects(t *testing.T) {
	s := testStore(t)
	// Create projects in non-alphabetical order
	for _, name := range []string{"Charlie", "Alpha", "Bravo"} {
		proj := newProject(name, name, "/tmp/"+name)
		if err := s.CreateProject(proj); err != nil {
			t.Fatalf("create project: %v", err)
		}
	}

	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(projects))
	}
	// Should be sorted by creation time (insertion order)
	names := []string{projects[0].Name, projects[1].Name, projects[2].Name}
	expected := []string{"Charlie", "Alpha", "Bravo"}
	if names[0] != expected[0] || names[1] != expected[1] || names[2] != expected[2] {
		t.Fatalf("unexpected order: %v", names)
	}
}

func TestUpdateProject(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "Old Name", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	updated, err := s.UpdateProject("proj-1", "New Name")
	if err != nil {
		t.Fatalf("update project: %v", err)
	}
	if updated.Name != "New Name" {
		t.Fatalf("unexpected name: %s", updated.Name)
	}
}

func TestDeleteProject(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	if err := s.DeleteProject("proj-1"); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	_, err := s.GetProject("proj-1")
	if err == nil {
		t.Fatal("expected error for deleted project")
	}
}

func TestDeleteProjectCascadesTasks(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create tasks for this project
	for _, id := range []string{"task-1", "task-2"} {
		if err := s.CreateTask(newTask(id, "proj-1")); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	// Create tasks for another project
	proj2 := newProject("proj-2", "test2", "/tmp/repo2")
	if err := s.CreateProject(proj2); err != nil {
		t.Fatalf("create project 2: %v", err)
	}
	if err := s.CreateTask(newTask("task-3", "proj-2")); err != nil {
		t.Fatalf("create task for proj-2: %v", err)
	}

	// Delete first project
	if err := s.DeleteProject("proj-1"); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	// Tasks for proj-1 should be gone (CASCADE)
	tasks, err := s.GetTasksByProject("proj-1")
	if err != nil {
		t.Fatalf("get tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks after project delete, got %d", len(tasks))
	}

	// Tasks for proj-2 should still exist
	tasks2, err := s.GetTasksByProject("proj-2")
	if err != nil {
		t.Fatalf("get tasks for proj-2: %v", err)
	}
	if len(tasks2) != 1 {
		t.Fatalf("expected 1 task for proj-2, got %d", len(tasks2))
	}
}

func TestGetAllProjectCounts(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create tasks in different statuses
	backlog1 := newTask("b1", "proj-1")
	backlog1.Status = "backlog"
	backlog2 := newTask("b2", "proj-1")
	backlog2.Status = "backlog"
	inProgress := newTask("ip1", "proj-1")
	inProgress.Status = "in_progress"
	review := newTask("r1", "proj-1")
	review.Status = "review"
	done := newTask("d1", "proj-1")
	done.Status = "done"

	for _, task := range []*types.Task{backlog1, backlog2, inProgress, review, done} {
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	counts := s.GetAllProjectCounts()
	c, ok := counts["proj-1"]
	if !ok {
		t.Fatal("expected counts for proj-1")
	}
	if c.Backlog != 2 {
		t.Fatalf("expected 2 backlog, got %d", c.Backlog)
	}
	if c.InProgress != 1 {
		t.Fatalf("expected 1 in_progress, got %d", c.InProgress)
	}
	if c.Review != 1 {
		t.Fatalf("expected 1 review, got %d", c.Review)
	}
	// Done tasks should NOT be counted
}

func TestGetAllProjectCountsMultipleProjects(t *testing.T) {
	s := testStore(t)
	for _, id := range []string{"proj-1", "proj-2"} {
		proj := newProject(id, id, "/tmp/"+id)
		if err := s.CreateProject(proj); err != nil {
			t.Fatalf("create project: %v", err)
		}
	}

	// proj-1: 1 backlog
	if err := s.CreateTask(newTask("t1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	// proj-2: 1 in_progress
	ip := newTask("t2", "proj-2")
	ip.Status = "in_progress"
	if err := s.CreateTask(ip); err != nil {
		t.Fatalf("create task: %v", err)
	}

	counts := s.GetAllProjectCounts()
	if counts["proj-1"].Backlog != 1 {
		t.Fatalf("proj-1 backlog: %d", counts["proj-1"].Backlog)
	}
	if counts["proj-2"].InProgress != 1 {
		t.Fatalf("proj-2 in_progress: %d", counts["proj-2"].InProgress)
	}
}

func TestProjectUniquePath(t *testing.T) {
	s := testStore(t)
	proj1 := newProject("proj-1", "test", "/tmp/same-path")
	if err := s.CreateProject(proj1); err != nil {
		t.Fatalf("create project 1: %v", err)
	}

	proj2 := newProject("proj-2", "test2", "/tmp/same-path")
	err := s.CreateProject(proj2)
	if err == nil {
		t.Fatal("expected error for duplicate project path")
	}
}

func TestProjectCreatedAtUpdatedAt(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	got, err := s.GetProject("proj-1")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("timestamps should be set")
	}
}

func TestProjectSandboxEnabled(t *testing.T) {
	s := testStore(t)

	// Create project with sandbox disabled (default)
	proj := newProject("proj-1", "test", "/tmp/repo")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	got, _ := s.GetProject("proj-1")
	if got.SandboxEnabled {
		t.Fatal("expected sandbox_enabled to be false by default")
	}

	// Enable sandbox
	if err := s.UpdateProjectSandboxEnabled("proj-1", true); err != nil {
		t.Fatalf("update sandbox: %v", err)
	}
	got, _ = s.GetProject("proj-1")
	if !got.SandboxEnabled {
		t.Fatal("expected sandbox_enabled to be true after update")
	}

	// Disable sandbox
	if err := s.UpdateProjectSandboxEnabled("proj-1", false); err != nil {
		t.Fatalf("update sandbox: %v", err)
	}
	got, _ = s.GetProject("proj-1")
	if got.SandboxEnabled {
		t.Fatal("expected sandbox_enabled to be false after disable")
	}

	// Create project with sandbox enabled
	proj2 := newProject("proj-2", "sandboxed", "/tmp/repo2")
	proj2.SandboxEnabled = true
	if err := s.CreateProject(proj2); err != nil {
		t.Fatalf("create sandboxed project: %v", err)
	}
	got2, _ := s.GetProject("proj-2")
	if !got2.SandboxEnabled {
		t.Fatal("expected sandbox_enabled to be true for newly created project")
	}

	// Verify ListProjects includes sandbox_enabled
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
}
