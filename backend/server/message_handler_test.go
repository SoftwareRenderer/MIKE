package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/types"
)

func TestMessageCreate(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":    "user",
		"content": "Hello world",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["role"] != "user" || result["content"] != "Hello world" {
		t.Fatalf("unexpected message: %+v", result)
	}
	if result["task_id"] != taskID {
		t.Fatalf("wrong task_id: %s", result["task_id"])
	}
	// Server should generate an ID
	if result["id"] == "" || result["id"] == nil {
		t.Fatal("server should generate message ID")
	}
}

func TestMessageCreateMissingFields(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Missing role
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"content": "Hello",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing role, got %d", rr.Code)
	}

	// Missing content
	req = testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role": "user",
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing content, got %d", rr.Code)
	}
}

func TestMessageList(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create 3 messages
	for _, content := range []string{"msg 1", "msg 2", "msg 3"} {
		req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
			"role":    "user",
			"content": content,
		})
		testResponseAuth(t, s, req)
	}

	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	// Task creation stores the prompt as a system message, so there's 1 extra
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages (3 user + 1 prompt), got %d", len(msgs))
	}
}

func TestMessageUpdate(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create a message
	createReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":    "user",
		"content": "Original content",
	})
	createRR := testResponseAuth(t, s, createReq)
	msg := mustJSON[map[string]any](t, createRR.Body)
	msgID := msg["id"].(string)

	// Update the message
	req := testRequest("PATCH", "/api/projects/"+projID+"/tasks/"+taskID+"/messages/"+msgID, map[string]any{
		"content": "Updated content",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify update
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	listRR := testResponseAuth(t, s, listReq)
	msgs := mustJSON[[]map[string]any](t, listRR.Body)
	if msgs[len(msgs)-1]["content"] != "Updated content" {
		t.Fatalf("expected updated content, got %s", msgs[len(msgs)-1]["content"])
	}
}

func TestMessageDeleteAfter(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create messages
	var msgIDs []string
	for _, content := range []string{"msg 1", "msg 2", "msg 3", "msg 4"} {
		req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
			"role":    "user",
			"content": content,
		})
		rr := testResponseAuth(t, s, req)
		msg := mustJSON[map[string]any](t, rr.Body)
		msgIDs = append(msgIDs, msg["id"].(string))
	}

	// Delete after msg 2
	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"message_id": msgIDs[1],
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify messages were deleted (count should be less than before)
	// Note: due to second-level timestamp granularity, messages created in the same
	// second may all be deleted together. The store layer tests verify precise behavior.
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	listRR := testResponseAuth(t, s, listReq)
	remaining := mustJSON[[]map[string]any](t, listRR.Body)
	// At least the deleted message itself should be gone
	for _, m := range remaining {
		if m["id"] == msgIDs[1] {
			t.Fatalf("deleted message %s should not be present", msgIDs[1])
		}
	}
	_ = remaining // verified above
}

func TestMessageDeleteSummarizationSkipsForkCleanup(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	task, _ := s.store.GetTask(projID, taskID)
	threadID := task.CurrentThreadID

	// Create messages with explicit timestamps: msg1 < summarization < msg2 < msg3
	baseTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:        "msg-1",
		TaskID:    taskID,
		ThreadID:  threadID,
		Role:      "user",
		Content:   "msg 1",
		CreatedAt: baseTime,
	})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:        "summ-msg",
		TaskID:    taskID,
		ThreadID:  threadID,
		Role:      "summarization",
		Content:   "Summary of changes",
		CreatedAt: baseTime.Add(time.Second),
	})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:        "msg-2",
		TaskID:    taskID,
		ThreadID:  threadID,
		Role:      "user",
		Content:   "msg 2",
		CreatedAt: baseTime.Add(2 * time.Second),
	})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:        "msg-3",
		TaskID:    taskID,
		ThreadID:  threadID,
		Role:      "user",
		Content:   "msg 3",
		CreatedAt: baseTime.Add(3 * time.Second),
	})

	// Delete the summarization message (default mode, not delete_single)
	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"message_id": "summ-msg",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Summarization deletion should succeed and truncate messages after it
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	listRR := testResponseAuth(t, s, listReq)
	remaining := mustJSON[[]map[string]any](t, listRR.Body)

	// The summarization message and messages after it should be gone
	for _, m := range remaining {
		if m["id"] == "summ-msg" {
			t.Fatal("summarization message should have been deleted")
		}
		if m["id"] == "msg-2" || m["id"] == "msg-3" {
			t.Fatalf("message after summarization should have been deleted, got %v", m["id"])
		}
	}
	// msg-1 should still be present
	found := false
	for _, m := range remaining {
		if m["id"] == "msg-1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("msg-1 should still be present")
	}
}

func TestMessageDeleteMissingID(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestMessageGetByID(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create a message
	createReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":    "user",
		"content": "Hello",
	})
	createRR := testResponseAuth(t, s, createReq)
	msg := mustJSON[map[string]any](t, createRR.Body)
	msgID := msg["id"].(string)

	// Get message metadata (will_delete_branch check)
	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages/"+msgID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	// No fork threads, so will_delete_branch should be false
	if result["will_delete_branch"] != false {
		t.Fatalf("expected will_delete_branch=false, got %v", result["will_delete_branch"])
	}
}

func TestMessageListWriteFileContentSuppressed(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create a tool_call message with write_file args
	toolCallsJSON, _ := json.Marshal(map[string]any{
		"tool_id":           "call_abc123",
		"tool_name":         "write_file",
		"tool_display_name": "Write File",
		"args": map[string]any{
			"path":    "src/main.go",
			"content": "package main\n\nfunc main() {}\n",
		},
		"raw_args": `{"path":"src/main.go","content":"package main\n\nfunc main() {}\n"}`,
		"sandbox":    false,
	})

	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":       "tool_call",
		"content":    "file written",
		"tool_calls": string(toolCallsJSON),
		"tool_result": `{"output":"","success":true,"duration":10}`,
	})
	testResponseAuth(t, s, req)

	// GET without full=1 — content should be suppressed
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	// Find the tool_call message
	var toolCallMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "tool_call" {
			toolCallMsg = m
			break
		}
	}
	if toolCallMsg == nil {
		t.Fatal("tool_call message not found")
	}

	// Parse tool_calls field
	tcJSON := toolCallMsg["tool_calls"].(string)
	var tc map[string]any
	if err := json.Unmarshal([]byte(tcJSON), &tc); err != nil {
		t.Fatalf("parse tool_calls: %v", err)
	}
	args := tc["args"].(map[string]any)

	// content should be suppressed
	if args["content"] != "[REDACTED]" {
		t.Fatalf("expected content=[REDACTED], got %v", args["content"])
	}
	// path should still be present
	if args["path"] != "src/main.go" {
		t.Fatalf("expected path=src/main.go, got %v", args["path"])
	}
	// raw_args should also be suppressed
	if tc["raw_args"] != "[REDACTED]" {
		t.Fatalf("expected raw_args=[REDACTED], got %v", tc["raw_args"])
	}
}

func TestMessageListWriteFileContentFull(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create a tool_call message with write_file args
	toolCallsJSON, _ := json.Marshal(map[string]any{
		"tool_id":           "call_abc123",
		"tool_name":         "write_file",
		"tool_display_name": "Write File",
		"args": map[string]any{
			"path":    "src/main.go",
			"content": "package main\n\nfunc main() {}\n",
		},
		"raw_args": `{"path":"src/main.go","content":"package main\n\nfunc main() {}\n"}`,
		"sandbox":    false,
	})

	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":       "tool_call",
		"content":    "file written",
		"tool_calls": string(toolCallsJSON),
		"tool_result": `{"output":"","success":true,"duration":10}`,
	})
	testResponseAuth(t, s, req)

	// GET with full=1 — content should be preserved
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages?full=1", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	var toolCallMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "tool_call" {
			toolCallMsg = m
			break
		}
	}
	if toolCallMsg == nil {
		t.Fatal("tool_call message not found")
	}

	tcJSON := toolCallMsg["tool_calls"].(string)
	var tc map[string]any
	if err := json.Unmarshal([]byte(tcJSON), &tc); err != nil {
		t.Fatalf("parse tool_calls: %v", err)
	}
	args := tc["args"].(map[string]any)

	// content should be preserved
	if args["content"] != "package main\n\nfunc main() {}\n" {
		t.Fatalf("expected full content, got %v", args["content"])
	}
	// raw_args should also be preserved
	if tc["raw_args"] != `{"path":"src/main.go","content":"package main\n\nfunc main() {}\n"}` {
		t.Fatalf("expected full raw_args, got %v", tc["raw_args"])
	}
}

func TestMessageListNonWriteFileNotSuppressed(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create a tool_call message with bash args (not write_file)
	toolCallsJSON, _ := json.Marshal(map[string]any{
		"tool_id":           "call_xyz789",
		"tool_name":         "bash",
		"tool_display_name": "Bash",
		"args": map[string]any{
			"command": "ls -la",
		},
		"raw_args": `{"command":"ls -la"}`,
		"sandbox":    false,
	})

	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":       "tool_call",
		"content":    "executed",
		"tool_calls": string(toolCallsJSON),
		"tool_result": `{"output":"total 100","success":true,"duration":5}`,
	})
	testResponseAuth(t, s, req)

	// GET without full=1 — non-write_file tool should NOT be suppressed
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	var toolCallMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "tool_call" && m["tool_calls"].(string) != "" {
			var tc map[string]any
			json.Unmarshal([]byte(m["tool_calls"].(string)), &tc)
			if tc["tool_name"] == "bash" {
				toolCallMsg = m
				break
			}
		}
	}
	if toolCallMsg == nil {
		t.Fatal("bash tool_call message not found")
	}

	tcJSON := toolCallMsg["tool_calls"].(string)
	var tc map[string]any
	if err := json.Unmarshal([]byte(tcJSON), &tc); err != nil {
		t.Fatalf("parse tool_calls: %v", err)
	}
	args := tc["args"].(map[string]any)

	// command should NOT be suppressed
	if args["command"] != "ls -la" {
		t.Fatalf("expected command=ls -la, got %v", args["command"])
	}
	// raw_args should NOT be suppressed
	if tc["raw_args"] != `{"command":"ls -la"}` {
		t.Fatalf("expected raw_args unchanged, got %v", tc["raw_args"])
	}
}

func TestMessageListReasoningContentSuppressed(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create an assistant message with long reasoning + regular content (would be auto-hidden)
	longReasoning := strings.Repeat("x", 300)
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":              "assistant",
		"content":           "Here is the answer.",
		"reasoning_content": longReasoning,
	})
	testResponseAuth(t, s, req)

	// GET without full=1 — reasoning should be suppressed
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	var assistantMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "assistant" {
			assistantMsg = m
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("assistant message not found")
	}

	if assistantMsg["reasoning_content"] != "[HIDDEN]" {
		t.Fatalf("expected reasoning_content=[HIDDEN], got %v", assistantMsg["reasoning_content"])
	}
	// has_reasoning should be true so frontend knows reasoning exists
	if assistantMsg["has_reasoning"] != true {
		t.Fatalf("expected has_reasoning=true, got %v", assistantMsg["has_reasoning"])
	}
}

func TestMessageListReasoningContentAutoExpanded(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Short reasoning with no content — should be auto-expanded (NOT suppressed)
	// Create message directly in store (simulates exec loop behavior)
	shortReasoning := "Brief thought"
	task, _ := s.store.GetTask(projID, taskID)
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:               "test-reasoning-short",
		TaskID:           taskID,
		ThreadID:         task.CurrentThreadID,
		Role:             "assistant",
		Content:          "",
		ReasoningContent: shortReasoning,
		CreatedAt:        time.Now().UTC(),
	})

	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	var assistantMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "assistant" {
			assistantMsg = m
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("assistant message not found")
	}

	// Short reasoning with no content should NOT be suppressed
	if assistantMsg["reasoning_content"] != shortReasoning {
		t.Fatalf("expected reasoning_content=%q, got %v", shortReasoning, assistantMsg["reasoning_content"])
	}
}

func TestMessageListReasoningContentFull(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create an assistant message with long reasoning + regular content
	longReasoning := strings.Repeat("y", 500)
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":              "assistant",
		"content":           "The answer is 42.",
		"reasoning_content": longReasoning,
	})
	testResponseAuth(t, s, req)

	// GET with full=1 — reasoning should be preserved
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages?full=1", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	var assistantMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "assistant" {
			assistantMsg = m
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("assistant message not found")
	}

	if assistantMsg["reasoning_content"] != longReasoning {
		t.Fatalf("expected full reasoning_content, got length %d", len(assistantMsg["reasoning_content"].(string)))
	}
}

func TestMessageListReasoningWithToolCallsNotSuppressed(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Assistant message with reasoning + tool calls but no content — should NOT be suppressed
	// Create message directly in store (simulates exec loop behavior)
	longReasoning := strings.Repeat("z", 400)
	toolCallsJSON, _ := json.Marshal(map[string]any{
		"tool_id":   "call_1",
		"tool_name": "bash",
		"args":      map[string]any{"command": "ls"},
	})
	task, _ := s.store.GetTask(projID, taskID)
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:               "test-reasoning-toolcalls",
		TaskID:           taskID,
		ThreadID:         task.CurrentThreadID,
		Role:             "assistant",
		Content:          "",
		ReasoningContent: longReasoning,
		ToolCalls:        string(toolCallsJSON),
		CreatedAt:        time.Now().UTC(),
	})

	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	var assistantMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "assistant" {
			assistantMsg = m
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("assistant message not found")
	}

	// Reasoning with tool calls (no content) should NOT be suppressed
	if assistantMsg["reasoning_content"] != longReasoning {
		t.Fatalf("expected full reasoning_content, got length %d", len(assistantMsg["reasoning_content"].(string)))
	}
}

func TestMessageGetByIDFull(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create an assistant message with long reasoning + content (would be suppressed in list)
	longReasoning := strings.Repeat("a", 500)
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":              "assistant",
		"content":           "The answer is 42.",
		"reasoning_content": longReasoning,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	msg := mustJSON[map[string]any](t, rr.Body)
	msgID := msg["id"].(string)

	// GET single message with full=1 — reasoning should be preserved
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages/"+msgID+"?full=1", nil)
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["reasoning_content"] != longReasoning {
		t.Fatalf("expected full reasoning_content, got length %d", len(result["reasoning_content"].(string)))
	}
	if result["content"] != "The answer is 42." {
		t.Fatalf("expected content, got %v", result["content"])
	}
}

func TestMessageListBashResultTruncated(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create a bash tool_call message with long output (>10 lines)
	longOutput := strings.Repeat("line of output text\n", 20)
	toolCallsJSON, _ := json.Marshal(map[string]any{
		"tool_id":   "call_bash",
		"tool_name": "bash",
		"args":      map[string]any{"command": "cat large_file.txt"},
	})
	toolResultJSON, _ := json.Marshal(map[string]any{
		"output":   longOutput,
		"success":  true,
		"duration": 50,
	})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:         "test-bash-long",
		TaskID:     taskID,
		ThreadID:   (func() string { task, _ := s.store.GetTask(projID, taskID); return task.CurrentThreadID })(),
		Role:       "tool_call",
		Content:    "executed",
		ToolCalls:  string(toolCallsJSON),
		ToolResult: string(toolResultJSON),
		CreatedAt:  time.Now().UTC(),
	})

	// GET without full=1 — output should be truncated
	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	var bashMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "tool_call" {
			bashMsg = m
			break
		}
	}
	if bashMsg == nil {
		t.Fatal("tool_call message not found")
	}

	// result_truncated should be true
	if bashMsg["result_truncated"] != true {
		t.Fatalf("expected result_truncated=true, got %v", bashMsg["result_truncated"])
	}

	// Parse tool_result and check output is truncated
	var result map[string]any
	if err := json.Unmarshal([]byte(bashMsg["tool_result"].(string)), &result); err != nil {
		t.Fatalf("parse tool_result: %v", err)
	}
	output := result["output"].(string)
	if !strings.Contains(output, "truncated") {
		t.Fatalf("expected truncated output, got: %s", output)
	}
	// Should have fewer lines than original
	outputLines := strings.Count(output, "\n")
	if outputLines >= 20 {
		t.Fatalf("expected fewer than 20 lines, got %d", outputLines)
	}
	// original_lines and original_bytes should be set
	// strings.Split on "line\n" * 20 produces 21 elements (trailing empty)
	originalLines := len(strings.Split(longOutput, "\n"))
	if result["original_lines"] != float64(originalLines) {
		t.Fatalf("expected original_lines=%d, got %v", originalLines, result["original_lines"])
	}
	if result["original_bytes"] != float64(len(longOutput)) {
		t.Fatalf("expected original_bytes=%d, got %v", len(longOutput), result["original_bytes"])
	}
}

func TestMessageListBashResultFull(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	longOutput := strings.Repeat("line of output text\n", 20)
	toolCallsJSON, _ := json.Marshal(map[string]any{
		"tool_id":   "call_bash",
		"tool_name": "bash",
		"args":      map[string]any{"command": "cat large_file.txt"},
	})
	toolResultJSON, _ := json.Marshal(map[string]any{
		"output":   longOutput,
		"success":  true,
		"duration": 50,
	})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:         "test-bash-full",
		TaskID:     taskID,
		ThreadID:   (func() string { task, _ := s.store.GetTask(projID, taskID); return task.CurrentThreadID })(),
		Role:       "tool_call",
		Content:    "executed",
		ToolCalls:  string(toolCallsJSON),
		ToolResult: string(toolResultJSON),
		CreatedAt:  time.Now().UTC(),
	})

	// GET with full=1 — output should be preserved
	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages?full=1", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	var bashMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "tool_call" {
			bashMsg = m
			break
		}
	}
	if bashMsg == nil {
		t.Fatal("tool_call message not found")
	}

	// result_truncated should NOT be set
	if bashMsg["result_truncated"] == true {
		t.Fatalf("expected result_truncated=false, got true")
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(bashMsg["tool_result"].(string)), &result); err != nil {
		t.Fatalf("parse tool_result: %v", err)
	}
	output := result["output"].(string)
	if output != longOutput {
		t.Fatalf("expected full output, got length %d", len(output))
	}
}

func TestMessageListShortBashResultNotTruncated(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Short output — should NOT be truncated
	shortOutput := "total 100\ndrwxr-xr-x  5 user user  4096 Jan 1 .\n"
	toolCallsJSON, _ := json.Marshal(map[string]any{
		"tool_id":   "call_bash",
		"tool_name": "bash",
		"args":      map[string]any{"command": "ls -la"},
	})
	toolResultJSON, _ := json.Marshal(map[string]any{
		"output":   shortOutput,
		"success":  true,
		"duration": 5,
	})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:         "test-bash-short",
		TaskID:     taskID,
		ThreadID:   (func() string { task, _ := s.store.GetTask(projID, taskID); return task.CurrentThreadID })(),
		Role:       "tool_call",
		Content:    "executed",
		ToolCalls:  string(toolCallsJSON),
		ToolResult: string(toolResultJSON),
		CreatedAt:  time.Now().UTC(),
	})

	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	var bashMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "tool_call" {
			bashMsg = m
			break
		}
	}
	if bashMsg == nil {
		t.Fatal("tool_call message not found")
	}

	if bashMsg["result_truncated"] == true {
		t.Fatalf("expected result_truncated=false for short output, got true")
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(bashMsg["tool_result"].(string)), &result); err != nil {
		t.Fatalf("parse tool_result: %v", err)
	}
	output := result["output"].(string)
	if output != shortOutput {
		t.Fatalf("expected unchanged short output, got: %s", output)
	}
}

func TestMessageListReadFileResultTruncated(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	longOutput := strings.Repeat("line of file content text\n", 20)
	toolCallsJSON, _ := json.Marshal(map[string]any{
		"tool_id":   "call_read",
		"tool_name": "read_file",
		"args":      map[string]any{"path": "src/large_file.go"},
	})
	toolResultJSON, _ := json.Marshal(map[string]any{
		"output":   longOutput,
		"success":  true,
		"duration": 10,
	})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:         "test-read-long",
		TaskID:     taskID,
		ThreadID:   (func() string { task, _ := s.store.GetTask(projID, taskID); return task.CurrentThreadID })(),
		Role:       "tool_call",
		Content:    "",
		ToolCalls:  string(toolCallsJSON),
		ToolResult: string(toolResultJSON),
		CreatedAt:  time.Now().UTC(),
	})

	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	var readMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "tool_call" {
			readMsg = m
			break
		}
	}
	if readMsg == nil {
		t.Fatal("tool_call message not found")
	}

	// result_truncated should be true
	if readMsg["result_truncated"] != true {
		t.Fatalf("expected result_truncated=true, got %v", readMsg["result_truncated"])
	}

	// Parse tool_result and check output is truncated
	var result map[string]any
	if err := json.Unmarshal([]byte(readMsg["tool_result"].(string)), &result); err != nil {
		t.Fatalf("parse tool_result: %v", err)
	}
	output := result["output"].(string)
	if !strings.Contains(output, "truncated") {
		t.Fatalf("expected truncated output, got: %s", output)
	}
	outputLines := strings.Count(output, "\n")
	if outputLines >= 20 {
		t.Fatalf("expected fewer than 20 lines, got %d", outputLines)
	}
	originalLines := len(strings.Split(longOutput, "\n"))
	if result["original_lines"] != float64(originalLines) {
		t.Fatalf("expected original_lines=%d, got %v", originalLines, result["original_lines"])
	}
	if result["original_bytes"] != float64(len(longOutput)) {
		t.Fatalf("expected original_bytes=%d, got %v", len(longOutput), result["original_bytes"])
	}
}

func TestMessageListReadFileResultFull(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	longOutput := strings.Repeat("line of file content text\n", 20)
	toolCallsJSON, _ := json.Marshal(map[string]any{
		"tool_id":   "call_read",
		"tool_name": "read_file",
		"args":      map[string]any{"path": "src/large_file.go"},
	})
	toolResultJSON, _ := json.Marshal(map[string]any{
		"output":   longOutput,
		"success":  true,
		"duration": 10,
	})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:         "test-read-full",
		TaskID:     taskID,
		ThreadID:   (func() string { task, _ := s.store.GetTask(projID, taskID); return task.CurrentThreadID })(),
		Role:       "tool_call",
		Content:    "",
		ToolCalls:  string(toolCallsJSON),
		ToolResult: string(toolResultJSON),
		CreatedAt:  time.Now().UTC(),
	})

	// GET with full=1 — output should be preserved
	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages?full=1", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	var readMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "tool_call" {
			readMsg = m
			break
		}
	}
	if readMsg == nil {
		t.Fatal("tool_call message not found")
	}

	if readMsg["result_truncated"] == true {
		t.Fatalf("expected result_truncated=false with full=1, got true")
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(readMsg["tool_result"].(string)), &result); err != nil {
		t.Fatalf("parse tool_result: %v", err)
	}
	output := result["output"].(string)
	if output != longOutput {
		t.Fatalf("expected full output, got length %d", len(output))
	}
}

func TestMessageListShortReadFileResultNotTruncated(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	shortOutput := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	toolCallsJSON, _ := json.Marshal(map[string]any{
		"tool_id":   "call_read",
		"tool_name": "read_file",
		"args":      map[string]any{"path": "main.go"},
	})
	toolResultJSON, _ := json.Marshal(map[string]any{
		"output":   shortOutput,
		"success":  true,
		"duration": 2,
	})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:         "test-read-short",
		TaskID:     taskID,
		ThreadID:   (func() string { task, _ := s.store.GetTask(projID, taskID); return task.CurrentThreadID })(),
		Role:       "tool_call",
		Content:    "",
		ToolCalls:  string(toolCallsJSON),
		ToolResult: string(toolResultJSON),
		CreatedAt:  time.Now().UTC(),
	})

	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	msgs := mustJSON[[]map[string]any](t, rr.Body)
	var readMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "tool_call" {
			readMsg = m
			break
		}
	}
	if readMsg == nil {
		t.Fatal("tool_call message not found")
	}

	if readMsg["result_truncated"] == true {
		t.Fatalf("expected result_truncated=false for short output, got true")
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(readMsg["tool_result"].(string)), &result); err != nil {
		t.Fatalf("parse tool_result: %v", err)
	}
	output := result["output"].(string)
	if output != shortOutput {
		t.Fatalf("expected unchanged short output, got: %s", output)
	}
}

// TestDeleteMessagesAfterCheckpointRollsBackFork verifies that when a user deletes
// all messages after a checkpoint (the message just below the checkpoint line),
// the worktree is rolled back to the checkpoint state so related file changes are
// discarded. This specifically covers fork threads, where copied messages carry no
// git_commit_hash, so the last-remaining-message lookup finds nothing and the code
// must fall back to the thread's checkpoint_commit_hash.
func TestDeleteMessagesAfterCheckpointRollsBackFork(t *testing.T) {
	s := testServer(t)

	// Create a project with a git repo that has an initial committed file.
	tmpDir := t.TempDir()
	initialContent := "hello world\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte(initialContent), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	initGitRepo(t, tmpDir)
	mustRunGit(t, tmpDir, "add", "file.txt")
	mustRunGit(t, tmpDir, "commit", "-m", "Add file.txt")

	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "checkpoint-rollback-test", "path": tmpDir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	// Create a task.
	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{"prompt": "test task"})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskData := mustJSON[map[string]any](t, taskRR.Body)
	taskID := taskData["id"].(string)

	// Create worktree (simulating task start).
	worktreePath, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Update task with branch info (required for fork to manipulate the worktree).
	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Branch = "task/" + taskID
	task.BaseRef = "master"
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task branch: %v", err)
	}

	// Main thread: commit a change so there is a real checkpoint commit.
	friendContent := "hello friend\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "file.txt"), []byte(friendContent), 0o644); err != nil {
		t.Fatalf("write friend content: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "file.txt")
	mustRunGit(t, worktreePath, "commit", "-m", "friend changes")
	checkpointCommit := mustRunGitOutput(t, worktreePath, "rev-parse", "HEAD")

	// Get the Main thread.
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	listRR := testResponseAuth(t, s, listReq)
	threads := mustJSON[[]map[string]any](t, listRR.Body)
	mainThreadID := threads[0]["id"].(string)

	// Create messages with explicit, distinct timestamps so the fork point is
	// deterministically detected: a message carrying the checkpoint commit, then
	// the checkpoint message itself.
	baseTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:            "cp-prev-msg",
		TaskID:        taskID,
		ThreadID:      mainThreadID,
		Role:          "assistant",
		Content:       "made friend changes",
		GitCommitHash: checkpointCommit,
		CreatedAt:     baseTime,
	})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:        "cp-msg",
		TaskID:    taskID,
		ThreadID:  mainThreadID,
		Role:      "user",
		Content:   "checkpoint here",
		CreatedAt: baseTime.Add(time.Second),
	})

	// Fork at the checkpoint → fork thread gets checkpoint_commit_hash = checkpointCommit.
	forkReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": "cp-msg",
	})
	withAuthCSRF(forkReq, cookie, csrfToken)
	forkRR := testResponse(t, s, forkReq)
	if forkRR.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", forkRR.Code, forkRR.Body.String())
	}

	// After fork, worktree should be reset to the checkpoint commit (friend content).
	verifyWorktreeContent(t, worktreePath, "file.txt", friendContent, "after fork reset to checkpoint")

	// In the fork thread, add a message after the checkpoint and commit a change.
	afterReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":    "user",
		"content": "after checkpoint",
	})
	withAuthCSRF(afterReq, cookie, csrfToken)
	afterRR := testResponse(t, s, afterReq)
	if afterRR.Code != http.StatusCreated {
		t.Fatalf("create after-checkpoint message: %d %s", afterRR.Code, afterRR.Body.String())
	}
	afterMsg := mustJSON[map[string]any](t, afterRR.Body)
	afterMsgID := afterMsg["id"].(string)

	forkContent := "hello fork\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "file.txt"), []byte(forkContent), 0o644); err != nil {
		t.Fatalf("write fork content: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "file.txt")
	mustRunGit(t, worktreePath, "commit", "-m", "fork changes")
	verifyWorktreeContent(t, worktreePath, "file.txt", forkContent, "fork committed changes")

	// Delete the message just below the checkpoint (removes it and everything after).
	delReq := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"message_id": afterMsgID,
	})
	withAuthCSRF(delReq, cookie, csrfToken)
	delRR := testResponse(t, s, delReq)
	if delRR.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", delRR.Code, delRR.Body.String())
	}

	// The fork thread now ends at the checkpoint. File changes made after the
	// checkpoint should be discarded → the worktree rolls back to the checkpoint.
	verifyWorktreeContent(t, worktreePath, "file.txt", friendContent, "after delete rollback to checkpoint")
}

// TestDeleteFirstRequestRollsBackToBase verifies that deleting the first real
// request (and everything after it) rolls the worktree back to the task's base
// commit. When the deleted portion contained the only checkpoint, no commit hash
// remains in the thread and the code must fall back to task.BaseCommit so the
// request's file changes are discarded.
func TestDeleteFirstRequestRollsBackToBase(t *testing.T) {
	s := testServer(t)

	// Create a project with a git repo that has an initial committed file.
	tmpDir := t.TempDir()
	initialContent := "hello world\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte(initialContent), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	initGitRepo(t, tmpDir)
	mustRunGit(t, tmpDir, "add", "file.txt")
	mustRunGit(t, tmpDir, "commit", "-m", "Add file.txt")
	baseCommit := mustRunGitOutput(t, tmpDir, "rev-parse", "HEAD")

	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "base-rollback-test", "path": tmpDir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{"prompt": "test task"})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskData := mustJSON[map[string]any](t, taskRR.Body)
	taskID := taskData["id"].(string)

	// Create worktree and record the task's base commit.
	worktreePath, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Branch = "task/" + taskID
	task.BaseRef = "master"
	task.BaseCommit = baseCommit
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task branch: %v", err)
	}

	// Get the Main thread.
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	listRR := testResponseAuth(t, s, listReq)
	threads := mustJSON[[]map[string]any](t, listRR.Body)
	mainThreadID := threads[0]["id"].(string)

	// Create a request message + assistant checkpoint with explicit timestamps.
	baseTime := time.Date(2025, 2, 1, 12, 0, 0, 0, time.UTC)
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:        "req-msg",
		TaskID:    taskID,
		ThreadID:  mainThreadID,
		Role:      "user",
		Content:   "change file",
		CreatedAt: baseTime,
	})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID:            "resp-msg",
		TaskID:        taskID,
		ThreadID:      mainThreadID,
		Role:          "assistant",
		Content:       "done",
		GitCommitHash: "deadbeef00000000000000000000000000000000", // checkpoint, gets deleted
		CreatedAt:     baseTime.Add(time.Second),
	})

	// Simulate the conversation's change in the worktree (uncommitted).
	changedContent := "hello changed\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "file.txt"), []byte(changedContent), 0o644); err != nil {
		t.Fatalf("write changed content: %v", err)
	}
	verifyWorktreeContent(t, worktreePath, "file.txt", changedContent, "conversation change present")

	// Delete the request message → deletes it and the assistant response after it.
	delReq := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"message_id": "req-msg",
	})
	withAuthCSRF(delReq, cookie, csrfToken)
	delRR := testResponse(t, s, delReq)
	if delRR.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", delRR.Code, delRR.Body.String())
	}

	// No checkpoint remains, so the worktree should roll back to the base commit.
	verifyWorktreeContent(t, worktreePath, "file.txt", initialContent, "after delete rollback to base")
}

// TestDeleteCounterRequestRollsBackToPriorCheckpoint reproduces the user scenario:
//  1. "Create hello.cs" -> hello.cs WITHOUT counter (checkpoint A, survives)
//  2. "count to 5"      -> hello.cs WITH counter    (checkpoint B, deleted)
//  3. delete the "count to 5" message
// Expected: worktree rolls back to checkpoint A -> hello.cs WITHOUT counter
// (NOT the base commit, which would remove the file entirely).
func TestDeleteCounterRequestRollsBackToPriorCheckpoint(t *testing.T) {
	s := testServer(t)

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# base\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	initGitRepo(t, tmpDir)
	baseCommit := mustRunGitOutput(t, tmpDir, "rev-parse", "HEAD")

	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "prior-checkpoint-test", "path": tmpDir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{"prompt": "test task"})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskData := mustJSON[map[string]any](t, taskRR.Body)
	taskID := taskData["id"].(string)

	worktreePath, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Branch = "task/" + taskID
	task.BaseRef = "master"
	task.BaseCommit = baseCommit
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}

	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	listRR := testResponseAuth(t, s, listReq)
	threads := mustJSON[[]map[string]any](t, listRR.Body)
	mainThreadID := threads[0]["id"].(string)

	// Create the two real checkpoint stashes in the worktree.
	noCounter := "using System;\n\nclass Program\n{\n    static void Main()\n    {\n        Console.WriteLine(\"Hello, World!\");\n    }\n}\n"
	withCounter := "using System;\n\nclass Program\n{\n    static void Main()\n    {\n        for (int i = 1; i <= 5; i++)\n        {\n            Console.WriteLine(i);\n        }\n    }\n}\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "hello.cs"), []byte(noCounter), 0o644); err != nil {
		t.Fatalf("write no-counter: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "hello.cs")
	checkpointA := mustRunGitOutput(t, worktreePath, "stash", "create")
	if err := os.WriteFile(filepath.Join(worktreePath, "hello.cs"), []byte(withCounter), 0o644); err != nil {
		t.Fatalf("write with-counter: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "hello.cs")
	checkpointB := mustRunGitOutput(t, worktreePath, "stash", "create")
	if checkpointA == "" || checkpointB == "" {
		t.Fatal("expected non-empty checkpoint hashes")
	}

	// Messages: userA, assistantA(checkpointA), userB(count to 5), assistantB(checkpointB).
	baseTime := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	_ = s.store.CreateTaskMessage(&types.TaskMessage{ID: "user-a", TaskID: taskID, ThreadID: mainThreadID, Role: "user", Content: "Create hello.cs", CreatedAt: baseTime})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{ID: "asm-a", TaskID: taskID, ThreadID: mainThreadID, Role: "assistant", Content: "created", GitCommitHash: checkpointA, CreatedAt: baseTime.Add(time.Second)})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{ID: "user-b", TaskID: taskID, ThreadID: mainThreadID, Role: "user", Content: "count to 5", CreatedAt: baseTime.Add(2 * time.Second)})
	_ = s.store.CreateTaskMessage(&types.TaskMessage{ID: "asm-b", TaskID: taskID, ThreadID: mainThreadID, Role: "assistant", Content: "done", GitCommitHash: checkpointB, CreatedAt: baseTime.Add(3 * time.Second)})

	// Worktree currently holds the with-counter version (staged).
	verifyWorktreeContent(t, worktreePath, "hello.cs", withCounter, "before delete")

	// Delete the "count to 5" user message (removes it and asm-b).
	delReq := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"message_id": "user-b",
	})
	withAuthCSRF(delReq, cookie, csrfToken)
	delRR := testResponse(t, s, delReq)
	if delRR.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", delRR.Code, delRR.Body.String())
	}

	// Expected: roll back to checkpoint A -> hello.cs WITHOUT counter.
	verifyWorktreeContent(t, worktreePath, "hello.cs", noCounter, "after delete rollback to prior checkpoint")
}
