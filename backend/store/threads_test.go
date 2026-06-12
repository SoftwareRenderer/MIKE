package store

import (
	"kanban/backend/types"
	"testing"
	"time"
)

func TestCreateAndGetThread(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	th := newThread("thread-1", "task-1", "Main")
	if err := s.CreateThread(th); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	got, err := s.GetThread("thread-1")
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if got.ID != "thread-1" || got.TaskID != "task-1" || got.Name != "Main" {
		t.Fatalf("unexpected thread: %+v", got)
	}
}

func TestGetThreadNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.GetThread("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing thread")
	}
}

func TestGetThreadsByTask(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	baseTime := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	threads := []*types.TaskThread{
		func() *types.TaskThread { th := newThread("th-1", "task-1", "Main"); th.CreatedAt = baseTime; return th }(),
		func() *types.TaskThread { th := newThread("th-2", "task-1", "Fork #1"); th.CreatedAt = baseTime.Add(1 * time.Second); return th }(),
		func() *types.TaskThread { th := newThread("th-3", "task-1", "Fork #2"); th.CreatedAt = baseTime.Add(2 * time.Second); return th }(),
	}
	for _, th := range threads {
		if err := s.CreateThread(th); err != nil {
			t.Fatalf("create thread: %v", err)
		}
	}

	got, err := s.GetThreadsByTask("task-1")
	if err != nil {
		t.Fatalf("get threads: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 threads, got %d", len(got))
	}
	// Should be ordered by created_at
	if got[0].ID != "th-1" || got[1].ID != "th-2" || got[2].ID != "th-3" {
		t.Fatalf("unexpected order: %v, %v, %v", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestSetActiveThread(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create 3 threads, first one active
	th1 := newThread("th-1", "task-1", "Main")
	th1.IsActive = true
	th2 := newThread("th-2", "task-1", "Fork #1")
	th3 := newThread("th-3", "task-1", "Fork #2")

	for _, th := range []*types.TaskThread{th1, th2, th3} {
		if err := s.CreateThread(th); err != nil {
			t.Fatalf("create thread: %v", err)
		}
	}

	// Activate th-2
	if err := s.SetActiveThread("th-2"); err != nil {
		t.Fatalf("set active thread: %v", err)
	}

	threads, err := s.GetThreadsByTask("task-1")
	if err != nil {
		t.Fatalf("get threads: %v", err)
	}

	activeCount := 0
	var activeID string
	for _, th := range threads {
		if th.IsActive {
			activeCount++
			activeID = th.ID
		}
	}
	if activeCount != 1 {
		t.Fatalf("expected 1 active thread, got %d", activeCount)
	}
	if activeID != "th-2" {
		t.Fatalf("expected th-2 to be active, got %s", activeID)
	}
}

func TestDeleteThread(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	th := newThread("th-1", "task-1", "Main")
	if err := s.CreateThread(th); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	if err := s.DeleteThread("th-1"); err != nil {
		t.Fatalf("delete thread: %v", err)
	}

	_, err := s.GetThread("th-1")
	if err == nil {
		t.Fatal("expected error for deleted thread")
	}
}

func TestGetThreadsByCheckpointMessageID(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	th1 := newThread("th-1", "task-1", "Main")
	th1.CheckpointMessageID = "checkpoint-msg-1"
	th2 := newThread("th-2", "task-1", "Fork #1")
	th2.CheckpointMessageID = "checkpoint-msg-1"
	th3 := newThread("th-3", "task-1", "Fork #2")
	th3.CheckpointMessageID = "checkpoint-msg-2"

	for _, th := range []*types.TaskThread{th1, th2, th3} {
		if err := s.CreateThread(th); err != nil {
			t.Fatalf("create thread: %v", err)
		}
	}

	got, err := s.GetThreadsByCheckpointMessageID("checkpoint-msg-1")
	if err != nil {
		t.Fatalf("get threads by checkpoint: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(got))
	}
}

func TestGetThreadsByCheckpointMessageTime(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	checkpointTime := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	th1 := newThread("th-1", "task-1", "Fork #1")
	th1.CheckpointMessageTime = &checkpointTime
	th2 := newThread("th-2", "task-1", "Fork #2")
	th2.CheckpointMessageTime = &checkpointTime

	for _, th := range []*types.TaskThread{th1, th2} {
		if err := s.CreateThread(th); err != nil {
			t.Fatalf("create thread: %v", err)
		}
	}

	got, err := s.GetThreadsByCheckpointMessageTime(checkpointTime.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("get threads by checkpoint time: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(got))
	}
}

func TestCopyMessagesToThread(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create source and target threads
	src := newThread("src-thread", "task-1", "Main")
	tgt := newThread("tgt-thread", "task-1", "Fork #1")
	if err := s.CreateThread(src); err != nil {
		t.Fatalf("create source thread: %v", err)
	}
	if err := s.CreateThread(tgt); err != nil {
		t.Fatalf("create target thread: %v", err)
	}

	// Create messages in source thread with distinct timestamps
	baseTime := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	msgs := []*types.TaskMessage{
		func() *types.TaskMessage { m := newMessage("m1", "task-1", "user", "first"); m.ThreadID = "src-thread"; m.CreatedAt = baseTime; return m }(),
		func() *types.TaskMessage { m := newMessage("m2", "task-1", "assistant", "reply"); m.ThreadID = "src-thread"; m.CreatedAt = baseTime.Add(1 * time.Second); return m }(),
		func() *types.TaskMessage { m := newMessage("m3", "task-1", "user", "second"); m.ThreadID = "src-thread"; m.CreatedAt = baseTime.Add(2 * time.Second); return m }(),
	}
	for _, m := range msgs {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	// Copy messages up to and including m2
	if err := s.CopyMessagesToThread("src-thread", "tgt-thread", "m2"); err != nil {
		t.Fatalf("copy messages: %v", err)
	}

	// Target should have 2 messages (m1 and m2 content, new IDs)
	tgtMsgs, err := s.GetTaskMessages("task-1", "tgt-thread")
	if err != nil {
		t.Fatalf("get target messages: %v", err)
	}
	if len(tgtMsgs) != 2 {
		t.Fatalf("expected 2 copied messages, got %d", len(tgtMsgs))
	}
	if tgtMsgs[0].Content != "first" || tgtMsgs[1].Content != "reply" {
		t.Fatalf("unexpected copied content: %s, %s", tgtMsgs[0].Content, tgtMsgs[1].Content)
	}
	// IDs should be different (new UUIDs)
	if tgtMsgs[0].ID == "m1" || tgtMsgs[1].ID == "m2" {
		t.Fatal("copied messages should have new IDs")
	}
}

func TestThreadWithParentThreadID(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	parent := newThread("parent-th", "task-1", "Main")
	if err := s.CreateThread(parent); err != nil {
		t.Fatalf("create parent thread: %v", err)
	}

	child := newThread("child-th", "task-1", "Fork #1")
	child.ParentThreadID = "parent-th"
	if err := s.CreateThread(child); err != nil {
		t.Fatalf("create child thread: %v", err)
	}

	got, err := s.GetThread("child-th")
	if err != nil {
		t.Fatalf("get child thread: %v", err)
	}
	if got.ParentThreadID != "parent-th" {
		t.Fatalf("unexpected parent_thread_id: %s", got.ParentThreadID)
	}
}

func TestThreadWithGitBranch(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	th := newThread("th-1", "task-1", "Fork #1")
	th.GitBranch = "kanban/fork-branch"
	if err := s.CreateThread(th); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	got, err := s.GetThread("th-1")
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if got.GitBranch != "kanban/fork-branch" {
		t.Fatalf("unexpected git_branch: %s", got.GitBranch)
	}
}
