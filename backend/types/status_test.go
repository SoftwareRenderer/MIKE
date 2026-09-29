package types

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStatusWireFormatIsFrozen: existing rows hold these strings, the frontend compares on
// them and WebSocket payloads carry them. Renaming a constant is safe; changing a value
// orphans every card in that column, and no Go type can catch that.
func TestStatusWireFormatIsFrozen(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		got  Status
		want string
	}{
		{StatusBacklog, "backlog"},
		{StatusInProgress, "in_progress"},
		{StatusReview, "review"},
		{StatusDone, "done"},
	} {
		if string(tc.got) != tc.want {
			t.Errorf("Status %q changed wire value to %q — rows, the board and live events all key on this string", tc.want, string(tc.got))
		}
	}
}

func TestParseStatus(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"backlog", "in_progress", "review", "done"} {
		if _, ok := ParseStatus(s); !ok {
			t.Errorf("ParseStatus(%q) = not-ok, want ok", s)
		}
	}
	// The shapes a stray client value actually arrives in: a typo, a different case,
	// a padded value, and the empty string a partial-update request may send.
	for _, s := range []string{"baklog", "in-progress", "Backlog", " done", "", "reviewed", "DONE"} {
		got, ok := ParseStatus(s)
		if ok {
			t.Errorf("ParseStatus(%q) = %q,ok — must reject, or the task lands on no column", s, got)
		}
		if got != "" {
			t.Errorf("ParseStatus(%q) returned a non-zero Status with ok=false", s)
		}
	}
}

// TestTaskStatusJSONRoundTrip: the typed field must marshal exactly as the string replaced.
func TestTaskStatusJSONRoundTrip(t *testing.T) {
	t.Parallel()
	blob := `{"status":"review","execution_state":"running"}`
	var task Task
	if err := json.Unmarshal([]byte(blob), &task); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if task.Status != StatusReview {
		t.Fatalf("Status = %q, want %q", task.Status, StatusReview)
	}
	out, err := json.Marshal(Task{Status: task.Status, ExecutionState: task.ExecutionState})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"status":"review"`) {
		t.Errorf("marshalled task lost the wire form: %s", out)
	}
}
