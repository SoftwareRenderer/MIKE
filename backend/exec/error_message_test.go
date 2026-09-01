package exec

import (
	"testing"
	"time"

	"kanban/backend/types"
)

// A failed task used to announce itself again on every page view: the error frame
// stayed in the task's replay buffer, and each replay was rendered and stored as
// if it were a new failure. One failure now means one stored error and one frame,
// and nothing is handed to later subscribers of the task.
func TestFailTask_PersistsOneErrorAndStopsReplayingIt(t *testing.T) {
	e := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-fail-once", Name: "Fail Once", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := e.rawStore().CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{
		ID: "task-fail-once", ProjectID: proj.ID, Title: "Test", Prompt: "Test",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	const failure = "LLM connection failed: dial tcp 127.0.0.1:9: connect: connection refused"
	frames := taskStreamFramesDuring(t, e.appStreamMgr, proj.ID, task.ID, func() {
		e.failTask(task, proj.ID, failure, "llm_connection_failed")
	})

	var sent []*taskStreamFrame
	for i := range frames {
		if frames[i].Event == "error" {
			sent = append(sent, &frames[i])
		}
	}
	if len(sent) != 1 {
		t.Fatalf("broadcast %d error frames, want 1: %+v", len(sent), frames)
	}
	if sent[0].Data["code"] != "llm_connection_failed" {
		t.Errorf("frame code = %v, want llm_connection_failed", sent[0].Data["code"])
	}
	if sent[0].Data["message"] != failure {
		t.Errorf("frame message = %v, want %q", sent[0].Data["message"], failure)
	}

	msgs, err := e.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	if err != nil {
		t.Fatalf("get task messages: %v", err)
	}
	var stored []*types.TaskMessage
	for i := range msgs {
		if msgs[i].Role == "error" {
			stored = append(stored, &msgs[i])
		}
	}
	if len(stored) != 1 {
		t.Fatalf("stored %d error messages, want 1", len(stored))
	}
	// The frame names the row it came from, which is how the client recognizes a
	// frame it has already drawn.
	if sent[0].Data["message_id"] != stored[0].ID {
		t.Errorf("frame message_id = %v, want stored id %s", sent[0].Data["message_id"], stored[0].ID)
	}

	// Opening the task again must not replay the failure it already reported.
	replay := taskStreamFrames(t, e.appStreamMgr, proj.ID, task.ID)
	if frame, found := findTaskStreamEvent(replay, "error"); found {
		t.Errorf("stale error frame replayed to a new subscriber: %+v", frame.Data)
	}
}

// An agent-reported error is stored by the server too, so the client never has to
// write one, and the run keeps its replay buffer — an agent error need not end it.
func TestSendAgentStreamEvent_PersistsAgentError(t *testing.T) {
	e := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-agent-err", Name: "Agent Err", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := e.rawStore().CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{
		ID: "task-agent-err", ProjectID: proj.ID, Title: "Test", Prompt: "Test",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	frames := taskStreamFramesDuring(t, e.appStreamMgr, proj.ID, task.ID, func() {
		e.sendAgentStreamEvent(proj.ID, task.ID, types.StreamEvent{
			Type: types.WSTypeError,
			Data: map[string]string{"message": "provider overloaded", "code": "agent_error"},
		})
	})

	msgs, err := e.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	if err != nil {
		t.Fatalf("get task messages: %v", err)
	}
	var stored *types.TaskMessage
	for i := range msgs {
		if msgs[i].Role == "error" {
			if stored != nil {
				t.Fatal("agent error stored twice")
			}
			stored = &msgs[i]
		}
	}
	if stored == nil {
		t.Fatal("agent error was not stored")
	}
	if want := "[agent_error] provider overloaded"; stored.Content != want {
		t.Errorf("stored content = %q, want %q", stored.Content, want)
	}

	frame, found := findTaskStreamEvent(frames, "error")
	if !found {
		t.Fatal("no error frame broadcast")
	}
	if frame.Data["message_id"] != stored.ID {
		t.Errorf("frame message_id = %v, want stored id %s", frame.Data["message_id"], stored.ID)
	}
}
