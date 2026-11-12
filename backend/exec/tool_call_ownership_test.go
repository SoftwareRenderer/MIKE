package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
)

// awaitResultRows returns the first n tool_call rows once every one of them carries a stored result.
// Reading earlier would read a turn that is still being written.
func (r *interruptRun) awaitResultRows(t *testing.T, n int) []types.TaskMessage {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		rows := r.toolRows(t)
		if len(rows) >= n {
			complete := true
			for _, row := range rows[:n] {
				if row.ToolResult == "" {
					complete = false
				}
			}
			if complete {
				return rows[:n]
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d tool calls were answered", len(rows), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Stopping a task removes its execution state, and the turn in progress finds that out only when its
// next provider call fails. The tool calls that turn already holds have to be answered as stopped, since
// with that state gone there is nothing left to tell them where to read and write.
func TestStoppedTurnDoesNotDispatchItsRemainingToolCalls(t *testing.T) {
	t.Parallel()
	run := newInterruptRun(t, &types.Task{Title: "Stop mid-batch", Prompt: "Work"},
		toolCallsScript(
			scriptToolCall{id: "call_1", name: "bash", args: `{"command":"` + bashSleepThen + `","timeout":90}`},
			scriptToolCall{id: "call_2", name: "read_file", args: `{"path":"started.txt"}`},
			scriptToolCall{id: "call_3", name: "bash", args: `{"command":"echo third > third.txt"}`},
		),
		textScript("Stopping. What should I do instead?"),
	)

	run.awaitStreamCalls(t, 1)
	run.awaitFile(t, "started.txt")

	if err := run.te.StopTask(run.taskID); err != nil {
		t.Fatalf("stop the task: %v", err)
	}

	rows := run.awaitResultRows(t, 3)
	for _, row := range rows[1:] {
		got := storedResult(t, row)
		if got["output"] != stoppedToolResult {
			t.Errorf("tool %s stored output %v, want %q", row.ID, got["output"], stoppedToolResult)
		}
		if got["success"] != false {
			t.Errorf("tool %s stored success %v, want false", row.ID, got["success"])
		}
	}
	for _, row := range rows {
		if strings.Contains(row.ToolResult, "internal error while running") {
			t.Errorf("tool %s was dispatched after the stop; it stored %q", row.ID, row.ToolResult)
		}
	}

	// The third call never ran, so its file was never written.
	if _, err := os.Stat(filepath.Join(run.worktreePath, "third.txt")); err == nil {
		t.Error("third.txt exists: a call after the stop ran anyway")
	}
}
