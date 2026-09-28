package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
)

// truncatedBashArgs is the argument text a model really produced in task ridge-meteor-anvil, with a
// command that leaves a trace when it runs. The same command with intact arguments is what proves the
// refusal — and not a shell that failed on its own — is what kept the file from appearing.
func truncatedBashArgs() string {
	return `{"command": "touch marker.txt", "timeout\": 300}` + "\n" + `</parameter": `
}

// sseBashCall answers with one bash tool call whose arguments are rawArgs, verbatim: whatever the model
// produced, including text that never closed its object.
func sseBashCall(w http.ResponseWriter, id, rawArgs string) {
	w.Header().Set("Content-Type", "text/event-stream")
	quoted, err := json.Marshal(rawArgs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":"bash","arguments":%s}}]},"finish_reason":"tool_calls"}]}`+"\n\n", id, quoted)
	fmt.Fprint(w, sseDone)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// runBashTurn runs one task whose mock provider answers first with a bash call carrying rawArgs, then
// with a text answer, and returns the worktree and the thread's rows once a tool call has a result.
func runBashTurn(t *testing.T, taskID, rawArgs string) (string, []types.TaskMessage) {
	t.Helper()
	srv, _ := crashLLM(t,
		func(w http.ResponseWriter) { sseBashCall(w, "call_1", rawArgs) },
		func(w http.ResponseWriter) { sseText(w, "Done. Anything else?") },
	)
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	worktreePath := crashTask(t, te, projectID, initialSHA, taskID, srv.URL)
	startExecRun(t, te, projectID, taskID, srv.URL, worktreePath)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		msgs := mustMessages(t, te, taskID, "thread-"+taskID)
		if len(msgs) > 0 {
			if row := toolCallRowWithResult(t, msgs); row != nil {
				return worktreePath, msgs
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	var errs []string
	for _, m := range mustMessages(t, te, taskID, "thread-"+taskID) {
		if m.Role == "error" {
			errs = append(errs, m.Content)
		}
	}
	t.Fatalf("no tool_call row with a result within 60s; roles = %v errors = %v",
		rolesInOrder(t, te, taskID, "thread-"+taskID), errs)
	return "", nil
}

// toolCallRowWithResult returns the thread's executed tool call row, or nil while it has no result.
func toolCallRowWithResult(t *testing.T, msgs []types.TaskMessage) *types.TaskMessage {
	t.Helper()
	for i := range msgs {
		if msgs[i].Role == "tool_call" && msgs[i].ToolResult != "" {
			return &msgs[i]
		}
	}
	return nil
}

func TestToolCallWithTruncatedArgumentsIsRefusedWithoutRunning(t *testing.T) {
	t.Parallel()
	const taskID = "refused-truncated-args"
	worktreePath, msgs := runBashTurn(t, taskID, truncatedBashArgs())

	row := toolCallRowWithResult(t, msgs)
	if row == nil {
		t.Fatal("no executed tool call row")
	}
	if !json.Valid([]byte(row.ToolResult)) {
		t.Fatalf("tool_result is not the stored JSON shape: %q", row.ToolResult)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(row.ToolResult), &stored); err != nil {
		t.Fatalf("tool_result: %v", err)
	}
	if stored["success"] == true {
		t.Errorf("a call with undecodable arguments reported success: %q", row.ToolResult)
	}
	output, _ := stored["output"].(string)
	if !strings.Contains(output, "not valid JSON") {
		t.Errorf("the model was not told its arguments were unusable: %q", output)
	}
	if !strings.Contains(output, "touch marker.txt") {
		t.Errorf("the model was not shown what of its call survived: %q", output)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "marker.txt")); !os.IsNotExist(err) {
		t.Errorf("the refused command ran anyway (stat: %v); a call whose arguments did not decode "+
			"must never reach the tool", err)
	}
	// The row keeps the model's own text for the UI and for debugging; only the request is normalized.
	if !strings.Contains(row.ToolCalls, "timeout") {
		t.Errorf("tool_call row lost the arguments as the model sent them: %q", row.ToolCalls)
	}
}

// TestToolCallWithIntactArgumentsRunsTheSameCommand: the refusal above is only meaningful if the same
// command runs when its arguments survive.
func TestToolCallWithIntactArgumentsRunsTheSameCommand(t *testing.T) {
	t.Parallel()
	const taskID = "ran-intact-args"
	worktreePath, msgs := runBashTurn(t, taskID, `{"command": "touch marker.txt"}`)

	if _, err := os.Stat(filepath.Join(worktreePath, "marker.txt")); err != nil {
		t.Errorf("the command did not run: %v", err)
	}
	row := toolCallRowWithResult(t, msgs)
	if output := fmt.Sprint(row.ToolResult); strings.Contains(output, "not valid JSON") {
		t.Errorf("a call that decoded was refused: %q", output)
	}
}

func TestUnparseableToolArgs_RefusesOnlyWhatCouldNotBeDecoded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		rawArgs string
		parsed  map[string]any
		want    bool
	}{
		{"no arguments at all is a zero-argument call", "", nil, false},
		{"empty object", "{}", map[string]any{}, false},
		{"valid object", `{"command": "true"}`, map[string]any{"command": "true"}, false},
		{"salvaged by a lenient repair", `{"{path":"a"}`, map[string]any{"path": "a"}, false},
		{"truncated by the model", truncatedBashArgs(), nil, true},
		{"cut mid-string", `{"command": "gr`, nil, true},
	}
	for _, tc := range cases {
		got := unparseableToolArgs(tc.rawArgs, tc.parsed)
		if (got != "") != tc.want {
			t.Errorf("%s: refusal=%v, want refusal=%v (message %q)", tc.name, got != "", tc.want, got)
		}
	}
}

// TestUnparseableToolArgs_QuotedArgumentsAreBounded: a call can be garbled for 24 KB, and its own text
// must not cost the conversation that much on every later turn.
func TestUnparseableToolArgs_QuotedArgumentsAreBounded(t *testing.T) {
	t.Parallel()
	raw := `{"command": "` + strings.Repeat("x", 24*1024)

	got := unparseableToolArgs(raw, nil)

	if !strings.Contains(got, "... (truncated)") {
		t.Errorf("no truncation marker in %d bytes of refusal", len(got))
	}
	if len(got) > 1024 {
		t.Errorf("refusal is %d bytes, want the quoted arguments bounded to %d", len(got), maxEchoedArgsBytes)
	}
}

func TestBashToolWithoutCommandRunsNoShell(t *testing.T) {
	t.Parallel()
	te := newBareTestExecutor(t)
	dir := t.TempDir()

	result, ok := te.executeTool(context.Background(), types.ToolBash, map[string]any{"timeout": float64(5)},
		dir, &types.Task{}, &executionState{}, nil)

	if ok {
		t.Errorf("an empty command reported success: %q", result)
	}
	if !strings.Contains(result, "command is required") {
		t.Errorf("result = %q, want the model told the command argument is missing", result)
	}
}
