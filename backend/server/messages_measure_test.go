package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
)

// The size claim of the task-open window, measured: one synthetic 60-iteration turn is what a long agent
// session looks like in the table. `go test ./backend/server -run TaskOpenReads -v` re-measures it, and the
// assertion is the ratio rather than a byte count.

// iterationBytes is what one tool call's payload costs on the wire: 6 KiB of arguments and 6 KiB of output.
const iterationBytes = 6 << 10

// seedLongTurn writes one turn: the user row that opened it, then `iterations` assistant rows each
// carrying one tool call with its arguments and its result.
func seedLongTurn(t *testing.T, s *Server, projID, taskID string, iterations int) {
	t.Helper()
	base := time.Now().UTC()
	write := func(role, content, toolCalls, toolResult, assistantID string, at time.Time) {
		seedMessage(t, s, projID, taskID, &types.TaskMessage{
			Role: role, Content: content, ToolCalls: toolCalls, ToolResult: toolResult,
			AssistantMsgID: assistantID, CreatedAt: at,
		})
	}
	write("user", "work through the tree", "", "", "", base)
	body := strings.Repeat("func example() int { return 1 }\n", iterationBytes/29)
	for i := 0; i < iterations; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		assistant := fmt.Sprintf("assistant-%03d", i)
		write("assistant", fmt.Sprintf("iteration %03d: reading, then editing", i), "", "", "", at)
		args, _ := json.Marshal(map[string]any{
			"tool_id": fmt.Sprintf("call-%03d", i), "tool_name": "edit_file",
			"args": map[string]any{"path": fmt.Sprintf("file-%03d.go", i), "edits": body},
		})
		result, _ := json.Marshal(map[string]any{"output": body, "success": true, "duration": 12})
		write("tool_call", "", string(args), string(result), assistant, at.Add(time.Millisecond))
	}
}

func readRaw(t *testing.T, s *Server, path string) (rows int, bytes int) {
	t.Helper()
	rr := testResponseAuth(t, s, testRequest("GET", path, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rr.Code, rr.Body.String())
	}
	var messages []types.TaskMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &messages); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return len(messages), rr.Body.Len()
}

func TestTaskOpenReadsAFractionOfItsThread(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Sixty iterations")
	const iterations = 60
	seedLongTurn(t, s, projID, taskID, iterations)
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"

	openRows, openBytes := readRaw(t, s, path)
	allRows, allBytes := readRaw(t, s, path+"?limit=25")
	whole, err := s.store.GetTaskMessages(taskID, "")
	if err != nil {
		t.Fatalf("read the thread: %v", err)
	}
	encoded, err := json.Marshal(whole)
	if err != nil {
		t.Fatalf("encode the thread: %v", err)
	}
	t.Logf("one open reads %d rows / %d KiB; the thread holds %d rows / %d KiB; the widest window the route answers is %d rows / %d KiB",
		openRows, openBytes>>10, len(whole), len(encoded)>>10, allRows, allBytes>>10)

	if openRows > 12 {
		t.Errorf("the default window read %d rows, want the newest 5 answers and their calls", openRows)
	}
	if openBytes*10 > len(encoded) {
		t.Errorf("one open read %d of the thread's %d bytes: a fifth of the conversation is not a window",
			openBytes, len(encoded))
	}
}
