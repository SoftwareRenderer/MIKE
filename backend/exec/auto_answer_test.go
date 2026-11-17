package exec

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"mike/backend/types"
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
		_ = json.UnmarshalRead(r.Body, &req)
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
		_ = json.MarshalWrite(w, resp)
	}))
}

func setupAutoAnswerTasks(t *testing.T) (*TaskExecutor, *types.Task, *types.Task) {
	t.Helper()
	// RenderAnswerQuestionPrompt loads prompts/answer_question.tmpl from the CWD —
	// chdir to the repo root so the template is found (mirrors other prompt tests). The callers are
	// serial for the reason at chdirRepoRoot, and the CWD is left here on purpose.
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}

	te, s := newTestExecutorWithStore(t)
	te.gate = NewConcurrencyGate()
	proj := createProject(t, s, "proj-auto-answer", "Auto Answer")

	parent := &types.Task{
		ID:              "parent-aa",
		ProjectID:       proj.ID,
		Title:           "Parent AA",
		Prompt:          "Implement login with OAuth.",
		Status:          types.StatusInProgress,
		ExecutionState:  types.ExecStateIdle,
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
		Status:          types.StatusInProgress,
		ExecutionState:  types.ExecStateIdle,
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

// TestAutomatedAnswers_AnswersFromTheParentsContext is the answering call: the parent's endpoint and
// model answer, and the reply is split into one answer per question by its numbering.
func TestAutomatedAnswers_AnswersFromTheParentsContext(t *testing.T) {
	for _, tc := range []struct {
		name      string
		questions []string
		reply     string
		want      []string
	}{
		{
			name:      "one question",
			questions: []string{"Which auth flow?"},
			reply:     "1) Use OAuth flow.",
			want:      []string{"Use OAuth flow."},
		},
		{
			name:      "two questions keep their order",
			questions: []string{"Which auth flow?", "Token expiry?"},
			reply:     "1) Use OAuth. 2) 30 minutes",
			want:      []string{"Use OAuth.", "30 minutes"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := mockAnswerLLM(tc.reply)
			defer server.Close()

			te, parent, subtask := setupAutoAnswerTasks(t)
			// Parent's endpoint is preferred; point it at the mock.
			parent.LlamaServerURL = server.URL
			if err := te.store.UpdateTask(parent); err != nil {
				t.Fatalf("update parent: %v", err)
			}

			answers, err := te.automatedAnswers(context.Background(), subtask, tc.questions)
			if err != nil {
				t.Fatalf("automatedAnswers: %v", err)
			}
			if !reflect.DeepEqual(answers, tc.want) {
				t.Errorf("answers = %#v, want %#v", answers, tc.want)
			}
		})
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

// TestAskQuestionResults picks the calls that are a real question, and maps each to its answer by
// position. A call that never got a result — an incomplete call, or a failed ask_question — is not a
// question, so it must not reach the user as "the agent has a question" nor be answered from the
// parent's context; a real question in the same turn still is. With none left the caller falls
// through to the next LLM call instead of idling the task.
func TestAskQuestionResults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pending []types.ChatToolCall
		results []toolResult
		want    map[int]int
	}{
		{
			name: "indexes survive an incomplete call between the questions",
			pending: []types.ChatToolCall{
				mkToolCall("c0", "read_file", `{"path":"a"}`),
				{ID: ""}, // incomplete — skipped, no result
				mkToolCall("c1", "ask_question", `{"question":"q1"}`),
				mkToolCall("c2", "bash", `{"command":"ls"}`),
				mkToolCall("c3", "ask_question", `{"question":"q2"}`),
			},
			results: []toolResult{
				{toolCallID: "c0", output: "a-content", success: true},
				{toolCallID: "c1", output: "q1", success: true},
				{toolCallID: "c2", output: "ls-out", success: true},
				{toolCallID: "c3", output: "q2", success: true},
			},
			want: map[int]int{2: 1, 4: 3},
		},
		{
			name: "a failed ask_question is an error, not a question",
			pending: []types.ChatToolCall{
				mkToolCall("c0", "ask_question", `{}`),
				mkToolCall("c1", "bash", `{"command":"ls"}`),
				mkToolCall("c2", "ask_question", `{"question":"Which scope?"}`),
			},
			results: []toolResult{
				{toolCallID: "c0", output: "Error: question is required", success: false},
				{toolCallID: "c1", output: "ls-out", success: true},
				{toolCallID: "c2", output: "Which scope?", success: true},
			},
			want: map[int]int{2: 2},
		},
		{
			name: "no question left — neither a failed call nor a call with no result",
			pending: []types.ChatToolCall{
				mkToolCall("c0", "ask_question", `{"question":""}`),
				mkToolCall("c1", "ask_question", `{"question":"never answered"}`),
			},
			results: []toolResult{
				{toolCallID: "c0", output: "Error: question is required", success: false},
			},
			want: map[int]int{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := askQuestionResults(tc.pending, tc.results); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("askQuestionResults = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSplitNumberedAnswers(t *testing.T) {
	t.Parallel()
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
