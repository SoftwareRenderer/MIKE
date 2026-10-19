package exec

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mike/backend/types"
)

// fittedRequest is the messages array the provider was handed on one call.
type fittedRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func (f fittedRequest) messages() []types.ChatMessage {
	out := make([]types.ChatMessage, 0, len(f.Messages))
	for _, m := range f.Messages {
		out = append(out, types.ChatMessage{Role: m.Role, Content: m.Content})
	}
	return out
}

func (f fittedRequest) toolOutput() string {
	out := ""
	for _, m := range f.Messages {
		if m.Role == "tool" {
			out = m.Content
		}
	}
	return out
}

// fittedReadRun runs one task that reads a file the given estimated size from a model with the given context
// window, and returns the request the provider saw after that read. reportedPromptTokens is what the provider
// claimed its earlier call cost, or 0 when it reported no usage.
func fittedReadRun(t *testing.T, taskID, file string, tokens, window, reportedPromptTokens int) (fittedRequest, *TaskExecutor) {
	t.Helper()
	te, _, projectID, initialSHA := setupExecutorWithGit(t)

	var mu sync.Mutex
	var second fittedRequest
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"stream":true`) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"a summary"}}]}`))
			return
		}
		if calls.Add(1) == 1 {
			if reportedPromptTokens > 0 {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, `data: {"choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":8}}`+"\n\n", reportedPromptTokens)
			}
			sseToolCall(w, "call_1", file)
			return
		}
		var req fittedRequest
		_ = json.Unmarshal(raw, &req)
		mu.Lock()
		second = req
		mu.Unlock()
		sseText(w, "is that all of it?")
	}))
	t.Cleanup(srv.Close)

	worktreePath := crashTask(t, te, projectID, initialSHA, taskID, srv.URL)
	if err := os.WriteFile(filepath.Join(worktreePath, file), []byte(filler(tokens, "aa")), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	// The window the run reads at startup comes from a provider row naming this model.
	providers := fmt.Sprintf(`[{"id":1,"kind":"llm","url":%q,"models":[{"name":"test-model","context_window":%d}],"concurrency":1}]`, srv.URL, window)
	if err := te.store.SetConfigValue("providers", providers); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	fastRetryLadder(te)

	startExecRun(t, te, projectID, taskID, srv.URL, worktreePath)

	deadline := time.Now().Add(60 * time.Second)
	for {
		mu.Lock()
		got := len(second.Messages) > 0
		mu.Unlock()
		if got || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(second.Messages) == 0 {
		t.Fatal("the run never made a second call")
	}
	return second, te
}

// A run that reads past its window must be handed a trimmed result, not a provider that refuses the
// request and fails the task.
func TestRunTrimsAnOversizedReadBeforeTheProviderSeesIt(t *testing.T) {
	chdirRepoRoot(t)
	const taskID = "fit-run"
	const window = 16000

	second, te := fittedReadRun(t, taskID, "big.txt", 40000, window, 0)

	sent := second.toolOutput()
	if sent == "" {
		t.Fatal("the second call carried no tool result")
	}
	if !strings.Contains(sent, "trimmed to") {
		t.Errorf("the provider was handed the whole read: %d bytes of tool output", len(sent))
	}
	budget := promptBudget(window, defaultSummarizeThreshold)
	if over := estimatePromptTokens(second.messages()) - budget; over > 0 {
		t.Errorf("the request the provider received is %d tokens over its budget of %d", over, budget)
	}
	// The reviewer keeps what the tool returned: the stored row holds the whole output.
	full := false
	for _, m := range mustMessages(t, te, taskID, "thread-"+taskID) {
		if m.Role == "tool_call" && len(m.ToolResult) > len(sent) {
			full = true
		}
	}
	if !full {
		t.Errorf("no stored tool_call row holds more than the %d bytes the model was sent", len(sent))
	}
}

// The provider counted the conversation larger than a word count can price it, and its number is the one
// the next result must be trimmed against.
func TestRunTrimsWhatTheReportedUsagePutOverBudget(t *testing.T) {
	chdirRepoRoot(t)
	const window = 16000
	const reportedPrompt = 13000

	// The file is small enough that the estimate alone says the request fits: only the reported count puts it
	// over the budget.
	second, _ := fittedReadRun(t, "fit-run-usage", "long.txt", 2000, window, reportedPrompt)

	if sent := second.toolOutput(); !strings.Contains(sent, "trimmed to") {
		t.Errorf("the provider was handed the whole result: it had priced the conversation at %d of a %d token budget",
			reportedPrompt, promptBudget(window, defaultSummarizeThreshold))
	}
}
