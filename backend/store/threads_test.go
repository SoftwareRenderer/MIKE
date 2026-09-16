package store

import (
	"fmt"
	"mike/backend/types"
	"strings"
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
		func() *types.TaskThread {
			th := newThread("th-1", "task-1", "Main")
			th.CreatedAt = baseTime
			return th
		}(),
		func() *types.TaskThread {
			th := newThread("th-2", "task-1", "Fork #1")
			th.CreatedAt = baseTime.Add(1 * time.Second)
			return th
		}(),
		func() *types.TaskThread {
			th := newThread("th-3", "task-1", "Fork #2")
			th.CreatedAt = baseTime.Add(2 * time.Second)
			return th
		}(),
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
		func() *types.TaskMessage {
			m := newMessage("m1", "task-1", "user", "first")
			m.ThreadID = "src-thread"
			m.CreatedAt = baseTime
			return m
		}(),
		func() *types.TaskMessage {
			m := newMessage("m2", "task-1", "assistant", "reply")
			m.ThreadID = "src-thread"
			m.CreatedAt = baseTime.Add(1 * time.Second)
			return m
		}(),
		func() *types.TaskMessage {
			m := newMessage("m3", "task-1", "user", "second")
			m.ThreadID = "src-thread"
			m.CreatedAt = baseTime.Add(2 * time.Second)
			return m
		}(),
	}
	for _, m := range msgs {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	// Copy messages up to and including m2
	if _, err := s.CopyMessagesToThread("src-thread", "tgt-thread", "m2"); err != nil {
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

// TestCopyMessagesToThread_EntryBoundary: an assistant row and the tool calls it issued are one entry,
// and a fork contains whole entries — never half of one, never a row from the next.
func TestCopyMessagesToThread_EntryBoundary(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.CreateThread(newThread("src-thread", "task-1", "Main")); err != nil {
		t.Fatalf("create source thread: %v", err)
	}

	base := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	seed := func(id, role string, offset time.Duration, ownedBy string) {
		m := newMessage(id, "task-1", role, id+" content")
		m.ThreadID = "src-thread"
		m.CreatedAt = base.Add(offset)
		if role == "tool_call" {
			m.ToolCalls = `{"tool_id":"` + id + `","tool_name":"bash","raw_args":"{}"}`
			m.ToolResult = `{"output":"ok","success":true}`
			m.AssistantMsgID = ownedBy // "" models a row written before the column existed
		}
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	seed("u1", "user", 0, "")
	seed("a1", "assistant", 1*time.Second, "")
	seed("tc1", "tool_call", 2*time.Second, "a1")
	seed("tc2", "tool_call", 3*time.Second, "") // legacy: no owner id, adjacency only
	seed("a2", "assistant", 4*time.Second, "")
	seed("u2", "user", 5*time.Second, "")

	tests := []struct {
		forkAt string
		want   []string
	}{
		{"u1", []string{"u1"}},
		// Forking at an assistant row must take the tool calls that answer it — they are the same
		// message on screen — and stop before the next entry.
		{"a1", []string{"u1", "a1", "tc1", "tc2"}},
		// Forking at either tool call takes the whole entry too, so the fork never shows a lone
		// tool card under a reply that is not there.
		{"tc1", []string{"u1", "a1", "tc1", "tc2"}},
		{"tc2", []string{"u1", "a1", "tc1", "tc2"}},
		{"a2", []string{"u1", "a1", "tc1", "tc2", "a2"}},
		{"u2", []string{"u1", "a1", "tc1", "tc2", "a2", "u2"}},
	}

	for i, tc := range tests {
		tgt := fmt.Sprintf("tgt-%d", i)
		th := newThread(tgt, "task-1", "Fork "+tgt)
		if err := s.CreateThread(th); err != nil {
			t.Fatalf("create target thread: %v", err)
		}
		if _, err := s.CopyMessagesToThread("src-thread", tgt, tc.forkAt); err != nil {
			t.Fatalf("fork at %s: %v", tc.forkAt, err)
		}
		msgs, err := s.GetTaskMessages("task-1", tgt)
		if err != nil {
			t.Fatalf("get copied messages: %v", err)
		}
		if len(msgs) != len(tc.want) {
			t.Fatalf("fork at %s: got %d messages (%s), want %d (%s)", tc.forkAt, len(msgs),
				roleSeq(msgs), len(tc.want), strings.Join(tc.want, ","))
		}
		// The copied rows keep their source content, so the seeded content identifies each one.
		var got []string
		for _, m := range msgs {
			got = append(got, strings.TrimSuffix(m.Content, " content"))
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("fork at %s: got %v, want %v", tc.forkAt, got, tc.want)
		}
	}
}

// TestCopyMessagesToThread_PayloadFidelity: every column that gives a row its meaning travels, and the
// copied tool_call row points at the copied assistant row, not the parent's.
func TestCopyMessagesToThread_PayloadFidelity(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.CreateThread(newThread("src-thread", "task-1", "Main")); err != nil {
		t.Fatalf("create source thread: %v", err)
	}
	if err := s.CreateThread(newThread("tgt-thread", "task-1", "Fork #1")); err != nil {
		t.Fatalf("create target thread: %v", err)
	}

	base := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	mk := func(id, role string, offset time.Duration, mutate func(*types.TaskMessage)) {
		m := newMessage(id, "task-1", role, id)
		m.ThreadID = "src-thread"
		m.CreatedAt = base.Add(offset)
		mutate(m)
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	const (
		toolCalls  = `{"tool_id":"t1","tool_name":"write_file","raw_args":"{\"path\":\"x.go\"}"}`
		toolResult = `{"output":"Wrote 82 bytes to x.go","success":true,"duration":1}`
		atts       = `[{"mimeType":"image/png","data":"AAECAw=="}]`
	)
	mk("u1", "user", 0, func(m *types.TaskMessage) { m.Attachments = atts })
	mk("a1", "assistant", time.Second, func(m *types.TaskMessage) {
		m.ReasoningContent = "thinking"
		m.ModelName = "gpt-test"
		m.PromptTokens = 111
		m.GitCommitHash = "deadbeef" // belongs to the parent thread, must not travel
	})
	mk("tc1", "tool_call", 2*time.Second, func(m *types.TaskMessage) {
		m.ToolCalls, m.ToolResult, m.AssistantMsgID = toolCalls, toolResult, "a1"
	})
	mk("adv", "advisory", 3*time.Second, func(m *types.TaskMessage) { m.Kind = "diagnostics" })
	mk("hint", "assistant", 4*time.Second, func(m *types.TaskMessage) { m.IsLoopHint = true })

	inherited, err := s.CopyMessagesToThread("src-thread", "tgt-thread", "hint")
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	got, err := s.GetTaskMessages("task-1", "tgt-thread")
	if err != nil {
		t.Fatalf("get copied: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 copied rows, got %d", len(got))
	}
	byID := map[string]types.TaskMessage{}
	for _, m := range got {
		byID[m.Content] = m
	}

	if u := byID["u1"]; u.Attachments != atts {
		t.Errorf("attachments lost: %q", u.Attachments)
	}
	a1 := byID["a1"]
	if a1.ReasoningContent != "thinking" || a1.ModelName != "gpt-test" || a1.PromptTokens != 111 {
		t.Errorf("assistant payload lost: %+v", a1)
	}
	if a1.GitCommitHash != "deadbeef" {
		// A fork whose rows carry no checkpoint hash has no recorded file state: forking again from
		// inside it falls back to the task's base commit and the files rewind behind the chat.
		t.Errorf("the turn's checkpoint must travel to the fork, got %q", a1.GitCommitHash)
	}
	tc := byID["tc1"]
	if tc.ToolCalls != toolCalls {
		t.Errorf("tool_calls lost: %q", tc.ToolCalls)
	}
	if tc.ToolResult != toolResult {
		t.Errorf("tool_result lost: %q", tc.ToolResult)
	}
	if tc.AssistantMsgID == "" || tc.AssistantMsgID == "a1" || tc.AssistantMsgID != a1.ID {
		t.Errorf("copied tool_call must own the copied assistant: got %q, assistant %q", tc.AssistantMsgID, a1.ID)
	}
	if adv := byID["adv"]; adv.Kind != "diagnostics" {
		t.Errorf("advisory kind lost: %q", adv.Kind)
	}
	if hint := byID["hint"]; !hint.IsLoopHint {
		t.Error("is_loop_hint lost — the fork would feed a parent's loop hint to the model as real input")
	}

	// The copy reports the snapshots it inherited, keyed by the FORK's row ids, so the caller can pin
	// them for the fork instead of leaving them alive only through the parent thread's refs.
	if len(inherited) != 1 || inherited[0].CommitHash != "deadbeef" || inherited[0].MessageID != a1.ID {
		t.Errorf("inherited checkpoints = %+v, want one {%s deadbeef}", inherited, a1.ID)
	}
}

// roleSeq renders a message slice as "user,assistant,tool_call,…" for failure messages.
func roleSeq(msgs []types.TaskMessage) string {
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	return strings.Join(roles, ",")
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
