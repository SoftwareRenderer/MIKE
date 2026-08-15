package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"kanban/backend/types"
)

// mockAnswerLLM returns an httptest server that answers a non-streaming chat
// completion with a fixed consolidated numbered answer string. Like the real
// servers behind strict chat templates (e.g. Qwen3.8 on llama.cpp), it rejects
// requests that carry no user message with a 500.
func mockAnswerLLM(answer string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		hasUser := false
		for _, m := range req.Messages {
			if m.Role == "user" {
				hasUser = true
				break
			}
		}
		if !hasUser {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"No user query found in messages."}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": answer}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func setupAutoAnswerTasks(t *testing.T) (*TaskExecutor, *types.Task, *types.Task) {
	t.Helper()
	// RenderAnswerQuestionPrompt loads prompts/answer_question.tmpl from the CWD —
	// chdir to the repo root so the template is found (mirrors other prompt tests).
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}

	te := newTestExecutor(t)
	te.gate = NewConcurrencyGate()
	s := te.store
	proj := createProject(t, s, "proj-auto-answer", "Auto Answer")

	parent := &types.Task{
		ID:              "parent-aa",
		ProjectID:       proj.ID,
		Title:           "Parent AA",
		Prompt:          "Implement login with OAuth.",
		Status:          "in_progress",
		ExecutionState:  "idle",
		LlamaServerURL:  "http://placeholder",
		ModelName:       "parent-model",
		CurrentThreadID: "thread-parent",
	}
	if err := s.CreateTask(parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	subtask := &types.Task{
		ID:              "subtask-aa",
		ProjectID:       proj.ID,
		Title:           "Subtask AA",
		Prompt:          "Add login endpoint",
		Status:          "in_progress",
		ExecutionState:  "idle",
		ParentTaskID:    parent.ID,
		Autostarted:     true,
		LlamaServerURL:  "http://placeholder",
		ModelName:       "subtask-model",
		CurrentThreadID: "thread-subtask",
	}
	if err := s.CreateTask(subtask); err != nil {
		t.Fatalf("create subtask: %v", err)
	}
	return te, parent, subtask
}

func TestAutomatedAnswers_SingleQuestion(t *testing.T) {
	server := mockAnswerLLM("1) Use OAuth flow.")
	defer server.Close()

	te, parent, subtask := setupAutoAnswerTasks(t)
	// Parent's endpoint is preferred; point it at the mock.
	parent.LlamaServerURL = server.URL
	if err := te.store.UpdateTask(parent); err != nil {
		t.Fatalf("update parent: %v", err)
	}

	answers, err := te.automatedAnswers(context.Background(), subtask, []string{"Which auth flow?"})
	if err != nil {
		t.Fatalf("automatedAnswers: %v", err)
	}
	if len(answers) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(answers))
	}
	if answers[0] != "Use OAuth flow." {
		t.Errorf("unexpected answer: %q", answers[0])
	}
}

func TestAutomatedAnswers_MultipleQuestions(t *testing.T) {
	server := mockAnswerLLM("1) Use OAuth. 2) 30 minutes")
	defer server.Close()

	te, parent, subtask := setupAutoAnswerTasks(t)
	parent.LlamaServerURL = server.URL
	if err := te.store.UpdateTask(parent); err != nil {
		t.Fatalf("update parent: %v", err)
	}

	answers, err := te.automatedAnswers(context.Background(), subtask, []string{"Which auth flow?", "Token expiry?"})
	if err != nil {
		t.Fatalf("automatedAnswers: %v", err)
	}
	want := []string{"Use OAuth.", "30 minutes"}
	if !reflect.DeepEqual(answers, want) {
		t.Errorf("unexpected answers: got %#v, want %#v", answers, want)
	}
}

func TestAutomatedAnswers_NoParent(t *testing.T) {
	te, _, subtask := setupAutoAnswerTasks(t)
	subtask.ParentTaskID = ""
	if _, err := te.automatedAnswers(context.Background(), subtask, []string{"question?"}); err == nil {
		t.Error("expected error when task has no parent")
	}
}

// mkToolCall builds a ChatToolCall (Function is an anonymous struct, so it
// can't be set in a literal).
func mkToolCall(id, name, args string) types.ChatToolCall {
	tc := types.ChatToolCall{ID: id}
	tc.Function.Name = name
	tc.Function.Arguments = args
	return tc
}

func TestAskQuestionResults_AlignmentWithIncompleteCalls(t *testing.T) {
	pending := []types.ChatToolCall{
		mkToolCall("c0", "read_file", `{"path":"a"}`),
		{ID: ""}, // incomplete — skipped, no result
		mkToolCall("c1", "ask_question", `{"question":"q1"}`),
		mkToolCall("c2", "bash", `{"command":"ls"}`),
		mkToolCall("c3", "ask_question", `{"question":"q2"}`),
	}
	results := []toolResult{
		{toolCallID: "c0", output: "a-content"},
		{toolCallID: "c1", output: "q1"},
		{toolCallID: "c2", output: "ls-out"},
		{toolCallID: "c3", output: "q2"},
	}
	got := askQuestionResults(pending, results)
	want := map[int]int{2: 1, 4: 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("askQuestionResults: got %#v, want %#v", got, want)
	}
}

func TestAskQuestionResults_AllComplete(t *testing.T) {
	pending := []types.ChatToolCall{
		mkToolCall("c0", "ask_question", ""),
		mkToolCall("c1", "bash", ""),
		mkToolCall("c2", "ask_question", ""),
	}
	results := []toolResult{
		{toolCallID: "c0", output: "q0"},
		{toolCallID: "c1", output: "out"},
		{toolCallID: "c2", output: "q2"},
	}
	got := askQuestionResults(pending, results)
	want := map[int]int{0: 0, 2: 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("askQuestionResults: got %#v, want %#v", got, want)
	}
}

func TestSplitNumberedAnswers(t *testing.T) {
	tests := []struct {
		response string
		n        int
		want     []string
		wantErr  bool
	}{
		{"1) Use OAuth. 2) 30 minutes", 2, []string{"Use OAuth.", "30 minutes"}, false},
		{"1. Use OAuth.", 1, []string{"Use OAuth."}, false},
		{"1. Use OAuth.\n2. 30 minutes", 2, []string{"Use OAuth.", "30 minutes"}, false},
		{"Just an answer", 1, []string{"Just an answer"}, false},
		{"", 1, nil, true},
		{"1) Only one", 2, nil, true},
		// Decimals/versions/dates in answers must not split them.
		{"1) Use Go 1.21. 2) Use Python 3.10.", 2, []string{"Use Go 1.21.", "Use Python 3.10."}, false},
		{"1) It costs 3.5 dollars. 2) Done", 2, []string{"It costs 3.5 dollars.", "Done"}, false},
		{"1) At 2.5 hours. 2) OK", 2, []string{"At 2.5 hours.", "OK"}, false},
		{"1) Upgrade to v2.0. 2) Then reboot", 2, []string{"Upgrade to v2.0.", "Then reboot"}, false},
		{"1) Ship by 12.5.2024. 2) Done", 2, []string{"Ship by 12.5.2024.", "Done"}, false},
		{"1) See section 2. 2) Done", 2, []string{"See section 2.", "Done"}, false},
		{"Use Go 1.21", 1, []string{"Use Go 1.21"}, false},
		// Out-of-order or duplicated numbering must error, not silently misalign.
		{"1) first 3) third 2) second", 2, nil, true},
		{"1) a 1) b", 2, nil, true},
	}
	for _, tc := range tests {
		got, err := splitNumberedAnswers(tc.response, tc.n)
		if tc.wantErr {
			if err == nil {
				t.Errorf("splitNumberedAnswers(%q, %d): expected error, got %#v", tc.response, tc.n, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitNumberedAnswers(%q, %d): unexpected error %v", tc.response, tc.n, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitNumberedAnswers(%q, %d): got %#v, want %#v", tc.response, tc.n, got, tc.want)
		}
	}
}
