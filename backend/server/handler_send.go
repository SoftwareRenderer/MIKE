package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"mike/backend/errs"
	"mike/backend/httputil"
	"mike/backend/store"
	"mike/pkg/logging"
)

// errReplacedElsewhere refuses a replace whose row is no longer the thread's last user message, because
// truncating then would delete turns the client never saw.
var errReplacedElsewhere = errs.Conflict("that message is no longer the last one in the conversation")

// handleTaskSend answers POST …/tasks/{taskID}/send: truncate the thread the message lands in, then hand the
// body to the executor's send handler. One request rather than delete-then-send, because a thread ending in the
// text being sent is what the duplicate-send check refuses; the truncate sits here rather than in exec because
// releasing those turns' snapshots and fork threads is worktree work this package owns.
func (s *Server) handleTaskSend(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")

	// Read the body to see the replace field, then hand the same bytes on; every other field stays the
	// executor's to parse. One byte past the cap leaves the size limit to the delegate.
	raw, err := io.ReadAll(io.LimitReader(r.Body, httputil.MaxRequestBodyBytes+1))
	if err != nil {
		Fail(w, err, "read request", "request body unreadable")
		return
	}
	var peek struct {
		ReplaceMessageID string `json:"replace_message_id"`
		ClientToken      string `json:"client_token"`
	}
	// A body that is not JSON is the delegate's to reject.
	_ = json.Unmarshal(raw, &peek)

	// Asked here, before the truncate: the first arrival made the row this request names no longer the thread's
	// last, so the truncate would refuse a replay for the change it caused.
	if id, claimed := s.store.ClaimedWrite(peek.ClientToken, taskID); claimed && s.answerClaimedSend(w, id) {
		return
	}

	if peek.ReplaceMessageID != "" && !s.truncateForReplace(w, projectID, taskID, peek.ReplaceMessageID) {
		return
	}

	r.Body = io.NopCloser(bytes.NewReader(raw))
	s.executor.HandleSend(w, r)
}

// answerClaimedSend answers a send the server already wrote, and reports whether it answered. A row that cannot
// be read is one the conversation no longer holds, so the send goes through as the new message it is.
func (s *Server) answerClaimedSend(w http.ResponseWriter, messageID string) bool {
	msg, err := s.store.GetTaskMessageByID(messageID)
	if err != nil {
		if !errors.Is(err, store.ErrMessageNotFound) {
			logging.For(logging.HTTP).Warnf("claimed send row %s unreadable: %v", messageID, err)
		}
		return false
	}
	result := map[string]any{"id": msg.ID, "role": msg.Role, "content": msg.Content}
	if msg.ThreadID != "" {
		result["thread_id"] = msg.ThreadID
	}
	JSONResponse(w, http.StatusCreated, result)
	return true
}

// truncateForReplace takes the thread back to the message the client is editing, answering the failure itself
// and reporting false. The named row must be the active thread's last user row: a newer user row means the
// conversation moved somewhere this click cannot speak for.
func (s *Server) truncateForReplace(w http.ResponseWriter, projectID, taskID, messageID string) bool {
	msg, ok := s.messageInTask(w, projectID, taskID, messageID)
	if !ok {
		return false
	}
	task, ok := s.taskInPath(w, projectID, taskID)
	if !ok {
		return false
	}
	lastUserID, err := s.store.GetLastUserMessageInThread(task.CurrentThreadID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		Fail(w, err, "read thread", "thread not found")
		return false
	}
	// A thread with no user row reports ErrNoRows and answers "", which nothing can replace.
	if msg.ThreadID != task.CurrentThreadID || lastUserID != msg.ID {
		Fail(w, errReplacedElsewhere, "replace message", errReplacedElsewhere.Error())
		return false
	}
	// Not a regenerate: a new message is arriving, so the executor is woken and the forks whose checkpoint
	// rows are being deleted are cleaned up.
	if err := s.truncateMessagesFrom(projectID, taskID, msg, false); err != nil {
		Fail(w, err, "delete messages", "message not found")
		return false
	}
	return true
}
