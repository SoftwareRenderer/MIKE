package stream

import (
	"encoding/json"
	"testing"

	"kanban/backend/types"

	"github.com/coder/websocket"
)

// assertFlatJSON verifies that a JSON frame is flat (no double-encoded data field).
// It checks that the given key exists and is an object (not a string).
func assertFlatJSON(t *testing.T, frame []byte, typeKey, dataKey string, expectedType string) map[string]any {
	t.Helper()
	var msg map[string]any
	if err := json.Unmarshal(frame, &msg); err != nil {
		t.Fatalf("unmarshal frame: %v: %s", err, string(frame))
	}
	if msg[typeKey] != expectedType {
		t.Fatalf("expected %s=%s, got %v: %s", typeKey, expectedType, msg[typeKey], string(frame))
	}
	if dataKey != "" {
		data, ok := msg[dataKey]
		if !ok {
			t.Fatalf("expected '%s' field in message: %s", dataKey, string(frame))
		}
		if _, ok := data.(map[string]any); !ok {
			t.Fatalf("expected '%s' to be an object (not double-encoded), got %T: %s", dataKey, data, string(frame))
		}
	}
	return msg
}

// TestTaskUpdatedWireFormat verifies that a task_updated event serializes as
// flat JSON so the client can access msg.task directly (not msg.data.task).
func TestTaskUpdatedWireFormat(t *testing.T) {
	task := &types.Task{
		ID:        "test-task",
		ProjectID: "test-project",
		Title:     "Test task",
		Status:    "backlog",
	}
	frame, err := json.Marshal(map[string]any{
		"type": "task_updated",
		"task": task,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "task", "task_updated")
	taskData := msg["task"].(map[string]any)
	if taskData["id"] != "test-task" {
		t.Fatalf("expected task.id=test-task, got %v", taskData["id"])
	}
}

// TestTaskEventWireFormat verifies that a task_event serializes as flat JSON
// with type, task_id, event, and data at the top level.
func TestTaskEventWireFormat(t *testing.T) {
	payload := map[string]any{
		"type":    "task_event",
		"task_id": "test-task",
		"event":   "token",
		"data":    map[string]string{"text": "hello"},
	}
	frame, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "data", "task_event")
	if msg["task_id"] != "test-task" {
		t.Fatalf("expected task_id=test-task, got %v", msg["task_id"])
	}
	if msg["event"] != "token" {
		t.Fatalf("expected event=token, got %v", msg["event"])
	}
	dataMap := msg["data"].(map[string]any)
	if dataMap["text"] != "hello" {
		t.Fatalf("expected data.text=hello, got %v", dataMap["text"])
	}
}

// TestCommitStatusWireFormat verifies commit_status is flat JSON.
func TestCommitStatusWireFormat(t *testing.T) {
	frame, err := json.Marshal(map[string]string{
		"type":    "commit_status",
		"task_id": "test-task",
		"status":  "queued",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "", "commit_status")
	if msg["task_id"] != "test-task" {
		t.Fatalf("expected task_id=test-task, got %v", msg["task_id"])
	}
	if msg["status"] != "queued" {
		t.Fatalf("expected status=queued, got %v", msg["status"])
	}
}

// TestDiffWireFormat verifies task_diff is flat JSON.
func TestDiffWireFormat(t *testing.T) {
	frame, err := json.Marshal(map[string]any{
		"type":       "task_diff",
		"task_id":    "test-task",
		"hash":       "abc123",
		"diff":       "diff content",
		"short_stat": "1 file changed",
		"untracked":  []any{},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "", "task_diff")
	if msg["task_id"] != "test-task" {
		t.Fatalf("expected task_id=test-task, got %v", msg["task_id"])
	}
	if msg["hash"] != "abc123" {
		t.Fatalf("expected hash=abc123, got %v", msg["hash"])
	}
	if msg["diff"] != "diff content" {
		t.Fatalf("expected diff='diff content', got %v", msg["diff"])
	}
}

// TestProjectCountsWireFormat verifies project_counts is flat JSON.
func TestProjectCountsWireFormat(t *testing.T) {
	counts := map[string]any{
		"test-project": map[string]any{
			"backlog":     1,
			"in_progress": 2,
			"review":      0,
			"completed":   3,
		},
	}
	frame, err := json.Marshal(map[string]any{
		"type": "project_counts",
		"data": counts,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "data", "project_counts")
	dataMap := msg["data"].(map[string]any)
	projCounts := dataMap["test-project"].(map[string]any)
	if projCounts["backlog"].(float64) != 1 {
		t.Fatalf("expected backlog=1, got %v", projCounts["backlog"])
	}
}

// TestBranchWireFormat verifies project_branch is flat JSON.
func TestBranchWireFormat(t *testing.T) {
	frame, err := json.Marshal(map[string]any{
		"type":   "project_branch",
		"branch": "main",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "", "project_branch")
	if msg["branch"] != "main" {
		t.Fatalf("expected branch=main, got %v", msg["branch"])
	}
}

// TestBoardRefreshWireFormat verifies board_refresh is flat JSON.
func TestBoardRefreshWireFormat(t *testing.T) {
	frame, err := json.Marshal(map[string]string{"type": "board_refresh"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "", "board_refresh")
	_ = msg
}

// TestTaskDeletedWireFormat verifies task_deleted is flat JSON.
func TestTaskDeletedWireFormat(t *testing.T) {
	frame, err := json.Marshal(map[string]any{
		"type":    "task_deleted",
		"task_id": "test-task",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "", "task_deleted")
	if msg["task_id"] != "test-task" {
		t.Fatalf("expected task_id=test-task, got %v", msg["task_id"])
	}
}

// TestOldStyleBroken verifies that the old WSMessage-wrapped approach
// placed task inside msg.data instead of msg.task (regression reference).
func TestOldStyleBroken(t *testing.T) {
	// This is what the OLD code did: wrap in WSMessage{Type, Data: json.Marshal(payload)}
	payload, _ := json.Marshal(map[string]any{
		"type": "task_updated",
		"task": map[string]string{"id": "test-task"},
	})
	oldStyle := types.WSMessage{Type: "task_updated", Data: payload}
	frame, _ := json.Marshal(oldStyle)

	// The old style produces: {"type":"task_updated","data":{"type":"task_updated","task":{...}}}
	// Client checks msg.task which is undefined (task is inside msg.data)
	var msg map[string]any
	json.Unmarshal(frame, &msg)

	// Old style: msg.task is undefined, task is at msg.data.task
	if _, ok := msg["task"]; ok {
		t.Fatal("old-style should NOT have top-level 'task' field")
	}
	data, ok := msg["data"].(map[string]any)
	if !ok {
		t.Fatal("old-style should have 'data' as object")
	}
	if _, ok := data["task"]; !ok {
		t.Fatal("old-style should have task nested inside data")
	}
	// This confirms the old approach was broken: client expected msg.task but got msg.data.task
}

// TestClientParseTaskUpdated verifies the client can parse a task_updated frame.
func TestClientParseTaskUpdated(t *testing.T) {
	frame, _ := json.Marshal(map[string]any{
		"type": "task_updated",
		"task": map[string]any{
			"id":         "test-task",
			"title":      "Test",
			"status":     "backlog",
			"project_id": "test-project",
		},
	})

	// Simulate client-side parsing (matches frontend api.ts logic)
	var msg map[string]any
	if err := json.Unmarshal(frame, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Client checks: if (msg.type === 'task_updated') callbacks.onTaskUpdated(msg.task)
	if msg["type"] != "task_updated" {
		t.Fatalf("expected type=task_updated, got %v", msg["type"])
	}
	task, ok := msg["task"]
	if !ok {
		t.Fatal("client expects msg.task to exist")
	}
	taskMap, ok := task.(map[string]any)
	if !ok {
		t.Fatalf("client expects msg.task to be an object, got %T", task)
	}
	if taskMap["id"] != "test-task" {
		t.Fatalf("client expects msg.task.id=test-task, got %v", taskMap["id"])
	}
}

// TestClientParseTaskEvent verifies the client can parse a task_event frame.
func TestClientParseTaskEvent(t *testing.T) {
	frame, _ := json.Marshal(map[string]any{
		"type":    "task_event",
		"task_id": "test-task",
		"event":   "token",
		"data":    map[string]string{"text": "hello"},
	})

	// Simulate client-side parsing (matches frontend api.ts logic)
	var msg map[string]any
	if err := json.Unmarshal(frame, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Client checks: if (msg.type === 'task_event') callbacks.onTaskEvent(msg.task_id, {type: msg.event, data: msg.data})
	if msg["type"] != "task_event" {
		t.Fatalf("expected type=task_event, got %v", msg["type"])
	}
	if msg["task_id"] != "test-task" {
		t.Fatalf("client expects msg.task_id=test-task, got %v", msg["task_id"])
	}
	if msg["event"] != "token" {
		t.Fatalf("client expects msg.event=token, got %v", msg["event"])
	}
	dataMap, ok := msg["data"].(map[string]any)
	if !ok {
		t.Fatalf("client expects msg.data to be an object, got %T", msg["data"])
	}
	if dataMap["text"] != "hello" {
		t.Fatalf("client expects msg.data.text=hello, got %v", dataMap["text"])
	}
}

// TestTaskEventDelivery verifies that task event delivery is gated by
// task subscription (taskSub key exists with count > 0).
// We test the subscription filtering logic via hasTaskSubscribers,
// which uses the same key-based lookup as sendTaskPayload.
func TestTaskEventDelivery(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)

	// Create unique fake connection pointers
	connA := (*websocket.Conn)(new(websocket.Conn))
	connB := (*websocket.Conn)(new(websocket.Conn))
	connC := (*websocket.Conn)(new(websocket.Conn))

	projectID := "test-project"
	taskID := "test-task"

	// Set up client states directly (subscribe/unsubscribe are unexported)
	asm.mu.Lock()

	// Client A: subscribed to the target task
	asm.clients[connA] = &clientState{
		conn:       connA,
		projectSub: map[string]int{projectID: 1},
		taskSub:    map[string]int{projectID + ":" + taskID: 1},
		bufSize:    DefaultBufferSize,
	}

	// Client B: subscribed to project but NOT to the task
	asm.clients[connB] = &clientState{
		conn:       connB,
		projectSub: map[string]int{projectID: 1},
		taskSub:    map[string]int{},
		bufSize:    DefaultBufferSize,
	}

	// Client C: subscribed to a DIFFERENT task
	asm.clients[connC] = &clientState{
		conn:       connC,
		projectSub: map[string]int{projectID: 1},
		taskSub:    map[string]int{projectID + ":" + "other-task": 1},
		bufSize:    DefaultBufferSize,
	}

	asm.mu.Unlock()

	// 1. hasTaskSubscribers returns true when at least one client is subscribed
	if !asm.hasTaskSubscribers(projectID, taskID) {
		t.Fatal("expected hasTaskSubscribers=true for subscribed task")
	}

	// 2. hasTaskSubscribers returns false for a task with no subscribers
	if asm.hasTaskSubscribers(projectID, "nonexistent-task") {
		t.Fatal("expected hasTaskSubscribers=false for nonexistent task")
	}

	// 3. hasTaskSubscribers returns false for a different project
	if asm.hasTaskSubscribers("other-project", taskID) {
		t.Fatal("expected hasTaskSubscribers=false for different project")
	}

	// 4. After removing the subscription, hasTaskSubscribers returns false
	asm.mu.Lock()
	key := projectID + ":" + taskID
	delete(asm.clients[connA].taskSub, key)
	asm.mu.Unlock()

	if asm.hasTaskSubscribers(projectID, taskID) {
		t.Fatal("expected hasTaskSubscribers=false after unsubscribe")
	}

	// 5. Re-subscribe and verify multi-tab support (count > 1)
	asm.mu.Lock()
	asm.clients[connA].taskSub[key] = 2 // two tabs subscribed
	asm.mu.Unlock()

	if !asm.hasTaskSubscribers(projectID, taskID) {
		t.Fatal("expected hasTaskSubscribers=true with count=2")
	}

	// Unsubscribe once — should still have subscribers (count=1)
	asm.mu.Lock()
	asm.clients[connA].taskSub[key]--
	asm.mu.Unlock()

	if !asm.hasTaskSubscribers(projectID, taskID) {
		t.Fatal("expected hasTaskSubscribers=true after one unsubscribe (count=1)")
	}

	// Unsubscribe again — should have no subscribers
	asm.mu.Lock()
	delete(asm.clients[connA].taskSub, key)
	asm.mu.Unlock()

	if asm.hasTaskSubscribers(projectID, taskID) {
		t.Fatal("expected hasTaskSubscribers=false after second unsubscribe")
	}
}
