package exec

// Rebuild tests for buildMessagesFromDB: a tool_call row belongs to the assistant
// message that issued it, never to whatever row happens to be adjacent. Advisory rows
// written during tool execution must not be able to swallow a turn's tool calls or
// their results.

import (
	"encoding/json"
	"slices"
	"testing"

	"mike/backend/types"
)

// toolRow builds a tool_call row as the loop persists it: invocation JSON plus the result
// JSON written after the tool finished.
func toolRow(t *testing.T, owner, id, name, args, output string) types.TaskMessage {
	t.Helper()
	inv, err := json.Marshal(map[string]any{"tool_id": id, "tool_name": name, "raw_args": args})
	if err != nil {
		t.Fatalf("marshal invocation: %v", err)
	}
	m := types.TaskMessage{Role: "tool_call", ToolCalls: string(inv), AssistantMsgID: owner}
	if output != "" {
		res, err := json.Marshal(map[string]any{"output": output, "success": true})
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		m.ToolResult = string(res)
	}
	return m
}

func rebuildRows(rows []types.TaskMessage) []types.ChatMessage {
	task := &types.Task{ID: "rebuild-task", OperatingMode: "act", CurrentThreadID: "th"}
	return buildMessagesFromDB(task, rows, "/host/wt", "proj", false)
}

func summarizeRebuild(msgs []types.ChatMessage) (roles []string, toolCallIDs, toolResultIDs []string) {
	for _, m := range msgs {
		roles = append(roles, m.Role)
		for _, tc := range m.ToolCalls {
			toolCallIDs = append(toolCallIDs, tc.ID)
		}
		if m.Role == "tool" {
			toolResultIDs = append(toolResultIDs, m.ToolCallID)
		}
	}
	return
}

// TestBuildMessagesFromDB_AdvisoryRowKeepsToolRows is the regression test for the turn
// that lost its tail: a row written between two tool_call rows used to end the assistant
// row's look-ahead, so the second call and BOTH results vanished from the rebuilt
// conversation (and the assistant was rebuilt claiming it had only called A).
func TestBuildMessagesFromDB_AdvisoryRowKeepsToolRows(t *testing.T) {
	t.Parallel()
	const owner = "asst-1"
	rows := []types.TaskMessage{
		{ID: "u1", Role: "user", Content: "fix it"},
		{ID: owner, Role: "assistant", Content: "editing two files"},
		toolRow(t, owner, "A", "edit_file", `{"path":"a.ts"}`, "edited a.ts"),
		{ID: "hint", Role: "user", Content: "LSP: 2 errors in a.ts"},
		toolRow(t, owner, "B", "edit_file", `{"path":"b.ts"}`, "edited b.ts"),
	}

	roles, calls, results := summarizeRebuild(rebuildRows(rows))
	if !slices.Equal(calls, []string{"A", "B"}) {
		t.Fatalf("assistant tool calls = %v, want [A B]", calls)
	}
	if !slices.Equal(results, []string{"A", "B"}) {
		t.Fatalf("tool results = %v, want [A B]", results)
	}
	// The advisory keeps its place in the narrative: after the work it comments on.
	if !slices.Equal(roles, []string{"system", "user", "assistant", "tool", "tool", "user"}) {
		t.Fatalf("roles = %v", roles)
	}
}

// TestBuildMessagesFromDB_ContiguousTurnUnchanged pins the ordinary case: nothing about
// a well-ordered turn moves.
func TestBuildMessagesFromDB_ContiguousTurnUnchanged(t *testing.T) {
	t.Parallel()
	const owner = "asst-1"
	rows := []types.TaskMessage{
		{ID: "u1", Role: "user", Content: "fix it"},
		{ID: owner, Role: "assistant", Content: "editing"},
		toolRow(t, owner, "A", "bash", `{"command":"go test"}`, "ok"),
		toolRow(t, owner, "B", "read_file", `{"path":"x.go"}`, "package main"),
		{ID: "u2", Role: "user", Content: "and now?"},
	}

	roles, calls, results := summarizeRebuild(rebuildRows(rows))
	if !slices.Equal(calls, []string{"A", "B"}) || !slices.Equal(results, []string{"A", "B"}) {
		t.Fatalf("calls=%v results=%v", calls, results)
	}
	if !slices.Equal(roles, []string{"system", "user", "assistant", "tool", "tool", "user"}) {
		t.Fatalf("roles = %v", roles)
	}
}

// TestBuildMessagesFromDB_LegacyRowsAttachToPrecedingAssistant covers rows written before
// assistant_msg_id existed: they must land on the assistant above them even when an
// advisory row sits in between, which is exactly what the old adjacency scan got wrong.
func TestBuildMessagesFromDB_LegacyRowsAttachToPrecedingAssistant(t *testing.T) {
	t.Parallel()
	rows := []types.TaskMessage{
		{ID: "u1", Role: "user", Content: "fix it"},
		{ID: "asst-1", Role: "assistant", Content: "editing"},
		toolRow(t, "", "A", "edit_file", `{"path":"a.ts"}`, "edited a.ts"),
		{ID: "marker", Role: "system", Content: "Merge conflict detected — resolving with tools"},
		toolRow(t, "", "B", "edit_file", `{"path":"b.ts"}`, "edited b.ts"),
	}

	_, calls, results := summarizeRebuild(rebuildRows(rows))
	if !slices.Equal(calls, []string{"A", "B"}) {
		t.Fatalf("legacy tool calls = %v, want [A B]", calls)
	}
	if !slices.Equal(results, []string{"A", "B"}) {
		t.Fatalf("legacy tool results = %v, want [A B]", results)
	}
}

// TestBuildMessagesFromDB_OrphanToolRowsIgnored: a tool_call row with no assistant row
// before it and no owner id belongs to nothing — it must not be attached to a later turn.
func TestBuildMessagesFromDB_OrphanToolRowsIgnored(t *testing.T) {
	t.Parallel()
	rows := []types.TaskMessage{
		{ID: "u1", Role: "user", Content: "fix it"},
		toolRow(t, "", "A", "bash", `{}`, "orphaned output"),
		{ID: "asst-1", Role: "assistant", Content: "later turn"},
	}

	roles, calls, results := summarizeRebuild(rebuildRows(rows))
	if len(calls) != 0 || len(results) != 0 {
		t.Fatalf("orphan row was attached: calls=%v results=%v", calls, results)
	}
	if !slices.Equal(roles, []string{"system", "user", "assistant"}) {
		t.Fatalf("roles = %v", roles)
	}
}

// TestBuildMessagesFromDB_LoopHintsExcludedKeepsGrouping: loop hints are UI-only, but
// excluding them must not cost the turn its tool rows either.
func TestBuildMessagesFromDB_LoopHintsExcludedKeepsGrouping(t *testing.T) {
	t.Parallel()
	const owner = "asst-1"
	rows := []types.TaskMessage{
		{ID: owner, Role: "assistant", Content: "editing"},
		toolRow(t, owner, "A", "edit_file", `{"path":"a.ts"}`, "edited"),
		{ID: "hint", Role: "user", Content: "You seem to be repeating yourself", IsLoopHint: true},
		toolRow(t, owner, "B", "edit_file", `{"path":"b.ts"}`, "edited"),
	}

	roles, calls, results := summarizeRebuild(rebuildRows(rows))
	if !slices.Equal(calls, []string{"A", "B"}) || !slices.Equal(results, []string{"A", "B"}) {
		t.Fatalf("calls=%v results=%v", calls, results)
	}
	for _, r := range roles {
		if r == "user" {
			t.Fatalf("loop hint leaked into the LLM context: %v", roles)
		}
	}
}

// TestBuildMessagesFromDB_MissingToolResultStaysMissing: a call that never produced a
// result is still reported on the assistant message, but no result is invented for it.
func TestBuildMessagesFromDB_MissingToolResultStaysMissing(t *testing.T) {
	t.Parallel()
	const owner = "asst-1"
	rows := []types.TaskMessage{
		{ID: owner, Role: "assistant", Content: "editing"},
		toolRow(t, owner, "A", "bash", `{}`, "ran"),
		toolRow(t, owner, "B", "bash", `{}`, ""), // crashed before the result was stored
	}

	_, calls, results := summarizeRebuild(rebuildRows(rows))
	if !slices.Equal(calls, []string{"A", "B"}) {
		t.Fatalf("calls = %v, want [A B]", calls)
	}
	if !slices.Equal(results, []string{"A"}) {
		t.Fatalf("results = %v, want [A]", results)
	}
}

// The system prompt's summary is read off the rebuilt conversation, so a summarization row has to
// survive that rebuild: a row dropped from the conversation is a summary missing from the prompt.
func TestRebuildKeepsTheSummarizationRows(t *testing.T) {
	t.Parallel()
	rows := []types.TaskMessage{
		{Role: "user", Content: "first request"},
		{Role: "summarization", Content: "the superseded summary"},
		{Role: "user", Content: "second request"},
		{Role: "summarization", Content: "the edited summary"},
	}
	if got := extractPrevSummary(rebuildRows(rows)); got != "the edited summary" {
		t.Fatalf("the rebuilt conversation does not carry the summary in effect: %q", got)
	}
}
