package exec

import (
	"time"

	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/types"

	"mike/pkg/logging"

	"github.com/google/uuid"
)

// The kinds the frontend words and colours a card from. A new one needs a case in chat-entries.ts and a
// sentence in en.ts, spelled identically.
const (
	retryNoteKind        = "retry"
	memoryPauseNoteKind  = "memory_pressure"
	memoryResumeNoteKind = "memory_resumed"
	loopBreakerNoteKind  = "loop_breaker"
)

// systemNote is a note about the run in the task's own history. Role "system" is display-only:
// groupTaskMessages drops it when it rebuilds the conversation, so the model never reads one.
type systemNote struct {
	kind     string
	severity string // "" | "info" | "warning" | "error" — how the card is drawn
	content  string
}

// record returns the row id, or "" when the write failed. An empty thread is still where a task that has
// no thread shows its messages from, so a note can be written without one.
func (n systemNote) record(s *store.Store, taskID, threadID string) string {
	id := uuid.New().String()
	if err := s.CreateTaskMessage(&types.TaskMessage{
		ID:        id,
		TaskID:    taskID,
		ThreadID:  threadID,
		Role:      "system",
		Severity:  n.severity,
		Content:   n.content,
		Kind:      n.kind,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		logging.For(logging.Exec).With(logging.TaskID, taskID).Warnf("system note not stored (kind=%q): %v", n.kind, err)
		return ""
	}
	return id
}

// recordOnThread declines a note with no thread to carry it.
func (n systemNote) recordOnThread(s *store.Store, taskID, threadID string) string {
	if threadID == "" {
		return ""
	}
	return n.record(s, taskID, threadID)
}

// show sends the note to the task's clients, with `extra` merged into the flat frame. A caller holding no
// row id still sends: a client acting on the signal must not be left holding a dead stream.
func (n systemNote) show(asm *stream.AppStreamManager, projectID, taskID, msgID string, extra map[string]any) {
	data := map[string]any{
		"role":       "system",
		"content":    n.content,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}
	if msgID != "" {
		data["id"] = msgID
	}
	if n.kind != "" {
		data["kind"] = n.kind
	}
	if n.severity != "" {
		data["severity"] = n.severity
	}
	for k, v := range extra {
		data[k] = v
	}
	asm.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeHint, Data: data})
}
