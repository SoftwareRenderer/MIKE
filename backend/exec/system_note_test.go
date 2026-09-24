package exec

import (
	"testing"
	"time"

	"mike/backend/types"
)

// The retry hint needs record's tolerance for an empty thread; the pause and merge notes want
// recordOnThread.
func TestSystemNoteRecordShapes(t *testing.T) {
	t.Parallel()
	_, s := newTestExecutorWithStore(t)
	now := time.Now().UTC()
	if err := s.CreateProject(&types.Project{ID: "proj-note2", Name: "note2", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct{ id, thread string }{
		{"task-unthreaded", ""},
		{"task-threaded", "thread-x"},
	} {
		if err := s.CreateTask(&types.Task{
			ID: spec.id, ProjectID: "proj-note2", Title: spec.id, CurrentThreadID: spec.thread,
			Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateThread(&types.TaskThread{ID: "thread-x", TaskID: "task-threaded", Name: "Main", IsActive: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	note := systemNote{kind: memoryPauseNoteKind, severity: "warning", content: "why it stopped"}

	if id := note.record(s, "task-unthreaded", ""); id == "" {
		t.Fatal("record must write with no thread to name — the retry hint's note has nowhere else to go")
	}
	if id := note.recordOnThread(s, "task-unthreaded", ""); id != "" {
		t.Errorf("recordOnThread must decline an empty thread, got id %q", id)
	}
	rows, err := s.GetTaskMessages("task-unthreaded", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want the one row record wrote, got %d: %+v", len(rows), rows)
	}
	if rows[0].Role != "system" || rows[0].Kind != memoryPauseNoteKind || rows[0].Severity != "warning" {
		t.Errorf("row = role %q kind %q severity %q, want system/%s/warning", rows[0].Role, rows[0].Kind, rows[0].Severity, memoryPauseNoteKind)
	}

	if id := note.recordOnThread(s, "task-threaded", "thread-x"); id == "" {
		t.Fatal("recordOnThread must write when there is a thread")
	}
	rows, err = s.GetTaskMessages("task-threaded", "thread-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Content != "why it stopped" {
		t.Fatalf("want the note on the thread, got %+v", rows)
	}
}
