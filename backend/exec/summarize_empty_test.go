package exec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"mike/backend/types"
)

// chdirToRepoRoot points the CWD at the repo root so summarizeContext can render
// prompts/summarize.tmpl. Serial for the reason at chdirRepoRoot.
func chdirToRepoRoot(t *testing.T) {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
}

// emptySummaryLLM returns a completion whose content is empty — what a model
// returns when it spends its whole budget thinking (null on vLLM, "" on llama.cpp).
func emptySummaryLLM(content string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` + content +
			`,"reasoning":"Summarizing the conversation..."},"finish_reason":"length"}],` +
			`"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}}`))
	}))
}

// TestSummarizeContext_EmptySummaryErrors: installing an empty summary is silent
// data loss, since buildLLMContext drops every message before the summarization
// entry.
func TestSummarizeContext_EmptySummaryErrors(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"vLLM null content", "null"},
		{"llama.cpp empty string", `""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chdirToRepoRoot(t)
			te := newTestExecutor(t)
			server := emptySummaryLLM(tc.content)
			defer server.Close()

			messages := []types.ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "please fix the bug"},
				{Role: "assistant", Content: "I fixed the bug"},
			}

			res, err := te.summarize.summarizeContext(context.Background(), messages, server.URL, "test-model", "")
			if err == nil {
				t.Fatalf("summarizeContext accepted an empty summary (got %q); installing it would erase the conversation", res.summary)
			}
			if len(messages) != 3 {
				t.Errorf("messages slice modified: len = %d, want 3 (summarizeContext must not mutate)", len(messages))
			}
		})
	}
}

// TestSummarizeContext_NonEmptySummarySucceeds is the control: a real summary is
// accepted and its usage reported.
func TestSummarizeContext_NonEmptySummarySucceeds(t *testing.T) {
	chdirToRepoRoot(t)
	te := newTestExecutor(t)
	server := emptySummaryLLM(`"The user asked for a bug fix; the assistant fixed it."`)
	defer server.Close()

	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "please fix the bug"},
	}

	res, err := te.summarize.summarizeContext(context.Background(), messages, server.URL, "test-model", "")
	if err != nil {
		t.Fatalf("summarizeContext error: %v", err)
	}
	if res.summary == "" {
		t.Error("summary = empty, want the generated text")
	}
	if res.promptTokens != 100 || res.predictedTokens != 5 {
		t.Errorf("usage = %+v/%+v, want 100/5", res.promptTokens, res.predictedTokens)
	}
}

// TestBuildLLMContext_EmptySummaryErasesHistory shows why the guard exists: once
// a summarization message exists, everything before it is dropped on the
// assumption the summary captured it. An empty summary captures nothing.
func TestBuildLLMContext_EmptySummaryErasesHistory(t *testing.T) {
	t.Parallel()
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "the entire conversation"},
		{Role: "assistant", Content: "all the work done so far"},
		{Role: "summarization", Content: ""},
	}

	got := buildLLMContext(messages)
	if len(got) != 1 || got[0].Role != "system" {
		t.Fatalf("context = %+v, want only the system prompt — this is the data-loss this guard prevents", got)
	}
	t.Log("confirmed: an installed empty summary leaves only the system prompt")
}
