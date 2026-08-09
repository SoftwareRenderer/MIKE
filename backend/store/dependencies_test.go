package store

import (
	"testing"
)

func TestCreateAndGetDependency(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	for _, id := range []string{"task-1", "task-2"} {
		if err := s.CreateTask(newTask(id, "proj-1")); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	dep := newDependency("dep-1", "task-1", "task-2")
	if err := s.CreateDependency(dep); err != nil {
		t.Fatalf("create dependency: %v", err)
	}

	// task-1 is parent, task-2 is child
	// GetDependents of task-1 should return task-2
	dependents, err := s.GetDependents("task-1")
	if err != nil {
		t.Fatalf("get dependents: %v", err)
	}
	if len(dependents) != 1 || dependents[0].ChildTaskID != "task-2" {
		t.Fatalf("unexpected dependents: %+v", dependents)
	}

	// GetDependencies of task-2 should return task-1
	deps, err := s.GetDependencies("task-2")
	if err != nil {
		t.Fatalf("get dependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].ParentTaskID != "task-1" {
		t.Fatalf("unexpected dependencies: %+v", deps)
	}
}

func TestDeleteDependency(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	for _, id := range []string{"task-1", "task-2"} {
		if err := s.CreateTask(newTask(id, "proj-1")); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	dep := newDependency("dep-1", "task-1", "task-2")
	if err := s.CreateDependency(dep); err != nil {
		t.Fatalf("create dependency: %v", err)
	}

	if err := s.DeleteDependency("task-1", "task-2"); err != nil {
		t.Fatalf("delete dependency: %v", err)
	}

	dependents, err := s.GetDependents("task-1")
	if err != nil {
		t.Fatalf("get dependents: %v", err)
	}
	if len(dependents) != 0 {
		t.Fatalf("expected 0 dependents after delete, got %d", len(dependents))
	}
}

func TestMultipleDependents(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	for _, id := range []string{"parent", "child-1", "child-2", "child-3"} {
		if err := s.CreateTask(newTask(id, "proj-1")); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	// parent has 3 children
	for _, child := range []string{"child-1", "child-2", "child-3"} {
		dep := newDependency("dep-"+child, "parent", child)
		if err := s.CreateDependency(dep); err != nil {
			t.Fatalf("create dependency: %v", err)
		}
	}

	dependents, err := s.GetDependents("parent")
	if err != nil {
		t.Fatalf("get dependents: %v", err)
	}
	if len(dependents) != 3 {
		t.Fatalf("expected 3 dependents, got %d", len(dependents))
	}

	deps, err := s.GetDependencies("child-1")
	if err != nil {
		t.Fatalf("get dependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].ParentTaskID != "parent" {
		t.Fatalf("unexpected dependencies for child-1: %+v", deps)
	}
}

func TestDependencyAutoStartField(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	for _, id := range []string{"task-1", "task-2"} {
		if err := s.CreateTask(newTask(id, "proj-1")); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	dep := newDependency("dep-1", "task-1", "task-2")
	dep.AutoStart = false
	if err := s.CreateDependency(dep); err != nil {
		t.Fatalf("create dependency: %v", err)
	}

	dependents, err := s.GetDependents("task-1")
	if err != nil {
		t.Fatalf("get dependents: %v", err)
	}
	if dependents[0].AutoStart {
		t.Fatal("auto_start should be false")
	}
}

func TestDependencyCascadeDelete(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	for _, id := range []string{"task-1", "task-2"} {
		if err := s.CreateTask(newTask(id, "proj-1")); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	dep := newDependency("dep-1", "task-1", "task-2")
	if err := s.CreateDependency(dep); err != nil {
		t.Fatalf("create dependency: %v", err)
	}

	// Delete the parent task — dependency should be CASCADE deleted
	if err := s.DeleteTask("proj-1", "task-1"); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	dependents, err := s.GetDependents("task-1")
	if err != nil {
		t.Fatalf("get dependents: %v", err)
	}
	if len(dependents) != 0 {
		t.Fatalf("expected 0 dependents after cascade, got %d", len(dependents))
	}
}
