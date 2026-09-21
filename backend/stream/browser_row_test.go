package stream

// The row the browser holds: the stored row with the agent's result text removed and nothing else changed.
// Both halves are held: the text is gone from what the socket carries, and the row still arrives with every
// field the card and the detail view read.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
)

const probeResultText = "the agent's whole reported result, sentence after sentence"

func taskWithResult() *types.Task {
	return &types.Task{
		ID: "task-row", ProjectID: "proj-row", Title: "row", Prompt: "the prompt",
		Output: probeResultText, Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		Branch: "task/row", SessionCost: 1.25,
	}
}

func TestTaskForBrowserClearsTheResultAndKeepsEveryKey(t *testing.T) {
	stored := taskWithResult()
	row := TaskForBrowser(stored)

	if row.Output != "" {
		t.Fatalf("the row still carries the result text: %q", row.Output)
	}
	// The caller's row belongs to the store or to a running turn. Writing through it would clear the
	// stored text on the next full-row write.
	if stored.Output != probeResultText {
		t.Fatalf("the projection wrote through to the caller's row: %q", stored.Output)
	}
	if keysOf(t, stored) != keysOf(t, row) {
		t.Fatalf("the projection changed the shape of the row:\n stored %s\n browser %s", keysOf(t, stored), keysOf(t, row))
	}
	if TaskForBrowser(nil) != nil {
		t.Fatal("TaskForBrowser(nil) answered with a row")
	}
}

func TestBrowserRowsClearsEveryRowAndLeavesTheInput(t *testing.T) {
	stored := []types.Task{*taskWithResult(), *taskWithResult()}
	stored[1].ID = "task-row-2"

	rows := BrowserRows(stored)
	if len(rows) != 2 {
		t.Fatalf("%d rows returned for 2 tasks", len(rows))
	}
	for _, row := range rows {
		if row.Output != "" {
			t.Fatalf("row %s still carries the result text", row.ID)
		}
	}
	for i := range stored {
		if stored[i].Output != probeResultText {
			t.Fatalf("BrowserRows wrote through to the caller's slice at %d", i)
		}
	}
}

// TestTaskUpdatedFrameCarriesNoResultText: this frame goes to every tab of the project on each status change.
func TestTaskUpdatedFrameCarriesNoResultText(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	t.Cleanup(asm.Close)

	live := dialStream(t, asm)
	client := clientAlone(t, asm)
	const projectID = "proj-frame"
	live.sendSubscription(t, "subscribe_project", projectID, "")
	awaitProjectSub(t, asm, client, projectID)

	task := taskWithResult()
	task.ProjectID = projectID
	asm.BroadcastTaskUpdated(projectID, task)

	frame := live.awaitFrame(t, 5*time.Second, func(f map[string]any) bool { return f["type"] == "task_updated" },
		"the task_updated frame")
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	if strings.Contains(string(raw), probeResultText) {
		t.Fatalf("the frame carried the result text: %s", raw)
	}
	row, ok := frame["task"].(map[string]any)
	if !ok {
		t.Fatalf("frame has no task object: %s", raw)
	}
	for _, field := range []string{"id", "project_id", "title", "prompt", "status", "execution_state", "branch", "output"} {
		if _, held := row[field]; !held {
			t.Fatalf("the projected row dropped %q; the frame carries %s", field, raw)
		}
	}
	if row["output"] != "" {
		t.Fatalf("output is %v, want the empty string", row["output"])
	}
	// The broadcast left the row it was handed alone.
	if task.Output != probeResultText {
		t.Fatalf("BroadcastTaskUpdated cleared the caller's row: %q", task.Output)
	}
}

// awaitProjectSub waits for the server to apply a project subscription. A broadcast sent before it lands
// reaches nobody, and the test would pass on nothing.
func awaitProjectSub(t *testing.T, asm *AppStreamManager, client *clientState, projectID string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		asm.mu.RLock()
		_, subscribed := client.projectSub[projectID]
		asm.mu.RUnlock()
		if subscribed {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("project=%s was never subscribed on this connection", projectID)
		case <-time.After(time.Millisecond):
		}
	}
}

func keysOf(t *testing.T, task *types.Task) string {
	t.Helper()
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}
	keys := make([]string, 0, len(row))
	for key := range row {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return strings.Join(keys, ",")
}
