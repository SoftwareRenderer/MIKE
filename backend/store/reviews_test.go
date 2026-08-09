package store

import (
	"kanban/backend/types"
	"testing"
)

func TestCreateAndGetReviewComments(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	comments := []*types.ReviewComment{
		newReviewComment("c1", "task-1", "main.go", "use fmt instead", 10),
		newReviewComment("c2", "task-1", "main.go", "add error check", 25),
		newReviewComment("c3", "task-1", "utils.go", "rename function", 5),
	}
	for _, c := range comments {
		if err := s.CreateReviewComment(c); err != nil {
			t.Fatalf("create comment: %v", err)
		}
	}

	got, err := s.GetReviewComments("task-1")
	if err != nil {
		t.Fatalf("get comments: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 comments, got %d", len(got))
	}
	// Should be ordered by file_path, line_num
	if got[0].FilePath != "main.go" || got[0].LineNum != 10 {
		t.Fatalf("first comment wrong: %+v", got[0])
	}
	if got[2].FilePath != "utils.go" {
		t.Fatalf("last comment should be utils.go: %+v", got[2])
	}
}

func TestUpdateReviewComment(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	c := newReviewComment("c1", "task-1", "main.go", "original", 10)
	if err := s.CreateReviewComment(c); err != nil {
		t.Fatalf("create comment: %v", err)
	}

	// Update text
	newText := "updated text"
	updated, err := s.UpdateReviewComment("c1", &newText, nil)
	if err != nil {
		t.Fatalf("update comment: %v", err)
	}
	if updated.Comment != "updated text" {
		t.Fatalf("unexpected comment: %s", updated.Comment)
	}

	// Mark as resolved
	resolved := true
	updated, err = s.UpdateReviewComment("c1", nil, &resolved)
	if err != nil {
		t.Fatalf("update resolved: %v", err)
	}
	if !updated.Resolved {
		t.Fatal("comment should be resolved")
	}
	// Text should be unchanged
	if updated.Comment != "updated text" {
		t.Fatalf("text should be preserved: %s", updated.Comment)
	}
}

func TestDeleteReviewComment(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	c := newReviewComment("c1", "task-1", "main.go", "delete me", 10)
	if err := s.CreateReviewComment(c); err != nil {
		t.Fatalf("create comment: %v", err)
	}

	if err := s.DeleteReviewComment("c1"); err != nil {
		t.Fatalf("delete comment: %v", err)
	}

	comments, err := s.GetReviewComments("task-1")
	if err != nil {
		t.Fatalf("get comments: %v", err)
	}
	if len(comments) != 0 {
		t.Fatalf("expected 0 comments after delete, got %d", len(comments))
	}
}

func TestReviewCommentCascadeDelete(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	c := newReviewComment("c1", "task-1", "main.go", "will be cascade deleted", 10)
	if err := s.CreateReviewComment(c); err != nil {
		t.Fatalf("create comment: %v", err)
	}

	// Delete task — review comments should be CASCADE deleted
	if err := s.DeleteTask("proj-1", "task-1"); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	comments, err := s.GetReviewComments("task-1")
	if err != nil {
		t.Fatalf("get comments: %v", err)
	}
	if len(comments) != 0 {
		t.Fatalf("expected 0 comments after cascade, got %d", len(comments))
	}
}
