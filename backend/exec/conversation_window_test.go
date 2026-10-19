package exec

// A turn reads its conversation from the newest summarization row onward. What the provider is sent
// must not change, because buildLLMContext already dropped every row older than that one.

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mike/backend/store"
	"mike/backend/types"
)

func TestConversationWindowSendsWhatTheWholeThreadSends(t *testing.T) {
	s, err := store.NewStore(filepath.Join(t.TempDir(), "window.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now().UTC()
	if err := s.CreateProject(&types.Project{ID: "p1", Name: "p1", Path: "/tmp/p1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{
		ID: "t1", ProjectID: "p1", Title: "t1", Prompt: "count to ten", Status: types.StatusInProgress,
		ExecutionState: types.ExecStateIdle, CurrentThreadID: "thr", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	const summaryText = "Summarized: the agent created hello.bas and counted to five."
	base := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	rows := []*types.TaskMessage{
		{ID: "u1", TaskID: "t1", ThreadID: "thr", Role: "user", Content: "Create hello.bas", CreatedAt: base},
		{ID: "a1", TaskID: "t1", ThreadID: "thr", Role: "assistant", Content: "I will create it.", CreatedAt: base.Add(time.Second)},
		{ID: "u2", TaskID: "t1", ThreadID: "thr", Role: "user", Content: "Now count to five", CreatedAt: base.Add(2 * time.Second)},
		{ID: "a2", TaskID: "t1", ThreadID: "thr", Role: "assistant", Content: "One, two, three, four, five.", CreatedAt: base.Add(3 * time.Second)},
		{ID: "sum-1", TaskID: "t1", ThreadID: "thr", Role: "summarization", Content: summaryText, CreatedAt: base.Add(4 * time.Second)},
		{ID: "u3", TaskID: "t1", ThreadID: "thr", Role: "user", Content: "Count to ten", CreatedAt: base.Add(5 * time.Second)},
		{ID: "a3", TaskID: "t1", ThreadID: "thr", Role: "assistant", Content: "Six, seven, eight, nine, ten.", CreatedAt: base.Add(6 * time.Second)},
	}
	for _, m := range rows {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message %s: %v", m.ID, err)
		}
	}

	te := newTestExecutorWith(t, Deps{Store: s})
	scope := promptScope{projectName: "p1"}
	worktree := t.TempDir()
	stored, err := s.GetTaskByID(task.ID)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	wholeRows, err := s.GetTaskMessages(task.ID, task.CurrentThreadID)
	if err != nil {
		t.Fatalf("read the whole thread: %v", err)
	}
	whole := buildMessagesFromDB(stored, wholeRows, worktree, scope)
	windowed, _, _ := te.conversationFromDB(task, worktree, scope)

	wholePayload, windowedPayload := buildLLMContext(whole), buildLLMContext(windowed)
	if len(wholePayload) < 3 {
		t.Fatalf("the whole-thread payload holds %d messages; the fixture stopped exercising a real conversation", len(wholePayload))
	}
	// The system prompt is rendered from the conversation, so compare it apart from the turns.
	if wholePayload[0].Content != windowedPayload[0].Content {
		t.Errorf("the two reads built different system prompts: %d bytes vs %d bytes",
			len(wholePayload[0].Content), len(windowedPayload[0].Content))
	}
	if !reflect.DeepEqual(wholePayload[1:], windowedPayload[1:]) {
		t.Errorf("the windowed read sends different turns:\n whole=%+v\n window=%+v", wholePayload[1:], windowedPayload[1:])
	}
	// The window still carries the summary the system prompt is built from.
	if got := extractPrevSummary(windowed); got != summaryText {
		t.Errorf("the windowed conversation carries summary %q, want %q", got, summaryText)
	}
	if got, err := s.GetTaskMessagesFromSummary(task.ID, task.CurrentThreadID); err != nil || len(got) != 3 {
		t.Errorf("the bounded read returned %d rows (err %v), want the summary and the two turns after it", len(got), err)
	}
}
