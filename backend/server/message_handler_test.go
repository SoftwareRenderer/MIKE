package server

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// seedMessage writes a message directly to the store. It is used to seed
// internal-role (assistant/tool_call) fixture messages that the user-facing
// POST endpoint must reject for security reasons.
func seedMessage(t *testing.T, s *Server, projID, taskID string, msg *types.TaskMessage) *types.TaskMessage {
	t.Helper()
	if msg.TaskID == "" {
		msg.TaskID = taskID
	}
	if msg.ThreadID == "" {
		if task, _ := s.store.GetTask(projID, taskID); task != nil {
			msg.ThreadID = task.CurrentThreadID
		}
	}
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if err := s.store.CreateTaskMessage(msg); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	return msg
}

// The POST answers with the row it wrote, stamped with what the client did not send, and the window the
// task opens on then reads that row back: a row written into another thread is a message the conversation
// never sees.
func TestMessageCreateWritesTheRowTheWindowReadsBack(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"

	var created []string
	for _, content := range []string{"msg 1", "msg 2", "msg 3"} {
		rr := testResponseAuth(t, s, testRequest(http.MethodPost, path, map[string]any{
			"role": "user", "content": content,
		}))
		if rr.Code != http.StatusCreated {
			t.Fatalf("POST = %d: %s", rr.Code, rr.Body.String())
		}
		got := mustJSON[map[string]any](t, rr.Body)
		if got["role"] != "user" || got["content"] != content {
			t.Errorf("the response carries %v/%v, want the sent role and content", got["role"], got["content"])
		}
		if got["task_id"] != taskID {
			t.Errorf("task_id = %v, want the task the path named", got["task_id"])
		}
		// The id is the server's: a client that could choose one could answer for another row.
		id, _ := got["id"].(string)
		if id == "" {
			t.Fatal("the row was answered with no id")
		}
		created = append(created, id)
	}

	window, _ := readWindow(t, s, path)
	answered := map[string]bool{}
	for _, m := range window {
		answered[m.ID] = true
	}
	for _, id := range created {
		if !answered[id] {
			t.Errorf("the row the POST answered (%s) is not in the window the task opens on", id)
		}
	}
	// The task's own prompt is part of the thread whatever the client sent.
	if len(window) != len(created)+1 {
		t.Errorf("the window holds %d rows, want the %d sent beside the task's prompt row", len(window), len(created))
	}
}

// The message routes refuse a request they cannot act on, and each refusal costs a different answer: an
// internal role would inject text into the LLM's conversation, and a body over the cap is a size problem
// the caller can act on. A refused write leaves the thread as it was.
func TestMessageRoutesRefuseARequestTheyCannotActOn(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")
	path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"

	cases := []struct {
		name   string
		method string
		body   map[string]any
		want   int
	}{
		{"a post with no role", http.MethodPost, map[string]any{"content": "Hello"}, http.StatusBadRequest},
		{"a post with no content", http.MethodPost, map[string]any{"role": "user"}, http.StatusBadRequest},
		{"a system row", http.MethodPost, map[string]any{"role": "system", "content": "injected"}, http.StatusBadRequest},
		{"an assistant row", http.MethodPost, map[string]any{"role": "assistant", "content": "injected"}, http.StatusBadRequest},
		{"a tool_call row", http.MethodPost, map[string]any{"role": "tool_call", "content": "injected"}, http.StatusBadRequest},
		{"a tool row", http.MethodPost, map[string]any{"role": "tool", "content": "injected"}, http.StatusBadRequest},
		{
			"a body over the cap", http.MethodPost,
			map[string]any{"role": "user", "content": strings.Repeat("a", (1<<20)+1)},
			http.StatusRequestEntityTooLarge,
		},
		{"a delete naming no row", http.MethodDelete, map[string]any{}, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := testResponseAuth(t, s, testRequest(tc.method, path, tc.body))
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}

	if window, _ := readWindow(t, s, path); len(window) != 1 {
		t.Errorf("%d rows survived the refused writes, want the task's own prompt row alone", len(window))
	}
}

// A PATCH writes the field the body names and leaves the one it does not. Both fields are load-bearing
// downstream: a summarization row's content is what the task's system prompt carries, and a checkpoint hash
// is later handed to `git read-tree`, so an absent key must never arrive as a zero written into the row.
func TestPatchMessageWritesOnlyTheFieldItNames(t *testing.T) {
	const edited = "the edited text"

	cases := []struct {
		name string
		seed func(t *testing.T, s *Server, projID, taskID string) string
	}{
		{"a user row", func(t *testing.T, s *Server, projID, taskID string) string {
			return seedMessage(t, s, projID, taskID, &types.TaskMessage{
				Role: "user", Content: "original content",
			}).ID
		}},
		{"a summarization row", func(t *testing.T, s *Server, projID, taskID string) string {
			return createSummaryMessage(t, s, taskID, "the summary in effect")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)
			taskID := createTestTask(t, s, projID, "Test task")
			msgID := tc.seed(t, s, projID, taskID)
			path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages/"

			rr := testResponseAuth(t, s, testRequest(http.MethodPatch, path+msgID,
				map[string]any{"content": edited}))
			if rr.Code != http.StatusOK {
				t.Fatalf("edit = %d: %s", rr.Code, rr.Body.String())
			}
			if got := storedMessageContent(t, s, msgID); got != edited {
				t.Errorf("stored content = %q, want the edited text", got)
			}
			// The chat reads the window, so the edit has to reach that response too.
			window, _ := readWindow(t, s, "/api/projects/"+projID+"/tasks/"+taskID+"/messages")
			var shown string
			for _, m := range window {
				if m.ID == msgID {
					shown = m.Content
				}
			}
			if shown != edited {
				t.Errorf("the window answers %q, want the edited text", shown)
			}

			// A body naming neither field is a no-op, not a blank written into the row.
			rr = testResponseAuth(t, s, testRequest(http.MethodPatch, path+msgID, map[string]any{}))
			if rr.Code != http.StatusOK {
				t.Fatalf("an empty patch = %d: %s", rr.Code, rr.Body.String())
			}
			if got := storedMessageContent(t, s, msgID); got != edited {
				t.Errorf("a patch naming no field rewrote the row to %q", got)
			}
		})
	}
}

// storedMessageContent reads what the database holds, because a response that echoes an edit the row never
// got would otherwise pass.
func storedMessageContent(t *testing.T, s *Server, messageID string) string {
	t.Helper()
	msg, err := s.store.GetTaskMessageByID(messageID)
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	return msg.Content
}

// The list holds back a write_file's payload — the bytes are on disk and the card prints the path — and
// leaves every other tool's arguments whole, because for those the arguments are what the card shows.
func TestMessageListRedactsWriteFileArgs(t *testing.T) {
	const fileBody = "package main\n\nfunc main() {}\n"

	cases := []struct {
		name        string
		toolName    string
		args        map[string]any
		rawArgs     string
		query       string
		wantArgs    map[string]any
		wantRawArgs any
	}{
		{
			name:     "the content of a write_file is held back",
			toolName: "write_file",
			args:     map[string]any{"path": "src/main.go", "content": fileBody},
			rawArgs:  `{"path":"src/main.go","content":"package main\n\nfunc main() {}\n"}`,
			wantArgs: map[string]any{"path": "src/main.go", "content": "[REDACTED]"}, wantRawArgs: "[REDACTED]",
		},
		{
			name:        "full=1 sends it",
			toolName:    "write_file",
			args:        map[string]any{"path": "src/main.go", "content": fileBody},
			rawArgs:     `{"path":"src/main.go","content":"package main\n\nfunc main() {}\n"}`,
			query:       "?full=1",
			wantArgs:    map[string]any{"path": "src/main.go", "content": fileBody},
			wantRawArgs: `{"path":"src/main.go","content":"package main\n\nfunc main() {}\n"}`,
		},
		{
			name:     "another tool's arguments are the card's own text",
			toolName: "bash",
			args:     map[string]any{"command": "ls -la"},
			rawArgs:  `{"command":"ls -la"}`,
			wantArgs: map[string]any{"command": "ls -la"}, wantRawArgs: `{"command":"ls -la"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)
			taskID := createTestTask(t, s, projID, "Test task")
			toolCalls, err := json.Marshal(map[string]any{
				"tool_id": "call-1", "tool_name": tc.toolName, "tool_display_name": "Tool",
				"args": tc.args, "raw_args": tc.rawArgs, "sandbox": false,
			})
			if err != nil {
				t.Fatalf("encode tool_calls: %v", err)
			}
			seedMessage(t, s, projID, taskID, &types.TaskMessage{
				Role: "tool_call", Content: "done",
				ToolCalls: string(toolCalls), ToolResult: `{"output":"","success":true,"duration":10}`,
			})

			row := windowRowByRole(t, s, "/api/projects/"+projID+"/tasks/"+taskID+"/messages"+tc.query, "tool_call")
			var tc2 struct {
				ToolName string         `json:"tool_name"`
				Args     map[string]any `json:"args"`
				RawArgs  any            `json:"raw_args"`
			}
			if err := json.Unmarshal([]byte(row["tool_calls"].(string)), &tc2); err != nil {
				t.Fatalf("parse tool_calls: %v", err)
			}
			if tc2.ToolName != tc.toolName {
				t.Fatalf("the row read back is a %q call", tc2.ToolName)
			}
			if !reflect.DeepEqual(tc2.Args, tc.wantArgs) {
				t.Errorf("args = %v, want %v", tc2.Args, tc.wantArgs)
			}
			if tc2.RawArgs != tc.wantRawArgs {
				t.Errorf("raw_args = %v, want %v", tc2.RawArgs, tc.wantRawArgs)
			}
		})
	}
}

// windowRowByRole reads the window and answers the last row of one role.
func windowRowByRole(t *testing.T, s *Server, path, role string) map[string]any {
	t.Helper()
	rr := testResponseAuth(t, s, testRequest(http.MethodGet, path, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rr.Code, rr.Body.String())
	}
	var found map[string]any
	for _, m := range mustJSON[[]map[string]any](t, rr.Body) {
		if m["role"] == role {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("%s holds no %s row", path, role)
	}
	return found
}

// Reasoning is bulk: a long chain of thought behind an answer costs bandwidth the answer already used, so
// the list hides it and says it is there. Two shapes keep it whole — reasoning that IS the answer, and
// reasoning that carried a tool call — and full=1 lifts the rule entirely. The same pass runs over the
// window, so a task opened on one answer is redacted too.
func TestMessageListSuppressesBulkReasoning(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		reasoning  string
		toolCalls  string
		olderUser  bool
		query      string
		want       string
		wantHidden bool
	}{
		{
			name: "a chain of thought behind an answer", content: "Here is the answer.",
			reasoning: strings.Repeat("x", 300), want: "[HIDDEN]", wantHidden: true,
		},
		{
			name: "reasoning that is the whole answer", reasoning: "Brief thought",
			want: "Brief thought",
		},
		{
			name: "reasoning that carried a tool call", reasoning: strings.Repeat("z", 400),
			toolCalls: `{"tool_id":"call_1","tool_name":"bash","args":{"command":"ls"}}`,
			want:      strings.Repeat("z", 400),
		},
		{
			name: "full=1 sends it", content: "The answer is 42.", reasoning: strings.Repeat("y", 500),
			query: "?full=1", want: strings.Repeat("y", 500),
		},
		{
			name: "the newest answer of a windowed read", olderUser: true, content: "writing it now",
			reasoning: strings.Repeat("w", 300) + " the tail of the thought",
			query:     "?limit=1", want: "[HIDDEN]", wantHidden: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)
			taskID := createTestTask(t, s, projID, "Test task")
			if tc.olderUser {
				seedWindow(t, s, projID, taskID, 1)
			}
			seedMessage(t, s, projID, taskID, &types.TaskMessage{
				Role: "assistant", Content: tc.content, ReasoningContent: tc.reasoning, ToolCalls: tc.toolCalls,
			})

			path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages" + tc.query
			row := windowRowByRole(t, s, path, "assistant")
			if got := row["reasoning_content"]; got != tc.want {
				t.Errorf("reasoning_content = %v, want %q", got, tc.want)
			}
			if got := row["has_reasoning"]; got == true != tc.wantHidden {
				t.Errorf("has_reasoning = %v, want %v", got, tc.wantHidden)
			}
			if tc.wantHidden && row["content"] != tc.content {
				t.Errorf("hiding the reasoning also moved the answer: %v", row["content"])
			}
		})
	}
}

func TestMessageGetByIDFull(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create an assistant message with long reasoning + content (would be suppressed in list)
	longReasoning := strings.Repeat("a", 500)
	msg := seedMessage(t, s, projID, taskID, &types.TaskMessage{
		Role:             "assistant",
		Content:          "The answer is 42.",
		ReasoningContent: longReasoning,
	})

	// GET single message with full=1 — reasoning should be preserved
	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages/"+msg.ID+"?full=1", nil)
	rr := testResponseAuth(t, s, req)
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

// The list redacts a tool result too long to ship whole, for the two tools that return bulk output. The
// tool name is part of that decision, so it is a case here — and so is full=1, which is how the card that
// asks for the whole result gets back the bytes it was cut from.
func TestMessageListToolResultTruncation(t *testing.T) {
	long := strings.Repeat("line of output text\n", 20)
	short := "total 100\ndrwxr-xr-x  5 user user  4096 Jan 1 .\n"

	cases := []struct {
		name     string
		toolName string
		output   string
		full     bool
		wantCut  bool
	}{
		{"bash output over the ceiling", "bash", long, false, true},
		{"read_file output over the ceiling", "read_file", long, false, true},
		{"bash output within the ceiling", "bash", short, false, false},
		{"full=1 ships the whole result", "bash", long, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)
			taskID := createTestTask(t, s, projID, "Test task")

			toolCalls, _ := json.Marshal(map[string]any{
				"tool_id": "call-1", "tool_name": tc.toolName,
				"args": map[string]any{"command": "cat large_file.txt"},
			})
			toolResult, _ := json.Marshal(map[string]any{
				"output": tc.output, "success": true, "duration": 50,
			})
			seedMessage(t, s, projID, taskID, &types.TaskMessage{
				Role: "tool_call", Content: "executed",
				ToolCalls: string(toolCalls), ToolResult: string(toolResult),
			})

			path := "/api/projects/" + projID + "/tasks/" + taskID + "/messages"
			if tc.full {
				path += "?full=1"
			}
			msg := windowRowByRole(t, s, path, "tool_call")
			if got := msg["result_truncated"] == true; got != tc.wantCut {
				t.Fatalf("result_truncated = %v, want %v", got, tc.wantCut)
			}

			var result map[string]any
			if err := json.Unmarshal([]byte(msg["tool_result"].(string)), &result); err != nil {
				t.Fatalf("parse tool_result: %v", err)
			}
			output := result["output"].(string)
			if !tc.wantCut {
				if output != tc.output {
					t.Errorf("an ordinary result was rewritten: %q", output)
				}
				return
			}
			if !strings.Contains(output, "truncated") {
				t.Fatalf("the cut was not said out loud: %q", output)
			}
			if lines := strings.Count(output, "\n"); lines >= 20 {
				t.Fatalf("output still holds %d lines", lines)
			}
			// The counts are what the "show all" affordance reports.
			if got := result["original_lines"]; got != float64(len(strings.Split(tc.output, "\n"))) {
				t.Errorf("original_lines = %v", got)
			}
			if got := result["original_bytes"]; got != float64(len(tc.output)) {
				t.Errorf("original_bytes = %v", got)
			}
		})
	}
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
	testgit.Git(t, tmpDir, "add", "file.txt")
	testgit.Git(t, tmpDir, "commit", "-m", "Add file.txt")
	baseCommit := testgit.GitOutput(t, tmpDir, "rev-parse", "HEAD")

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
//
// Expected: worktree rolls back to checkpoint A -> hello.cs WITHOUT counter
// (NOT the base commit, which would remove the file entirely).
func TestDeleteCounterRequestRollsBackToPriorCheckpoint(t *testing.T) {
	s := testServer(t)

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# base\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	initGitRepo(t, tmpDir)
	baseCommit := testgit.GitOutput(t, tmpDir, "rev-parse", "HEAD")

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
	testgit.Git(t, worktreePath, "add", "hello.cs")
	checkpointA := testgit.GitOutput(t, worktreePath, "stash", "create")
	if err := os.WriteFile(filepath.Join(worktreePath, "hello.cs"), []byte(withCounter), 0o644); err != nil {
		t.Fatalf("write with-counter: %v", err)
	}
	testgit.Git(t, worktreePath, "add", "hello.cs")
	checkpointB := testgit.GitOutput(t, worktreePath, "stash", "create")
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

// A DELETE cuts the thread at the named row's entry: the row and everything after it go, everything older
// stays, and the newest summary — what the turns before it are remembered by — is never swept by a cascade
// aimed at an older row, which would reinstate the whole pre-summary history into the LLM context. Which
// rows the cut takes is backend/store's own rule; this is the route the UI calls.
func TestDeleteAfterNamedRowCutsTheThreadAtItsEntry(t *testing.T) {
	cases := []struct {
		name   string
		rows   []types.TaskMessage
		target string
		want   []string
	}{
		{
			name: "a cascade aimed at an older row leaves the summary standing",
			rows: []types.TaskMessage{
				{ID: "msg-old", Role: "user", Content: "oldest request"},
				{ID: "sum-1", Role: "summarization", Content: "summary of msg-old"},
				{ID: "msg-new", Role: "user", Content: "newest request"},
			},
			target: "msg-old",
			want:   []string{"sum-1"},
		},
		{
			name: "a summarization row takes its own tail with it",
			rows: []types.TaskMessage{
				{ID: "msg-1", Role: "user", Content: "msg 1"},
				{ID: "summ-msg", Role: "summarization", Content: "summary of msg 1"},
				{ID: "msg-2", Role: "user", Content: "msg 2"},
				{ID: "msg-3", Role: "user", Content: "msg 3"},
			},
			target: "summ-msg",
			want:   []string{"msg-1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)
			taskID := createTestTask(t, s, projID, "Truncated")
			seeded := map[string]bool{}
			base := time.Now().UTC().Add(-time.Minute)
			for i := range tc.rows {
				m := tc.rows[i]
				m.CreatedAt = base.Add(time.Duration(i) * time.Second)
				seedMessage(t, s, projID, taskID, &m)
				seeded[m.ID] = true
			}

			rr := testResponseAuth(t, s, testRequest(http.MethodDelete,
				"/api/projects/"+projID+"/tasks/"+taskID+"/messages",
				map[string]any{"message_id": tc.target}))
			if rr.Code != http.StatusOK {
				t.Fatalf("delete = %d: %s", rr.Code, rr.Body.String())
			}

			all, err := s.store.GetTaskMessages(taskID, "")
			if err != nil {
				t.Fatalf("read the thread: %v", err)
			}
			var left []string
			for _, m := range all {
				if seeded[m.ID] {
					left = append(left, m.ID)
				}
			}
			if !slices.Equal(left, tc.want) {
				t.Errorf("the rows still standing are %v, want %v", left, tc.want)
			}
		})
	}
}

// deleteCheck asks the endpoint that answers "what does deleting this take with it".
func deleteCheck(t *testing.T, s *Server, projID, taskID, msgID string) map[string]any {
	t.Helper()
	rr := testResponseAuth(t, s, testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages/"+msgID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("check: %d %s", rr.Code, rr.Body.String())
	}
	return mustJSON[map[string]any](t, rr.Body)
}

// The delete confirmation names what the click costs — the files it rewinds and the branch it deletes — and
// that verdict is the server's, because it is the side that rewinds the worktree to the state the remaining
// conversation describes. A summarized row owns no files, and a task with no worktree has nothing to go back
// to. No fork hangs off these rows, so the branch half of the answer says so.
func TestMessageDeleteCheckReportsFileRewind(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	createdRR := testResponseAuth(t, s, testRequest("POST",
		"/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{"role": "user", "content": "do it"}))
	created := mustJSON[map[string]any](t, createdRR.Body)
	msgID, _ := created["id"].(string)

	check := deleteCheck(t, s, projID, taskID, msgID)
	if check["resets_files"] == true {
		t.Error("a task with no worktree has no state to rewind")
	}
	if check["will_delete_branch"] != false {
		t.Errorf("will_delete_branch = %v, want false — no fork is keyed on this row", check["will_delete_branch"])
	}
	if check["branch_name"] != "" {
		t.Errorf("branch_name = %v, want the empty string beside a false will_delete_branch", check["branch_name"])
	}

	task, err := s.store.GetTaskByID(taskID)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	task.Branch, task.BaseCommit = "task/"+taskID, "0123456789abcdef0123456789abcdef01234567"
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}
	if got := deleteCheck(t, s, projID, taskID, msgID)["resets_files"]; got != true {
		t.Error("deleting a turn of a started task rewinds the worktree; the dialog has to say so")
	}

	summary := &types.TaskMessage{ID: "summ-resets-check", TaskID: taskID, Role: "summarization",
		Content: "what happened so far"}
	if err := s.store.CreateTaskMessage(summary); err != nil {
		t.Fatalf("create summary row: %v", err)
	}
	if got := deleteCheck(t, s, projID, taskID, summary.ID)["resets_files"]; got == true {
		t.Error("a summarized row owns no files, so deleting it moves nothing")
	}
}
