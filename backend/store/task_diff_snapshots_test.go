package store

import (
	"testing"
	"time"
)

func TestTaskDiffSnapshot_RoundTrip(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	snap := &TaskDiffSnapshot{
		TaskID:     "task-1",
		ProjectID:  "proj-1",
		BaseCommit: "aaa111",
		BranchTip:  "bbb222",
		StatsJSON:  `{"hash":"h1","files_n":2}`,
		DiffJSON:   `{"diff":"..."}`,
	}
	if err := s.UpsertTaskDiffSnapshot(snap); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetTaskDiffSnapshot("task-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected snapshot, got nil")
	}
	if got.BaseCommit != "aaa111" || got.BranchTip != "bbb222" || got.StatsJSON != `{"hash":"h1","files_n":2}` || got.DiffJSON != `{"diff":"..."}` {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set")
	}
}

func TestTaskDiffSnapshot_GetMissing(t *testing.T) {
	s := testStore(t)
	got, err := s.GetTaskDiffSnapshot("nope")
	if err != nil {
		t.Fatalf("expected no error for missing snapshot, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %+v", got)
	}
}

func TestTaskDiffSnapshot_UpsertReplaces(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	first := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if err := s.UpsertTaskDiffSnapshot(&TaskDiffSnapshot{
		TaskID: "task-1", ProjectID: "proj-1", BaseCommit: "aaa", BranchTip: "bbb",
		StatsJSON: `{"v":1}`, DiffJSON: `{}`, CreatedAt: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	// Simulate re-done task: new refs, new content. created_at should be
	// replaced too (fresh snapshot), so the second row uses a different time.
	if err := s.UpsertTaskDiffSnapshot(&TaskDiffSnapshot{
		TaskID: "task-1", ProjectID: "proj-1", BaseCommit: "ccc", BranchTip: "ddd",
		StatsJSON: `{"v":2}`, DiffJSON: `{"diff":"new"}`,
	}); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}

	got, err := s.GetTaskDiffSnapshot("task-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected snapshot, got nil")
	}
	if got.BaseCommit != "ccc" || got.BranchTip != "ddd" || got.StatsJSON != `{"v":2}` {
		t.Fatalf("expected replaced snapshot, got %+v", got)
	}
	if got.CreatedAt.Format(time.RFC3339) == first {
		t.Fatal("expected a fresh created_at on replace")
	}
}

func TestTaskDiffSnapshot_Delete(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.UpsertTaskDiffSnapshot(&TaskDiffSnapshot{
		TaskID: "task-1", ProjectID: "proj-1", BaseCommit: "aaa", BranchTip: "bbb",
		StatsJSON: `{}`, DiffJSON: `{}`,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.DeleteTaskDiffSnapshot("task-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := s.GetTaskDiffSnapshot("task-1")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil after delete, got %+v", got)
	}
}

func TestTaskDiffSnapshot_CascadeOnTaskDelete(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.UpsertTaskDiffSnapshot(&TaskDiffSnapshot{
		TaskID: "task-1", ProjectID: "proj-1", BaseCommit: "aaa", BranchTip: "bbb",
		StatsJSON: `{}`, DiffJSON: `{}`,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Deleting the task cascades to its snapshot (PRAGMA foreign_keys = ON).
	if err := s.DeleteTask("proj-1", "task-1"); err != nil {
		t.Fatalf("delete task: %v", err)
	}
	got, err := s.GetTaskDiffSnapshot("task-1")
	if err != nil {
		t.Fatalf("get after task delete: %v", err)
	}
	if got != nil {
		t.Fatalf("expected snapshot cascaded away, got %+v", got)
	}
}
