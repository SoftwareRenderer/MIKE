package server

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mike/backend/httputil"
	"mike/backend/types"
)

// An edited message's pictures are stored by the same request that stores its new words.

const editAttachmentJSON = `[{"mimeType":"image/png","data":"aGVsbG8="}]`

// editMessage PATCHes one message and answers the recorder.
func editMessage(t *testing.T, s *Server, projectID, taskID, messageID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return testResponseAuth(t, s, testRequest(http.MethodPatch,
		"/api/projects/"+projectID+"/tasks/"+taskID+"/messages/"+messageID, body))
}

func storedMessage(t *testing.T, s *Server, messageID string) *types.TaskMessage {
	t.Helper()
	msg, err := s.store.GetTaskMessageByID(messageID)
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	return msg
}

// TestMessageEditWritesTheTrayWithTheText: a picture attached while editing a message is what the row holds
// afterwards, and each half is written only when the request names it.
func TestMessageEditWritesTheTrayWithTheText(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")
	msg := &types.TaskMessage{
		ID:        "msg-edit-tray",
		TaskID:    taskID,
		Role:      "user",
		Content:   "the original question",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	if rr := editMessage(t, s, projID, taskID, msg.ID, map[string]any{
		"content":     "the edited question",
		"attachments": editAttachmentJSON,
	}); rr.Code != http.StatusOK {
		t.Fatalf("edit with a tray: got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	got := storedMessage(t, s, msg.ID)
	if got.Content != "the edited question" {
		t.Errorf("content = %q, want the edited text", got.Content)
	}
	if got.Attachments != editAttachmentJSON {
		t.Errorf("attachments = %q, want the tray the edit carried", got.Attachments)
	}

	// A field the request does not name is a field the edit is not about.
	if rr := editMessage(t, s, projID, taskID, msg.ID, map[string]any{"content": "words only"}); rr.Code != http.StatusOK {
		t.Fatalf("text-only edit: got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if got = storedMessage(t, s, msg.ID); got.Attachments != editAttachmentJSON {
		t.Errorf("a text-only edit moved the tray: %q", got.Attachments)
	}
	if got.Content != "words only" {
		t.Errorf("content = %q, want the new words", got.Content)
	}

	// An emptied tray is a decision, so it arrives — and it is stored as no tray, the shape a message
	// without pictures has from the moment it is written.
	if rr := editMessage(t, s, projID, taskID, msg.ID, map[string]any{"attachments": "[]"}); rr.Code != http.StatusOK {
		t.Fatalf("clearing edit: got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if got = storedMessage(t, s, msg.ID); got.Attachments != "" {
		t.Errorf("an emptied tray left %q on the row", got.Attachments)
	}
	if got.Content != "words only" {
		t.Errorf("clearing the tray moved the text: %q", got.Content)
	}
}

// TestMessageEditRefusesATrayThatIsNotAnArray keeps a hand-written request from writing a value the
// transcript cannot read back: the column holds the JSON array the tray sent, or nothing.
func TestMessageEditRefusesATrayThatIsNotAnArray(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")
	msg := &types.TaskMessage{
		ID:          "msg-edit-tray-malformed",
		TaskID:      taskID,
		Role:        "user",
		Content:     "question",
		Attachments: editAttachmentJSON,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.store.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	for _, malformed := range []string{
		"not json",                 // prose where the array was expected
		`{"mimeType":"image/png"}`, // an object, not the array
		"null",                     // decodes, but is not an array either
		`"aGVsbG8="`,               // a value that stopped at the picture
		`7`,                        // not even text
	} {
		if rr := editMessage(t, s, projID, taskID, msg.ID, map[string]any{"attachments": malformed}); rr.Code != http.StatusBadRequest {
			t.Errorf("attachments %q: got %d, want 400: %s", malformed, rr.Code, rr.Body.String())
		}
		if got := storedMessage(t, s, msg.ID); got.Attachments != editAttachmentJSON || got.Content != "question" {
			t.Fatalf("attachments %q was refused and written anyway: %q / %q", malformed, got.Content, got.Attachments)
		}
	}
}

// TestMessageEditReadsAtTheMessageBound: why this route decodes where a send does. Four pictures at the
// browser's intake cap are ~107 MB of base64, and an edit that only rearranges them must arrive.
func TestMessageEditReadsAtTheMessageBound(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")
	msg := &types.TaskMessage{
		ID:        "msg-edit-tray-large",
		TaskID:    taskID,
		Role:      "user",
		Content:   "question",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	pad := strings.Repeat("A", int(httputil.MaxRequestBodyBytes)+(1<<20))
	tray := `[{"mimeType":"image/png","data":"` + pad + `"}]`
	body, err := json.Marshal(map[string]string{"attachments": tray})
	if err != nil {
		t.Fatalf("build the body: %v", err)
	}
	req := secureRequest(httptest.NewRequest(http.MethodPatch,
		"/api/projects/"+projID+"/tasks/"+taskID+"/messages/"+msg.ID, bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusOK {
		t.Fatalf("a tray over the general bound: got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if got := storedMessage(t, s, msg.ID); got.Attachments != tray {
		t.Errorf("the large tray was not stored whole: %d bytes on the row, %d sent", len(got.Attachments), len(tray))
	}
}
