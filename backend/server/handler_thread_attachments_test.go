package server

// The fork of an edited message rewrites the row it copies, so the question and its pictures land together:
// the regenerate-send that follows finds that row by comparing both.

import (
	"net/http"
	"testing"

	"mike/backend/types"
)

// forkQuestion posts the fork an edited message makes and answers the question the new thread holds.
// A nil `attachments` is a request that says nothing about pictures; `[]` is an emptied tray.
func forkQuestion(t *testing.T, s *Server, projID, taskID, fromThread, fromMsg, newContent string, attachments any) *types.TaskMessage {
	t.Helper()
	body := map[string]any{
		"from_thread_id":  fromThread,
		"from_message_id": fromMsg,
		"new_content":     newContent,
	}
	if attachments != nil {
		body["attachments"] = attachments
	}
	rr := testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", body))
	if rr.Code != http.StatusCreated {
		t.Fatalf("fork: got %d, want 201: %s", rr.Code, rr.Body.String())
	}
	threadID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	id, err := s.store.GetLastUserMessageInThread(threadID)
	if err != nil || id == "" {
		t.Fatalf("the fork holds no user message (err=%v)", err)
	}
	question, err := s.store.GetTaskMessageByID(id)
	if err != nil {
		t.Fatalf("read the fork's question: %v", err)
	}
	return question
}

// mainThreadAndQuestion returns the task's own thread and the message it opens with.
func mainThreadAndQuestion(t *testing.T, s *Server, projID, taskID string) (string, string) {
	t.Helper()
	threads, err := s.store.GetThreadsByTask(taskID)
	if err != nil || len(threads) == 0 {
		t.Fatalf("the task has no thread to fork from (err=%v)", err)
	}
	main := threads[0].ID
	question, err := s.store.GetLastUserMessageInThread(main)
	if err != nil || question == "" {
		t.Fatalf("the thread has no opening question (err=%v)", err)
	}
	return main, question
}

// TestForkOfAnEditedMessageCarriesItsTray: a question rewritten into a new thread keeps the pictures the
// editor held. A fork that rewrote only the prose leaves the copied row matching neither half of the send
// that follows it, which then appends a second copy of the question.
func TestForkOfAnEditedMessageCarriesItsTray(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "the original question")
	main, question := mainThreadAndQuestion(t, s, projID, taskID)

	edited := forkQuestion(t, s, projID, taskID, main, question, "the edited question", editAttachmentJSON)
	if edited.Content != "the edited question" {
		t.Errorf("the fork's question = %q, want the edited text", edited.Content)
	}
	if edited.Attachments != editAttachmentJSON {
		t.Errorf("the fork's tray = %q, want the tray the edit carried", edited.Attachments)
	}
}

// TestForkOfAnEditedMessageClearsItsTrayOnlyWhenToldTo: an emptied tray is a decision and is stored as no
// tray, while a request that says nothing about pictures leaves the copied row's own.
func TestForkOfAnEditedMessageClearsItsTrayOnlyWhenToldTo(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "the original question")
	main, question := mainThreadAndQuestion(t, s, projID, taskID)
	tray := editAttachmentJSON
	if err := s.store.EditTaskMessage(question, nil, &tray); err != nil {
		t.Fatalf("give the question a tray: %v", err)
	}

	emptied := forkQuestion(t, s, projID, taskID, main, question, "no pictures please", "[]")
	if emptied.Attachments != "" {
		t.Errorf("an emptied tray left %q on the fork's question", emptied.Attachments)
	}

	untouched := forkQuestion(t, s, projID, taskID, main, question, "keep what was attached", nil)
	if untouched.Attachments != editAttachmentJSON {
		t.Errorf("a fork that said nothing about pictures moved them: %q", untouched.Attachments)
	}
}

// TestForkRefusesATrayThatIsNotAnArray: the tray is judged before the worktree is touched, so a refused fork
// changes nothing.
func TestForkRefusesATrayThatIsNotAnArray(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "the original question")
	main, question := mainThreadAndQuestion(t, s, projID, taskID)

	before, err := s.store.GetThreadsByTask(taskID)
	if err != nil {
		t.Fatalf("read the task's threads: %v", err)
	}

	rr := testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork",
		map[string]any{
			"from_thread_id":  main,
			"from_message_id": question,
			"new_content":     "edited",
			"attachments":     `{"mimeType":"image/png"}`,
		}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("an object where the array was expected: got %d, want 400: %s", rr.Code, rr.Body.String())
	}

	after, err := s.store.GetThreadsByTask(taskID)
	if err != nil {
		t.Fatalf("read the task's threads: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("a refused fork left %d threads where there were %d", len(after), len(before))
	}
	if row := storedMessage(t, s, question); row.Content != "the original question" {
		t.Errorf("a refused fork rewrote the question it was cut at: %q", row.Content)
	}
}
