package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
)

// Editing the summarized context goes through the one message PATCH: the summary is a task message
// whose role is "summarization", and what it says is what the task's system prompt carries.
//
// A blank content is refused for every role, not only for a summary. It is the row's existence that
// makes buildLLMContext drop everything before it, so an empty summary would erase the conversation
// from the model's context rather than restore it, and a PATCH is not the way to ask for that.

func TestSummaryEditStoresNewText(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")
	msgID := createSummaryMessage(t, s, taskID, "the old summary")

	req := testRequest(http.MethodPatch, "/api/projects/"+projID+"/tasks/"+taskID+"/messages/"+msgID,
		map[string]any{"content": "the edited summary"})
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusOK {
		t.Fatalf("edit summary: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	msg, err := s.store.GetTaskMessageByID(msgID)
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	if msg.Content != "the edited summary" {
		t.Fatalf("stored content = %q, want the edited text", msg.Content)
	}
}

func TestSummaryEditRefusesBlankContent(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")
	msgID := createSummaryMessage(t, s, taskID, "the summary in effect")

	for _, blank := range []string{"", "   ", "\n\t "} {
		req := testRequest(http.MethodPatch, "/api/projects/"+projID+"/tasks/"+taskID+"/messages/"+msgID,
			map[string]any{"content": blank})
		rr := testResponseAuth(t, s, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("content %q: got %d, want 400: %s", blank, rr.Code, rr.Body.String())
		}
		msg, err := s.store.GetTaskMessageByID(msgID)
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		if msg.Content != "the summary in effect" {
			t.Fatalf("content %q was refused but stored anyway: %q", blank, msg.Content)
		}
	}

	// A request that names neither field writes nothing, and a wrong type is a 400 naming it
	// rather than a zero value written into the row.
	req := testRequest(http.MethodPatch, "/api/projects/"+projID+"/tasks/"+taskID+"/messages/"+msgID,
		map[string]any{"content": 42})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("numeric content: got %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "content") {
		t.Errorf("a wrong type answered %q, which names nothing", rr.Body.String())
	}
}

// createSummaryMessage stores a summarization row in the task's current thread — the row a running
// task reads back into its system prompt — and returns its id.
func createSummaryMessage(t *testing.T, s *Server, taskID, content string) string {
	t.Helper()
	task, err := s.store.GetTaskByID(taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	msg := &types.TaskMessage{
		ID:        "msg-summary-" + taskID,
		TaskID:    taskID,
		ThreadID:  task.CurrentThreadID,
		Role:      "summarization",
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create summary message: %v", err)
	}
	return msg.ID
}
