package server

import (
	"net/http"
	"testing"
	"time"

	"mike/backend/types"
)

// An edited message arrives as one request that takes the thread back to the message being edited and then
// sends, so the edited row cannot sit on the thread and be read as a duplicate of the send carrying it.

// seedThread gives the task a thread to answer on and plants the rows a client edits from, oldest first.
// Timestamps are spaced a second apart so "the thread's last row" is never a guess.
func seedThread(t *testing.T, s *Server, projectID, taskID string, rows ...types.TaskMessage) string {
	t.Helper()
	threadID := taskID + "-thread"
	if err := s.store.CreateThread(&types.TaskThread{ID: threadID, TaskID: taskID, Name: "Main", IsActive: true}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	task, err := s.store.GetTask(projectID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.CurrentThreadID = threadID
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("activate thread: %v", err)
	}
	base := time.Now().UTC().Add(-time.Duration(len(rows)) * time.Minute)
	for i := range rows {
		m := rows[i]
		m.TaskID, m.ThreadID = taskID, threadID
		if m.CreatedAt.IsZero() {
			m.CreatedAt = base.Add(time.Duration(i) * time.Second)
		}
		if err := s.store.CreateTaskMessage(&m); err != nil {
			t.Fatalf("seed message %s: %v", m.ID, err)
		}
	}
	return threadID
}

// threadRows returns the thread's rows of one role, oldest first. Only the roles a send is about are read: a
// lazy start that cannot reach its LLM persists its own error row, and that lands whenever it likes.
func threadRows(t *testing.T, s *Server, taskID, threadID, role string) []types.TaskMessage {
	t.Helper()
	msgs, err := s.store.GetTaskMessages(taskID, threadID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	var kept []types.TaskMessage
	for _, m := range msgs {
		if m.Role == role {
			kept = append(kept, m)
		}
	}
	return kept
}

func sendBody(content string, extra map[string]any) map[string]any {
	body := map[string]any{"content": content}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestSendWithReplaceTruncatesAndSendsTheEditedRow(t *testing.T) {
	s := testServer(t)
	projectID := createTestProject(t, s)
	taskID := createTestTask(t, s, projectID, "edit and send")
	threadID := seedThread(t, s, projectID, taskID,
		types.TaskMessage{ID: "u-old", Role: "user", Content: "original question"},
		types.TaskMessage{ID: "a-old", Role: "assistant", Content: "an answer"},
	)
	t.Cleanup(func() { _ = s.executor.StopTask(taskID) })

	req := testRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/send",
		sendBody("asked again, differently", map[string]any{"replace_message_id": "u-old"}))
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	created := mustJSON[map[string]any](t, rr.Body)
	sentID, _ := created["id"].(string)
	if sentID == "u-old" {
		t.Errorf("the send reused the row it was told to replace — it was deduped and no turn will run")
	}

	users := threadRows(t, s, taskID, threadID, "user")
	if len(users) != 1 {
		t.Fatalf("user rows = %d, want exactly 1: %+v", len(users), users)
	}
	if users[0].Content != "asked again, differently" || users[0].ID != sentID {
		t.Errorf("the thread holds %+v, want the sent row %q", users[0], sentID)
	}
	// The answer that followed the edited question went with it: the thread is what the model reads.
	if got := len(threadRows(t, s, taskID, threadID, "assistant")); got != 0 {
		t.Errorf("%d assistant row(s) survived the replace", got)
	}
}

// A plain repeat is still refused: without this, the test above would only prove that nothing was checked.
func TestSendWithoutReplaceStillDedupesTheLastRow(t *testing.T) {
	s := testServer(t)
	projectID := createTestProject(t, s)
	taskID := createTestTask(t, s, projectID, "double submit")
	threadID := seedThread(t, s, projectID, taskID,
		types.TaskMessage{ID: "u-same", Role: "user", Content: "asked again, differently"},
	)

	req := testRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/send",
		sendBody("asked again, differently", nil))
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	if id, _ := mustJSON[map[string]any](t, rr.Body)["id"].(string); id != "u-same" {
		t.Errorf("a repeat must reuse the row it matched, answered with id %q", id)
	}
	if got := len(threadRows(t, s, taskID, threadID, "user")); got != 1 {
		t.Errorf("user rows = %d, want 1 — a dedup writes nothing", got)
	}
}

// A replace may drop what the agent wrote after the named row, but not a newer user question: truncating past
// one would delete turns the client never saw, which is what a queued edit flushed after a reconnect would do.
func TestSendWithReplaceRefusesAThreadThatMovedOn(t *testing.T) {
	s := testServer(t)
	projectID := createTestProject(t, s)
	taskID := createTestTask(t, s, projectID, "stale replace")
	threadID := seedThread(t, s, projectID, taskID,
		types.TaskMessage{ID: "u-old", Role: "user", Content: "original question"},
		types.TaskMessage{ID: "a-old", Role: "assistant", Content: "an answer"},
		types.TaskMessage{ID: "u-new", Role: "user", Content: "and now this"},
	)

	req := testRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/send",
		sendBody("edited late", map[string]any{"replace_message_id": "u-old"}))
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rr.Code, rr.Body.String())
	}
	if got := len(threadRows(t, s, taskID, threadID, "user")); got != 2 {
		t.Errorf("a refused replace took user rows with it: %d left, want 2", got)
	}
	if got := len(threadRows(t, s, taskID, threadID, "assistant")); got != 1 {
		t.Errorf("a refused replace took the answer row with it (%d left)", got)
	}
}
