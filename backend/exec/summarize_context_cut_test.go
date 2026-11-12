package exec

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mike/backend/types"

	"github.com/google/uuid"
)

// mockSummarizeLLMAnswersWith answers the summarization call with summary. It runs onCall while that call
// is in flight, which is the one moment a send can land in the middle of a compaction.
func mockSummarizeLLMAnswersWith(summary string, onCall func()) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		onCall()
		encoded, _ := json.Marshal(summary)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, encoded)
	}))
}

// summarizationIndex is where the marker row of a forced compaction sits.
func summarizationIndex(t *testing.T, rows []types.TaskMessage) int {
	t.Helper()
	for i, m := range rows {
		if m.Role == "summarization" {
			return i
		}
	}
	t.Fatalf("no summarization row among %+v", rolesOf(rows))
	return -1
}

// cutNoteIndex is where the note explaining the cut sits.
func cutNoteIndex(t *testing.T, rows []types.TaskMessage) int {
	t.Helper()
	for i, m := range rows {
		if m.Role == "advisory" && m.Kind == advisoryContextCut {
			return i
		}
	}
	t.Fatalf("no %s advisory row among %+v", advisoryContextCut, rolesOf(rows))
	return -1
}

// A compaction is one event. Nothing belongs between its marker and the note explaining the cut, not even
// a message that arrived while the summary was being answered, so the note keeps the marker's instant
// rather than the clock time it happens to be written at.
func TestContextCutNoteIsReadBesideItsMarker(t *testing.T) {
	chdirRepoRoot(t)
	te, setup, lc, _ := summarizeSetup(t)

	server := mockSummarizeLLMAnswersWith("SUMMARY-BESIDE-MARKER", func() {
		if err := te.store.CreateTaskMessage(&types.TaskMessage{
			ID: uuid.New().String(), TaskID: setup.task.ID, ThreadID: "thread-1",
			Role: "user", Content: "sent while the summary was being answered", CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Errorf("persist the mid-compaction message: %v", err)
		}
	})
	defer server.Close()
	lc.mc.serverURL = server.URL

	te.summarize.maybeAutoSummarize(setup, lc, "test-model", true)

	rows := mustMessages(t, te, setup.task.ID, "thread-1")
	marker, note := summarizationIndex(t, rows), cutNoteIndex(t, rows)
	if note != marker+1 {
		t.Errorf("the cut note is row %d and its marker row %d, so %+v sit between them: a compaction and the "+
			"note that explains it are read apart",
			note, marker, rolesOf(rows[marker+1:note]))
	}
}

// A compaction moves anything that arrived during it to after its marker. It must not move a row that
// predates it, because the turn the cut interrupted keeps its place above the marker.
func TestSummarizationRedatesNothingThatPredatesIt(t *testing.T) {
	chdirRepoRoot(t)
	te, setup, lc, _ := summarizeSetup(t, "SUMMARY-NO-REDATE")

	// Persist the turn the cut is about to interrupt, the way the loop persists it moments beforehand.
	turn := []types.TaskMessage{
		{ID: uuid.New().String(), Role: "assistant", Content: "reading the flaky test"},
		{ID: uuid.New().String(), Role: "tool_call", Content: "the read that filled the window"},
	}
	for i := range turn {
		turn[i].TaskID = setup.task.ID
		turn[i].ThreadID = "thread-1"
		turn[i].CreatedAt = time.Now().UTC()
		if err := te.store.CreateTaskMessage(&turn[i]); err != nil {
			t.Fatalf("persist the interrupted turn: %v", err)
		}
	}

	te.summarize.maybeAutoSummarize(setup, lc, "test-model", true)

	rows := mustMessages(t, te, setup.task.ID, "thread-1")
	marker := summarizationIndex(t, rows)
	for _, m := range turn {
		if at := indexOfID(rows, m.ID); at < 0 || at >= marker {
			t.Errorf("the row (%s) written before the compaction reads at row %d of %+v, not above its marker at %d",
				m.Role, at, rolesOf(rows), marker)
		}
	}
}

// indexOfID is where a row sits in the read, or -1 when the read does not hold it.
func indexOfID(rows []types.TaskMessage, id string) int {
	for i, m := range rows {
		if m.ID == id {
			return i
		}
	}
	return -1
}

// The turns a summary forced by the context limit drops were work in progress, so the run keeps them: the
// note is stored where a resumed run reads it back, after the summary that replaced them.
func TestContextCutNoteIsStoredAfterTheSummaryItExplains(t *testing.T) {
	chdirRepoRoot(t)
	te, setup, lc, proj := summarizeSetup(t, "SUMMARY-AFTER-CUT")
	lc.messages = append(lc.messages,
		types.ChatMessage{Role: "assistant", Content: "reading the flaky test",
			ReasoningContent: "the retry is in the fixture, so START-OF-THOUGHT then the fixture",
			ToolCalls:        []types.ToolCallMsg{tailCall("c1", types.ToolReadFile, `{"path":"flaky_test.go"}`)}},
		types.ChatMessage{Role: "tool", ToolCallID: "c1", Content: "the read that filled the window"},
	)

	te.summarize.maybeAutoSummarize(setup, lc, "test-model", true)

	rows := mustMessages(t, te, setup.task.ID, "thread-1")
	summaryIdx, noteIdx := summarizationIndex(t, rows), cutNoteIndex(t, rows)
	if noteIdx < summaryIdx {
		t.Fatalf("the cut note (row %d) sits before the summary (row %d): the read that opens at the summary would lose it", noteIdx, summaryIdx)
	}
	note := rows[noteIdx].Content
	for _, want := range []string{"context limit", "interrupted mid-task", "flaky_test.go", "the read that filled the window"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not reproduce %q:\n%s", want, note)
		}
	}

	// A resumed run reads the thread from its newest summarization row: the note has to be in what it gets.
	reloaded, err := te.store.GetTaskByID(setup.task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	rebuilt, _, _ := te.conversationFromDB(reloaded, setup.worktreePath, promptScopeOf(proj))
	if len(rebuilt) < 2 || rebuilt[0].Role != "system" {
		t.Fatalf("rebuilt conversation has no system prompt: %+v", rebuilt)
	}
	resumed := buildLLMContext(rebuilt)
	if len(resumed) < 2 || resumed[1].Role != "advisory" || !strings.Contains(resumed[1].Content, "flaky_test.go") {
		t.Errorf("the resumed run does not get the cut point: %+v", resumed)
	}
}

// Pressing Summarize interrupts nothing: the same call without the cause records no note. This is the code
// both manual paths reach — the running loop's channel and the DB read for an idle task.
func TestManualSummarizationRecordsNoCutNote(t *testing.T) {
	chdirRepoRoot(t)
	te, setup, lc, _ := summarizeSetup(t, "MANUAL-SUMMARY")
	lc.messages = append(lc.messages, types.ChatMessage{Role: "assistant", Content: "TURN-IN-PROGRESS"})

	messages, summary, _ := te.summarize.applySummarization(setup, setup.ctx, lc.messages, setup.serverURL, "test-model", nil, false)

	if summary == "" {
		t.Fatal("the manual summarization installed nothing")
	}
	for _, m := range messages {
		if m.Role == "advisory" {
			t.Errorf("a summary nobody was cut off by wrote a note: %.120s", m.Content)
		}
	}
	for _, m := range mustMessages(t, te, setup.task.ID, "thread-1") {
		if m.Role == "advisory" {
			t.Errorf("a cut note was persisted for a manual summarization (row %s)", m.ID)
		}
	}
}
