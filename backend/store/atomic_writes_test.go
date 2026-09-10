package store

import (
	"testing"
	"time"

	"kanban/backend/types"
)

// These tests hold the two properties the multi-statement sections exist to provide, and each was written
// against the code *before* the change it defends, so its failure is on the record:
//
//   - a delete that fails deletes nothing (DeleteProject used to delete every dependent row, then fail on
//     the project itself and report an error — children gone, project still listed);
//   - a write sequence that fails part-way leaves the previous state (SetActiveThread used to deactivate
//     every thread, then fail to activate the target — the task was left with no active thread).
//
// Faults are induced with SQLite triggers, not with a production seam: the trigger fires on the statement
// that must fail and on nothing else.

func seededProject(t *testing.T, s *Store, id string) *types.Task {
	t.Helper()
	if err := s.CreateProject(newProject(id, "Project "+id, "/tmp/"+id)); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{
		ID: id + "-task", ProjectID: id, Title: "Seeded task", Status: types.StatusInProgress,
		Branch: "task/" + id, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.CreateThread(&types.TaskThread{
		ID: id + "-thread", TaskID: task.ID, Name: "Main", IsActive: true, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	// An assistant message is what populates the cumulative token tables, so this seeds those too.
	msg := &types.TaskMessage{
		ID: id + "-msg", TaskID: task.ID, ThreadID: id + "-thread", Role: "assistant",
		Content: "done", PromptTokens: 10, PredictedTokens: 2, CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}
	if err := s.UpsertTaskDiffSnapshot(&TaskDiffSnapshot{
		TaskID: task.ID, ProjectID: id, BaseCommit: "abc", BranchTip: "def",
		StatsJSON: "{}", DiffJSON: "[]", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed diff snapshot: %v", err)
	}
	return task
}

// countForProject counts the rows a project owns, directly or through its tasks.
func countForProject(t *testing.T, s *Store, projectID string) map[string]int {
	t.Helper()
	out := map[string]int{}
	queries := map[string]string{
		"projects":            `SELECT COUNT(*) FROM projects WHERE id = ?`,
		"tasks":               `SELECT COUNT(*) FROM tasks WHERE project_id = ?`,
		"task_threads":        `SELECT COUNT(*) FROM task_threads WHERE task_id IN (SELECT id FROM tasks WHERE project_id = ?)`,
		"task_messages":       `SELECT COUNT(*) FROM task_messages WHERE task_id IN (SELECT id FROM tasks WHERE project_id = ?)`,
		"task_diff_snapshots": `SELECT COUNT(*) FROM task_diff_snapshots WHERE task_id IN (SELECT id FROM tasks WHERE project_id = ?)`,
		"token_stats":         `SELECT COUNT(*) FROM project_token_stats WHERE project_id = ?`,
		"token_tasks":         `SELECT COUNT(*) FROM project_token_tasks WHERE project_id = ?`,
	}
	for name, q := range queries {
		var n int
		if err := s.db.QueryRow(q, projectID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		out[name] = n
	}
	return out
}

func TestDeleteProjectRemovesEveryDependentRow(t *testing.T) {
	s := testStore(t)
	seededProject(t, s, "proj-cascade")

	if err := s.DeleteProject("proj-cascade"); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	for name, n := range countForProject(t, s, "proj-cascade") {
		if n != 0 {
			t.Errorf("%s: %d rows left after deleting the project", name, n)
		}
	}
}

// The one that pins the change: a project delete that fails must leave the tree alone.
func TestDeleteProjectThatFailsRemovesNothing(t *testing.T) {
	s := testStore(t)
	seededProject(t, s, "proj-abort")

	if _, err := s.db.Exec(`CREATE TRIGGER abort_project_delete BEFORE DELETE ON projects
		BEGIN SELECT RAISE(ABORT, 'injected: the project row cannot go'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	err := s.DeleteProject("proj-abort")
	if err == nil {
		t.Fatal("DeleteProject reported success against a trigger that aborts the delete")
	}

	before := map[string]int{"projects": 1, "tasks": 1, "task_threads": 1, "task_messages": 1, "task_diff_snapshots": 1}
	for name, want := range countForProject(t, s, "proj-abort") {
		w, pinned := before[name]
		if pinned && want != w {
			t.Errorf("%s: %d rows left, want %d — a failed delete took the %s with it and left the project behind",
				name, want, w, name)
		}
	}
}

func TestSetActiveThreadThatFailsLeavesTheActiveThread(t *testing.T) {
	s := testStore(t)
	task := seededProject(t, s, "proj-active")

	// A second thread to switch to. The trigger fires only on the activating statement (is_active = 1),
	// so the deactivation that runs first is untouched and the test discriminates between the two.
	if err := s.CreateThread(&types.TaskThread{
		ID: "thread-two", TaskID: task.ID, Name: "Fork", IsActive: false, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER abort_activate BEFORE UPDATE ON task_threads
		WHEN NEW.is_active = 1 AND NEW.id = 'thread-two'
		BEGIN SELECT RAISE(ABORT, 'injected: the target cannot be activated'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if err := s.SetActiveThread("thread-two"); err == nil {
		t.Fatal("SetActiveThread reported success against a trigger that aborts its second statement")
	}

	var active int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_threads WHERE task_id = ? AND is_active = 1`, task.ID).Scan(&active); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if active != 1 {
		t.Errorf("active threads after a failed switch = %d, want 1 (the original) — a task with no active thread "+
			"is the state this section must never leave behind", active)
	}
}

// A fork is the copy most likely to stop half-way: it inserts one row per copied message.
func TestCopyMessagesThatFailsCopiesNothing(t *testing.T) {
	s := testStore(t)
	task := seededProject(t, s, "proj-fork")
	source := "proj-fork-thread"

	// A three-row entry: an assistant row owning a tool_call row, then a follow-up.
	for i, m := range []*types.TaskMessage{
		{ID: "copy-1", TaskID: task.ID, ThreadID: source, Role: "assistant", Content: "one", CreatedAt: time.Now().UTC()},
		{ID: "copy-2", TaskID: task.ID, ThreadID: source, Role: "tool_call", Content: "two", CreatedAt: time.Now().UTC().Add(time.Second)},
		{ID: "copy-3", TaskID: task.ID, ThreadID: source, Role: "user", Content: "three", CreatedAt: time.Now().UTC().Add(2 * time.Second)},
	} {
		m.ID = m.ID + "-" + string(rune('a'+i))
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message %d: %v", i, err)
		}
	}
	if err := s.CreateThread(&types.TaskThread{
		ID: "fork-thread", TaskID: task.ID, Name: "Fork", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create fork thread: %v", err)
	}

	// The abort lands mid-copy: the first row would insert, the second would not.
	if _, err := s.db.Exec(`CREATE TRIGGER abort_copy BEFORE INSERT ON task_messages
		WHEN NEW.thread_id = 'fork-thread' AND NEW.role = 'tool_call'
		BEGIN SELECT RAISE(ABORT, 'injected: the copy cannot continue'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, err := s.CopyMessagesToThread(source, "fork-thread", "copy-3-c"); err == nil {
		t.Fatal("CopyMessagesToThread reported success against a trigger that aborts mid-copy")
	}

	var copied int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_messages WHERE thread_id = 'fork-thread'`).Scan(&copied); err != nil {
		t.Fatalf("count copied: %v", err)
	}
	if copied != 0 {
		t.Errorf("copied rows = %d, want 0 — a half-copied fork is a conversation with rows missing and no error left to explain it", copied)
	}
}
