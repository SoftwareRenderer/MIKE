package exec

// The context number a summarization publishes. A task compressed while idle runs no further turn,
// so the frame's `context_used` is the only thing that moves the meter.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mike/backend/types"

	"github.com/google/uuid"
)

// fillerWords is a user message whose own token estimate dwarfs any system prompt, which is what
// makes "the folded-away conversation is no longer counted" measurable.
const fillerWords = 3000

func fillerMessage() string { return strings.Repeat("filler ", fillerWords) }

// summarizationFrameOf replays the task's frame buffer and returns its summarization frame. The
// summary row is durable before the frame reaches the buffer, so one replay can arrive early and
// re-reading is the only wait that is honest here. Each replay already ends on 500 ms of a quiet
// socket, which paces the retry; the deadline bounds how long an absent frame costs the suite.
func summarizationFrameOf(t *testing.T, te *TaskExecutor, projectID, taskID string) taskStreamFrame {
	t.Helper()
	var events []string
	deadline := time.Now().Add(6 * time.Second)
	for {
		frames := taskStreamFrames(t, te.appStreamMgr, projectID, taskID)
		if frame, ok := findTaskStreamEvent(frames, string(types.WSTypeSummarization)); ok {
			return frame
		}
		events = events[:0]
		for _, f := range frames {
			events = append(events, f.Event)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no summarization frame reached the task stream within 6s; the last replay held %v", events)
		}
	}
}

func contextUsedOf(t *testing.T, frame taskStreamFrame) float64 {
	t.Helper()
	used, ok := frame.Data["context_used"].(float64)
	if !ok {
		t.Fatalf("summarization frame carries no numeric context_used: %v", frame.Data)
	}
	return used
}

func lastSummaryRow(t *testing.T, te *TaskExecutor, taskID, threadID string) *types.TaskMessage {
	t.Helper()
	msgs, err := te.store.GetTaskMessages(taskID, threadID)
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	var last *types.TaskMessage
	for i, m := range msgs {
		if m.Role == "summarization" {
			last = &msgs[i]
		}
	}
	if last == nil {
		t.Fatal("no summarization row was persisted")
	}
	return last
}

// TestManualSummarizePublishesCompressedContextUsed: the idle task a person compresses by hand,
// where no later turn moves the meter.
func TestManualSummarizePublishesCompressedContextUsed(t *testing.T) {
	chdirRepoRoot(t)
	llm := newSummaryLLM()
	defer llm.close()
	te, _, projectID, _ := setupExecutorWithGit(t)
	te.gate = NewConcurrencyGate()
	te.summarize.blockedMessages.tasks = make(map[string]struct{})
	te.summarize.dbSummarizeCancel.tasks = make(map[string]context.CancelFunc)

	taskID := "sum-db-used-" + t.Name()
	now := time.Now().UTC()
	if err := te.store.CreateTask(&types.Task{
		ID: taskID, ProjectID: projectID, Title: "Compress the meter", Prompt: "Do the thing",
		Status: types.StatusBacklog, ExecutionState: types.ExecStateIdle, OperatingMode: "act",
		LlamaServerURL: llm.server.URL, ModelName: "test-model", CurrentThreadID: "thread-1",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	conversation := fillerMessage()
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, ThreadID: "thread-1",
		Role: "user", Content: conversation, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	// The DB path answers the browser first and summarizes in the background, so the wait is on the
	// row it persists.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/summarize", te.HandleSummarize)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/summarize", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("summarize endpoint returned %d: %s", rec.Code, rec.Body.String())
	}
	waitFor(t, 20*time.Second, "the manual summarization to persist its summary", func() bool {
		msgs, err := te.store.GetTaskMessages(taskID, "thread-1")
		if err != nil {
			return false
		}
		for _, m := range msgs {
			if m.Role == "summarization" {
				return true
			}
		}
		return false
	})

	frame := summarizationFrameOf(t, te, projectID, taskID)
	used := contextUsedOf(t, frame)
	summary := lastSummaryRow(t, te, taskID, "thread-1")

	if used <= 0 {
		t.Errorf("context_used = %v, want a usage the meter can show", used)
	}
	if folded := float64(estimateTokensFromText(conversation)); used >= folded {
		t.Errorf("context_used = %v, want under the %v tokens the folded-away message alone is — the meter must show the compressed window", used, folded)
	}
	if worth := float64(estimateTokensFromText(summary.Content)); used <= worth {
		t.Errorf("context_used = %v, want over the %v tokens the summary is worth — it rides in the prompt the next call sends", used, worth)
	}
}

// TestAutoSummarizePublishesCompressedContextUsed: the mid-turn case, where the figure after the
// compression must be the smaller one.
func TestAutoSummarizePublishesCompressedContextUsed(t *testing.T) {
	chdirRepoRoot(t)
	llm := newSummaryLLM()
	defer llm.close()
	te, setup, lc := rollingSetup(t, llm)

	conversation := fillerMessage()
	lc.messages = append(lc.messages, types.ChatMessage{Role: "user", Content: conversation})
	before := contextTokensUsed(lc)

	te.summarize.maybeAutoSummarize(setup, lc, "test-model", false)

	frame := summarizationFrameOf(t, te, setup.projectID, setup.task.ID)
	used := contextUsedOf(t, frame)
	summary := lastSummaryRow(t, te, setup.task.ID, setup.task.CurrentThreadID)

	if used <= 0 {
		t.Errorf("context_used = %v, want a usage the meter can show", used)
	}
	if used >= float64(before) {
		t.Errorf("context_used = %v, want under the %d tokens used before compressing", used, before)
	}
	if folded := float64(estimateTokensFromText(conversation)); used >= folded {
		t.Errorf("context_used = %v, want under the %v tokens the folded-away message alone is", used, folded)
	}
	if worth := float64(estimateTokensFromText(summary.Content)); used <= worth {
		t.Errorf("context_used = %v, want over the %v tokens the summary is worth", used, worth)
	}
}
