package exec

// A fork must rebuild the same conversation the parent rebuilds — same turns, tool calls and results.
// Without the copied payloads the model saw a run of empty assistant turns: it had said something, but
// not what it had done.

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"kanban/backend/store"
	"kanban/backend/types"
)

func forkCtxStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "fork.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now().UTC()
	if err := s.CreateProject(&types.Project{ID: "p1", Name: "p1", Path: "/tmp/p1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(&types.Task{
		ID: "t1", ProjectID: "p1", Title: "t1", Prompt: "prompt", Status: types.StatusBacklog,
		ExecutionState: types.ExecStateIdle, CurrentThreadID: "src", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	for _, th := range []*types.TaskThread{
		{ID: "src", TaskID: "t1", Name: "Main", CreatedAt: now},
		{ID: "fork", TaskID: "t1", Name: "Fork #1", ParentThreadID: "src", CreatedAt: now},
	} {
		if err := s.CreateThread(th); err != nil {
			t.Fatalf("create thread %s: %v", th.ID, err)
		}
	}
	return s
}

// TestForkRebuildsTheParentsConversation forks at the last user message and asserts the fork's rows
// rebuild into exactly the conversation the parent's rows do.
func TestForkRebuildsTheParentsConversation(t *testing.T) {
	s := forkCtxStore(t)
	base := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	// The reported shape: an assistant row with no text whose content is the tool calls it issued.
	const owner = "asst-1"
	rows := []*types.TaskMessage{
		{ID: "u1", TaskID: "t1", ThreadID: "src", Role: "user", Content: "Create hello.bas and count to 10", CreatedAt: base},
		{ID: owner, TaskID: "t1", ThreadID: "src", Role: "assistant", Content: "", CreatedAt: base.Add(time.Second)},
		{ID: "tc1", TaskID: "t1", ThreadID: "src", Role: "tool_call", AssistantMsgID: owner, CreatedAt: base.Add(2 * time.Second),
			ToolCalls:  jsonOf(t, map[string]any{"tool_id": "call-write", "tool_name": "write_file", "raw_args": `{"path":"hello.bas"}`}),
			ToolResult: jsonOf(t, map[string]any{"output": "Wrote 82 bytes to hello.bas", "success": true})},
		{ID: "tc2", TaskID: "t1", ThreadID: "src", Role: "tool_call", AssistantMsgID: owner, CreatedAt: base.Add(3 * time.Second),
			ToolCalls:  jsonOf(t, map[string]any{"tool_id": "call-bash", "tool_name": "bash", "raw_args": `{"command":"cat hello.bas"}`}),
			ToolResult: jsonOf(t, map[string]any{"output": "10 REM Count to 10", "success": true})},
		{ID: "a2", TaskID: "t1", ThreadID: "src", Role: "assistant", Content: "Created hello.bas.", CreatedAt: base.Add(4 * time.Second)},
		{ID: "u2", TaskID: "t1", ThreadID: "src", Role: "user", Content: "Count to 5", CreatedAt: base.Add(5 * time.Second)},
	}
	for _, m := range rows {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create %s: %v", m.ID, err)
		}
	}

	if _, err := s.CopyMessagesToThread("src", "fork", "u2"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	parent, err := s.GetTaskMessages("t1", "src")
	if err != nil {
		t.Fatalf("parent messages: %v", err)
	}
	fork, err := s.GetTaskMessages("t1", "fork")
	if err != nil {
		t.Fatalf("fork messages: %v", err)
	}

	want, got := groupTaskMessages(parent), groupTaskMessages(fork)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("fork rebuilds a different conversation than the parent\n parent: %s\n   fork: %s", renderConv(want), renderConv(got))
	}
	// Assert the parent rebuild really carries them, so a matching fork cannot be both sides empty.
	var calls, results int
	for _, m := range got {
		calls += len(m.ToolCalls)
		if m.Role == "tool" {
			results++
		}
	}
	if calls != 2 || results != 2 {
		t.Fatalf("fork lost the parent's tool calls: %d calls, %d results\n%s", calls, results, renderConv(got))
	}
}

// jsonOf marshals a fixture payload the way the loop stores tool rows.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// renderConv prints a rebuilt conversation as one line per message for failures.
func renderConv(msgs []types.ChatMessage) string {
	out := ""
	for _, m := range msgs {
		out += "\n  " + m.Role + " " + m.Content
		for _, tc := range m.ToolCalls {
			out += " [call:" + tc.ID + "/" + tc.Function.Name + "]"
		}
		if m.ToolCallID != "" {
			out += " [result-for:" + m.ToolCallID + "]"
		}
	}
	return out
}
