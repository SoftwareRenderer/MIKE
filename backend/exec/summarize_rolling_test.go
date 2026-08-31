package exec

// Rolling-summarization tests.
//
// The contract: a summary folds the *previous* summary together with the messages
// that came after it — never the whole conversation. The full list is what the loop
// holds (lc.messages only grows; compression happens per call in buildLLMContext), so
// summarizeContext must compress before rendering. Passing the raw list made every
// pass re-read every message ever, which (a) grew the summarizer's prompt with the
// conversation, (b) made summarization the one call whose input was never compressed,
// and (c) guaranteed failure on a utility model with a smaller window — including the
// manual /summarize path, leaving the task unsummarizable.
//
// Also covered: environment notes (advisories) must survive into the summary, because
// rolling summarization makes the summary the only copy carried forward.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"kanban/backend/types"
)

// summaryLLM records every request body it serves and answers each call with the next
// summary in a numbered sequence, so consecutive passes are distinguishable.
type summaryLLM struct {
	mu     sync.Mutex
	bodies []string
	server *httptest.Server
}

func newSummaryLLM() *summaryLLM {
	c := &summaryLLM{}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		n := len(c.bodies)
		c.bodies = append(c.bodies, string(body))
		c.mu.Unlock()

		encoded, _ := json.Marshal(fmt.Sprintf("GENERATED-SUMMARY-%d", n+1))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, encoded)
	}))
	return c
}

func (c *summaryLLM) requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...)
}

func (c *summaryLLM) last(t *testing.T) string {
	t.Helper()
	reqs := c.requests()
	if len(reqs) == 0 {
		t.Fatal("summarization LLM was never called")
	}
	return reqs[len(reqs)-1]
}

func (c *summaryLLM) close() { c.server.Close() }

// rollingSetup builds the wiring maybeAutoSummarize needs, with the summarization
// threshold already crossed (9200 of a 10000-token window against the 90% default).
func rollingSetup(t *testing.T, llm *summaryLLM) (*TaskExecutor, *taskExecSetup, *loopContext) {
	t.Helper()
	te := newTestExecutor(t)
	te.gate = NewConcurrencyGate()
	s := te.store
	proj := createProject(t, s, "proj-rolling-"+t.Name(), "Rolling Sum")

	now := time.Now().UTC()
	task := &types.Task{
		ID: "roll-" + t.Name(), ProjectID: proj.ID, Title: "Rolling summary",
		Status: "review", ExecutionState: "idle", OperatingMode: "act",
		LlamaServerURL: llm.server.URL, ModelName: "test-model",
		CurrentThreadID: "thread-1", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	cw := 10000
	lc := &loopContext{
		messages: []types.ChatMessage{
			{Role: "system", Content: "SYSTEM PROMPT"},
		},
		contextWindow: &cw,
		usage:         &types.ChatUsage{PromptTokens: 9200},
		detector:      newLoopDetector(),
		mc:            &modelConfig{serverURL: llm.server.URL, modelName: "test-model"},
	}
	setup := &taskExecSetup{
		task: task, project: proj, projectID: proj.ID, ctx: context.Background(),
		serverURL: llm.server.URL, modelName: "test-model", worktreePath: t.TempDir(),
	}
	return te, setup, lc
}

func mustNotContain(t *testing.T, body, label string, probes ...string) {
	t.Helper()
	for _, p := range probes {
		if strings.Contains(body, p) {
			t.Errorf("%s must not contain %q — it is already represented by the previous summary\n--- request ---\n%s", label, p, body)
		}
	}
}

func mustContain(t *testing.T, body, label string, probes ...string) {
	t.Helper()
	for _, p := range probes {
		if !strings.Contains(body, p) {
			t.Errorf("%s must contain %q\n--- request ---\n%s", label, p, body)
		}
	}
}

// TestSummarizeContext_UsesCompressedHistoryOnly is the core regression: with a
// summarization marker in the middle of the list, the input must be the previous
// summary plus the messages after the marker — nothing older.
func TestSummarizeContext_UsesCompressedHistoryOnly(t *testing.T) {
	chdirRepoRoot(t)
	llm := newSummaryLLM()
	defer llm.close()
	te, _, _ := rollingSetup(t, llm)

	messages := []types.ChatMessage{
		{Role: "system", Content: "SYSTEM PROMPT TEXT"},
		{Role: "user", Content: "FIRST-REQUEST-UNIQUETOKEN"},
		{Role: "assistant", Content: "FIRST-WORK-UNIQUETOKEN"},
		{Role: "summarization", Content: "PREVIOUS-SUMMARY-UNIQUETOKEN"},
		{Role: "user", Content: "SECOND-REQUEST-UNIQUETOKEN"},
		{Role: "assistant", Content: "SECOND-WORK-UNIQUETOKEN"},
	}

	if _, err := te.summarizeContext(context.Background(), messages, llm.server.URL, "test-model", extractPrevSummary(messages)); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	body := llm.last(t)
	mustContain(t, body, "summarization input", "SECOND-REQUEST-UNIQUETOKEN", "SECOND-WORK-UNIQUETOKEN", "PREVIOUS-SUMMARY-UNIQUETOKEN")
	mustNotContain(t, body, "summarization input", "FIRST-REQUEST-UNIQUETOKEN", "FIRST-WORK-UNIQUETOKEN", "SYSTEM PROMPT TEXT")

	// The previous summary arrives in its own section and the marker row is dropped by
	// buildLLMContext, so it must appear exactly once (never both inline and as the
	// summary of a prior pass).
	if n := strings.Count(body, "PREVIOUS-SUMMARY-UNIQUETOKEN"); n != 1 {
		t.Errorf("previous summary sent %d times, want exactly 1", n)
	}
}

// TestMaybeAutoSummarize_SecondPassFoldsSummaryPlusTail is the end-to-end version:
// two auto-summarizations in one run. The second must summarize "previous summary +
// messages since the previous summary", never the whole conversation.
func TestMaybeAutoSummarize_SecondPassFoldsSummaryPlusTail(t *testing.T) {
	chdirRepoRoot(t)
	llm := newSummaryLLM()
	defer llm.close()
	te, setup, lc := rollingSetup(t, llm)

	lc.messages = append(lc.messages,
		types.ChatMessage{Role: "user", Content: "ROUND-ONE-REQUEST-UNIQUETOKEN"},
		types.ChatMessage{Role: "assistant", Content: "ROUND-ONE-WORK-UNIQUETOKEN"},
	)

	te.maybeAutoSummarize(setup, lc, "test-model", true)

	first := llm.last(t)
	mustContain(t, first, "first pass", "ROUND-ONE-REQUEST-UNIQUETOKEN")
	if len(llm.requests()) != 1 {
		t.Fatalf("expected one summarization call, got %d", len(llm.requests()))
	}

	// The summary reached the system prompt, and the conversation kept growing.
	if !strings.Contains(lc.messages[0].Content, "GENERATED-SUMMARY-1") {
		t.Fatalf("system prompt does not carry the first summary:\n%.300s", lc.messages[0].Content)
	}
	lc.messages = append(lc.messages,
		types.ChatMessage{Role: "user", Content: "ROUND-TWO-REQUEST-UNIQUETOKEN"},
		types.ChatMessage{Role: "assistant", Content: "ROUND-TWO-WORK-UNIQUETOKEN"},
	)

	// Requirement 1, before the second pass: the model gets the summary plus every
	// message that came after it — nothing older, and no marker row.
	between := buildLLMContext(lc.messages)
	if !strings.Contains(between[0].Content, "GENERATED-SUMMARY-1") {
		t.Fatalf("system message must carry the newest summary: %.200s", between[0].Content)
	}
	for _, m := range between {
		if m.Role == "summarization" {
			t.Error("summarization marker leaked into the LLM context")
		}
		if strings.Contains(m.Content, "ROUND-ONE-") {
			t.Errorf("pre-summary message survived into the LLM context: %.80s", m.Content)
		}
	}
	for _, want := range []string{"Proceed", "ROUND-TWO-REQUEST-UNIQUETOKEN", "ROUND-TWO-WORK-UNIQUETOKEN"} {
		if !containsAny(between, want) {
			t.Errorf("messages since the summary must be kept, missing %q", want)
		}
	}

	te.maybeAutoSummarize(setup, lc, "test-model", true)

	if len(llm.requests()) != 2 {
		t.Fatalf("expected two summarization calls, got %d", len(llm.requests()))
	}
	second := llm.last(t)
	mustContain(t, second, "second pass",
		"GENERATED-SUMMARY-1",           // folded forward as the previous summary
		"ROUND-TWO-REQUEST-UNIQUETOKEN", // and the messages since that summary
		"ROUND-TWO-WORK-UNIQUETOKEN",
		"User: Proceed", // the injected continuation from the first pass
	)
	mustNotContain(t, second, "second pass", "ROUND-ONE-REQUEST-UNIQUETOKEN", "ROUND-ONE-WORK-UNIQUETOKEN")

	// After the second pass the newest summary replaces what it folded away, and the
	// tail restarts (only the injected continuation is in it).
	after := buildLLMContext(lc.messages)
	if !strings.Contains(after[0].Content, "GENERATED-SUMMARY-2") {
		t.Fatalf("system message must carry the newest summary: %.200s", after[0].Content)
	}
	for _, m := range after {
		if m.Role == "summarization" {
			t.Error("summarization marker leaked into the LLM context")
		}
		if strings.Contains(m.Content, "ROUND-") {
			t.Errorf("folded-away message survived into the LLM context: %.80s", m.Content)
		}
	}
}

// containsAny reports whether any message carries the fragment.
func containsAny(messages []types.ChatMessage, fragment string) bool {
	for _, m := range messages {
		if strings.Contains(m.Content, fragment) {
			return true
		}
	}
	return false
}

// TestFormatChatHistory_AdvisoryKeepsItsFindings is the regression for the
// first-line-only note: for a diagnostics advisory the first line is the header, so
// the summarizer was handed "…(2 files with findings):" and every finding was dropped.
// With rolling summarization that stub would be the only copy carried forward.
func TestFormatChatHistory_AdvisoryKeepsItsFindings(t *testing.T) {
	note := "LSP diagnostics for this turn's changes (1 file with findings):\n\n" +
		"Fix issues in a.go:\n" +
		"1. line 12, col 5 [compiler]: undefined: widget (error)\n" +
		"2. line 40, col 2 [staticcheck]: S1002 should use return (warning)"

	hist := formatChatHistory([]types.ChatMessage{
		{Role: "system", Content: "SYSTEM"},
		{Role: "user", Content: "wire it up"},
		{Role: "advisory", Content: note},
	}, false)

	mustContain(t, hist, "advisory in summarization input",
		"undefined: widget", "S1002 should use return", "Fix issues in a.go")
	if !strings.Contains(hist, "Environment note:") {
		t.Errorf("advisory must still be labelled as an environment note:\n%s", hist)
	}
}

// TestFormatChatHistory_AdvisoryIsNotTruncated pins the decision that an environment
// note is carried into the summarization input whole. A diagnostics advisory can run to
// hundreds of findings, so this is a deliberate trade: the summary keeps the detail at
// the cost of a summarization prompt that grows with the number of findings. The volume
// is bounded upstream instead, by maxDiagnosticFilesPerPass.
func TestFormatChatHistory_AdvisoryIsNotTruncated(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("LSP diagnostics for this turn's changes (1 file with findings):\n\n")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, "%d. line %d, col 3 [compiler]: undefined: symbol%03d (error)\n", i+1, i*7+1, i)
	}

	hist := formatChatHistory([]types.ChatMessage{
		{Role: "system", Content: "SYSTEM"},
		{Role: "advisory", Content: sb.String()},
	}, false)

	if !strings.Contains(hist, "symbol499") {
		t.Error("the last finding was cut off — the note must be carried in full")
	}
	if strings.Contains(hist, "truncated") {
		t.Error("summarization input truncated an environment note")
	}
}
