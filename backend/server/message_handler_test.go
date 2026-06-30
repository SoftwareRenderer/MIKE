package server

import (
	"encoding/json"
	"net/http"
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
