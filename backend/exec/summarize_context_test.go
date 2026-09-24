package exec

// Context-summarization tests for the LLM-facing contract: the summary reaches the
// model through the SYSTEM PROMPT, never through the summarization marker
// (buildLLMContext drops the marker along with every message the summary replaced).
// A mid-run auto-summary must therefore refresh the live system prompt, and the
// persisted row must produce the same context in a freshly started process.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/types"
)

// chdirRepoRoot makes prompts/summarize.tmpl and prompts/base.tmpl resolvable from
// the CWD, mirroring how the running server resolves them.
//
// This is the rule for every prompt test in the package — here, chdirToRepoRoot, setupAutoAnswerTasks
// and startTextStopRun: the calling test must not call t.Parallel(). The CWD is process-wide and
// template resolution reads it on every render (prompts.resolvePromptDir), so another test's restore
// moves it mid-turn and the failure reads "open prompts/summarize.tmpl: no such file or directory".
func chdirRepoRoot(t *testing.T) {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	oldwd, _ := os.Getwd()
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
}

// mockSummarySeqLLM answers each non-streaming call (context summarization) with the
// next summary in seq, so consecutive summarizations are distinguishable.
func mockSummarySeqLLM(seq ...string) *httptest.Server {
	var mu sync.Mutex
	n := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		mu.Lock()
		idx := n
		if n < len(seq) {
			n++
		}
		mu.Unlock()
		text := fmt.Sprintf("SUMMARY-UNUSED-%d", idx)
		if idx < len(seq) {
			text = seq[idx]
		}
		encoded, _ := json.Marshal(text)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, encoded)
	}))
}

// summarizeSetup wires a task whose context sits above the auto-summarize threshold
// (92 % of a 10 000-token window against the default 90 % setting).
func summarizeSetup(t *testing.T, summaries ...string) (*TaskExecutor, *taskExecSetup, *loopContext, *types.Project) {
	t.Helper()
	te := newTestExecutor(t)
	te.gate = NewConcurrencyGate()
	s := te.store
	proj := createProject(t, s, "proj-sum-ctx-"+t.Name(), "Sum Ctx")
	server := mockSummarySeqLLM(summaries...)
	t.Cleanup(server.Close)

	now := time.Now().UTC()
	task := &types.Task{
		ID: "sum-ctx-" + t.Name(), ProjectID: proj.ID, Title: "Sum ctx",
		Status: types.StatusReview, ExecutionState: types.ExecStateIdle, OperatingMode: "act",
		LlamaServerURL: server.URL, ModelName: "test-model",
		CurrentThreadID: "thread-1", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	cw := 10000
	lc := &loopContext{
		messages: []types.ChatMessage{
			{Role: "system", Content: "STALE PROMPT — NO SUMMARY"},
			{Role: "user", Content: "original request"},
			{Role: "assistant", Content: "did work"},
		},
		contextWindow: &cw,
		usage:         &types.ChatUsage{PromptTokens: 9200},
		detector:      newLoopDetector(),
		mc:            &modelConfig{serverURL: server.URL, modelName: "test-model"},
	}
	setup := &taskExecSetup{
		task: task, project: proj, projectID: proj.ID, ctx: context.Background(),
		serverURL: server.URL, modelName: "test-model", worktreePath: t.TempDir(),
	}
	return te, setup, lc, proj
}

// TestMaybeAutoSummarize_SummaryReachesLLMContext is the regression test for the
// mid-run summarization hole: after auto-summarization feeds the compressed context
// back to the LLM, the request must carry the summary (in the system prompt) next to
// the injected "Proceed". Before the fix it carried only the pre-summary prompt plus
// "Proceed" — the summary was persisted to the DB but never reached the model.
func TestMaybeAutoSummarize_SummaryReachesLLMContext(t *testing.T) {
	chdirRepoRoot(t)
	te, setup, lc, _ := summarizeSetup(t, "SUMMARY-ONE: build the widget")

	te.summarize.maybeAutoSummarize(setup, lc, "test-model", true)

	if !strings.Contains(lc.messages[0].Content, "SUMMARY-ONE") {
		t.Fatalf("system message does not embed the new summary:\n%s", lc.messages[0].Content)
	}
	if strings.Contains(lc.messages[0].Content, "STALE PROMPT") {
		t.Fatal("system message still carries the pre-summary prompt")
	}
	if !strings.Contains(lc.messages[0].Content, "# Current Task") {
		t.Fatalf("summary section heading missing from system message:\n%s", lc.messages[0].Content)
	}

	sent := buildLLMContext(lc.messages)
	if len(sent) != 2 {
		t.Fatalf("expected [system, user \"Proceed\"], got %d messages: %+v", len(sent), sent)
	}
	if sent[0].Role != "system" || !strings.Contains(sent[0].Content, "SUMMARY-ONE") {
		t.Fatalf("system message sent to the LLM lacks the summary: %+v", sent[0])
	}
	if sent[1].Role != "user" || sent[1].Content != "Proceed" {
		t.Fatalf("expected the injected Proceed, got role=%q content=%q", sent[1].Role, sent[1].Content)
	}
	for _, m := range sent {
		if m.Role == "summarization" {
			t.Fatal("the summarization marker itself must not be sent (the summary lives in the system prompt)")
		}
	}
}

// TestMaybeAutoSummarize_NewestSummaryWins pins the "most recent summary" rule for
// the live run: the second summarization supersedes the first in the system prompt,
// and buildLLMContext cuts at the newest marker only.
func TestMaybeAutoSummarize_NewestSummaryWins(t *testing.T) {
	chdirRepoRoot(t)
	te, setup, lc, _ := summarizeSetup(t, "SUMMARY-ONE: first phase", "SUMMARY-TWO: second phase")

	te.summarize.maybeAutoSummarize(setup, lc, "test-model", true)
	if !strings.Contains(lc.messages[0].Content, "SUMMARY-ONE") {
		t.Fatalf("first summary missing from system message:\n%s", lc.messages[0].Content)
	}

	// Context is still above the threshold, so a second summarization runs and must
	// fold the first summary in (passed as PreviousSummary) and replace it.
	te.summarize.maybeAutoSummarize(setup, lc, "test-model", true)

	if got := extractPrevSummary(lc.messages); got != "SUMMARY-TWO: second phase" {
		t.Fatalf("latest summarization marker = %q, want the second summary", got)
	}
	if !strings.Contains(lc.messages[0].Content, "SUMMARY-TWO") {
		t.Fatalf("newest summary missing from system message:\n%s", lc.messages[0].Content)
	}
	if strings.Contains(lc.messages[0].Content, "SUMMARY-ONE") {
		t.Fatalf("superseded summary still in system message:\n%s", lc.messages[0].Content)
	}
	sent := buildLLMContext(lc.messages)
	var text strings.Builder
	for _, m := range sent {
		text.WriteString(m.Content)
	}
	if !strings.Contains(text.String(), "SUMMARY-TWO") {
		t.Fatalf("summary sent to the LLM is not the newest: %+v", sent)
	}
	if strings.Contains(text.String(), "SUMMARY-ONE") {
		t.Fatalf("superseded summary leaked into the LLM context: %+v", sent)
	}
}

// TestMaybeAutoSummarize_RestartResumesWithSameSummary covers the operator-level
// requirement: the DB is written first, so a server restarted at any point rebuilds
// the same context — including the newest summary — from the persisted rows.
func TestMaybeAutoSummarize_RestartResumesWithSameSummary(t *testing.T) {
	chdirRepoRoot(t)
	te, setup, lc, proj := summarizeSetup(t, "SUMMARY-PERSISTED")

	te.summarize.maybeAutoSummarize(setup, lc, "test-model", true)

	rows, err := te.store.GetTaskMessages(setup.task.ID, "thread-1")
	if err != nil {
		t.Fatalf("get task messages: %v", err)
	}
	summaryRow, proceedRow := -1, -1
	for i, m := range rows {
		switch {
		case m.Role == "summarization":
			summaryRow = i
		case m.Role == "user" && m.Content == "Proceed":
			proceedRow = i
		}
	}
	if summaryRow < 0 {
		t.Fatal("summarization row was not persisted")
	}
	if proceedRow >= 0 && proceedRow < summaryRow {
		t.Fatalf("Proceed row (index %d) precedes the summary row (index %d): a restart between them loses the summary", proceedRow, summaryRow)
	}

	// Fresh process: rebuild the conversation from the DB, exactly as a restart does.
	reloaded, err := te.store.GetTaskByID(setup.task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	restarted := buildLLMContext(buildMessagesFromDB(reloaded, rows, setup.worktreePath, proj.Name, false))
	if len(restarted) == 0 || restarted[0].Role != "system" {
		t.Fatalf("rebuilt context has no system message: %+v", restarted)
	}
	if !strings.Contains(restarted[0].Content, "SUMMARY-PERSISTED") {
		t.Fatalf("rebuilt system message lacks the summary:\n%s", restarted[0].Content)
	}
	// The live prompt and the rebuilt one must carry the same summary.
	if !strings.Contains(lc.messages[0].Content, "SUMMARY-PERSISTED") {
		t.Fatal("live system message lacks the summary the restart would load")
	}
}

// TestComposeSystemPrompt_SandboxCwd guards the extraction of the prompt assembly:
// a sandboxed task sees the container mount as its working directory while the host
// worktree path is not advertised as the cwd, and the host path survives in the
// non-sandboxed prompt.
func TestComposeSystemPrompt_SandboxCwd(t *testing.T) {
	chdirRepoRoot(t)
	task := &types.Task{OperatingMode: "act"}

	host := composeSystemPrompt(task, "/host/worktree", "proj", false, "S")
	if !strings.Contains(host, "/host/worktree") {
		t.Fatalf("host prompt lost the worktree path:\n%s", host)
	}
	if strings.Contains(host, "/home/sandbox/") {
		t.Fatalf("non-sandboxed prompt mentions the sandbox mount:\n%s", host)
	}

	sandboxed := composeSystemPrompt(task, "/host/worktree", "proj", true, "S")
	if !strings.Contains(sandboxed, "/home/sandbox/proj") {
		t.Fatalf("sandboxed prompt lacks the container cwd:\n%s", sandboxed)
	}
	if strings.Contains(sandboxed, "Current working directory: /host/worktree") {
		t.Fatalf("sandboxed prompt advertises the host path as the cwd:\n%s", sandboxed)
	}
}
