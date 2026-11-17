package server

// Which row the browser holds, answered over HTTP. The board is what a browser refetches on every board
// change and caches; the read of one task is the route a program holds a token for. The two may differ in
// one field only, and only in its value.

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"slices"
	"strings"
	"testing"
)

const probeResultText = "the agent's whole reported result, sentence after sentence"

func TestBoardRowDropsResultTextAndOneTaskReadKeepsIt(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "bandwidth row")

	put := testRequest("PUT", "/api/projects/"+projID+"/tasks/"+taskID, map[string]any{"output": probeResultText})
	if rr := testResponseAuth(t, s, put); rr.Code != http.StatusOK {
		t.Fatalf("storing the result text: %d %s", rr.Code, rr.Body.String())
	}

	boardRaw := getAuth(t, s, "/api/projects/"+projID+"/board")
	if bytes.Contains(boardRaw, []byte(probeResultText)) {
		t.Fatalf("the board carried the result text: %s", boardRaw)
	}
	var board struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(boardRaw, &board); err != nil {
		t.Fatalf("decode board: %v", err)
	}
	if len(board.Tasks) != 1 {
		t.Fatalf("the board holds %d tasks, want the one this test created", len(board.Tasks))
	}
	row := board.Tasks[0]
	if row["id"] != taskID {
		t.Fatalf("board row is %v, want task %s", row["id"], taskID)
	}
	if row["output"] != "" {
		t.Fatalf("the board row's output is %v, want the empty string", row["output"])
	}
	// Only a value changed. The card and the detail view read this row field by field.
	for _, field := range []string{"title", "prompt", "status", "execution_state", "created_at", "moved_at"} {
		if _, held := row[field]; !held {
			t.Fatalf("the board row dropped %q; it holds %s", field, strings.Join(sortedKeys(row), ","))
		}
	}

	oneRaw := getAuth(t, s, "/api/projects/"+projID+"/tasks/"+taskID)
	if !bytes.Contains(oneRaw, []byte(probeResultText)) {
		t.Fatalf("the read of one task lost the result text: %s", oneRaw)
	}
	var one map[string]any
	if err := json.Unmarshal(oneRaw, &one); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if keys := strings.Join(sortedKeys(one), ","); keys != strings.Join(sortedKeys(row), ",") {
		t.Fatalf("the two answers differ in shape, not just in a value:\n board %s\n  task %s", strings.Join(sortedKeys(row), ","), keys)
	}
}

// TestBoardListsEveryTaskOfTheProject is the board's completeness rule: one row per task of the
// project, whatever the column and whatever the depth. A board that lists roots alone renders a
// column with its subtasks missing, and the counts beside the column header come from the same rows.
func TestBoardListsEveryTaskOfTheProject(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "board-parent")
	rootID := createTestTask(t, s, projID, "board-root")
	subID := createTestSubtask(t, s, projID, parentID, "board-subtask")
	moveTask(t, s, projID, subID, "review")

	board := mustJSON[map[string]any](t, bytes.NewReader(getAuth(t, s, "/api/projects/"+projID+"/board")))
	rows := map[string]map[string]any{}
	for _, raw := range board["tasks"].([]any) {
		row := raw.(map[string]any)
		rows[row["id"].(string)] = row
	}
	for id, wantStatus := range map[string]string{parentID: "backlog", rootID: "backlog", subID: "review"} {
		row, held := rows[id]
		if !held {
			t.Fatalf("the board left out %s; it holds %d rows", id, len(rows))
		}
		if row["status"] != wantStatus {
			t.Errorf("%s is on the board as %v, want %q", id, row["status"], wantStatus)
		}
	}
}

func sortedKeys(row map[string]any) []string {
	keys := make([]string, 0, len(row))
	for key := range row {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
