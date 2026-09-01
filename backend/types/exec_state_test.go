package types

import "testing"

// TestExecutionStateWireFormatIsFrozen: the six values are what the UI's spinner,
// badge and start/stop logic key off, what existing rows hold, and what the status
// WebSocket frames carry. A renamed constant is a refactor; a changed value silently
// strands every task whose state no longer matches a branch in the UI.
func TestExecutionStateWireFormatIsFrozen(t *testing.T) {
	for _, tc := range []struct {
		got  ExecutionState
		want string
	}{
		{ExecStateIdle, "idle"},
		{ExecStateRunning, "running"},
		{ExecStatePullingImage, "pulling_image"},
		{ExecStatePaused, "paused"},
		{ExecStateCompleted, "completed"},
		{ExecStateFailed, "failed"},
	} {
		if string(tc.got) != tc.want {
			t.Errorf("ExecutionState %q changed wire value to %q", tc.want, string(tc.got))
		}
	}
}

func TestParseExecutionState(t *testing.T) {
	for _, s := range []string{"idle", "running", "pulling_image", "paused", "completed", "failed"} {
		if _, ok := ParseExecutionState(s); !ok {
			t.Errorf("ParseExecutionState(%q) = not-ok, want ok", s)
		}
	}
	// "merge_conflict" is the interesting rejection: it used to be an execution state
	// and now lives in Task.HasMergeConflict, so a client still sending it must not be
	// able to write a state nothing reads.
	for _, s := range []string{"merge_conflict", "runing", "Stopped", "stopped", "", " complete"} {
		if got, ok := ParseExecutionState(s); ok {
			t.Errorf("ParseExecutionState(%q) = %q,ok — must reject", s, got)
		}
	}
}
