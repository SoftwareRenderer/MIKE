package exec

import (
	"strings"
	"testing"

	"mike/backend/types"
)

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
	summaryIdx, noteIdx := -1, -1
	for i, m := range rows {
		switch {
		case m.Role == "summarization":
			summaryIdx = i
		case m.Role == "advisory" && m.Kind == advisoryContextCut:
			noteIdx = i
		}
	}
	if summaryIdx < 0 {
		t.Fatal("no summarization row was written")
	}
	if noteIdx < 0 {
		t.Fatalf("no %s advisory row was written; rows: %+v", advisoryContextCut, rolesInOrder(t, te, setup.task.ID, "thread-1"))
	}
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

	messages, summary := te.summarize.applySummarization(setup, setup.ctx, lc.messages, setup.serverURL, "test-model", nil, false)

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
